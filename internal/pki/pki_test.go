package pki_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/pki"
)

// readCertificate reads a PEM encoded certificate from disk.
func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s is not a PEM block", path)
	}
	if block.Type != "CERTIFICATE" {
		t.Fatalf("%s holds the PEM block %q, want a certificate", path, block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert
}

// TestEnsureCreatesTheAuthorityAndTheServerCertificate checks the material
// that a node needs to serve HTTPS.
func TestEnsureCreatesTheAuthorityAndTheServerCertificate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	caPath, certPath, keyPath, err := pki.Ensure(dir, []string{"node.local"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, path := range []string{caPath, certPath, keyPath} {
		if path == "" {
			t.Fatal("ensure returned an empty path")
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not created: %v", path, err)
		}
	}
	// The directory of the authority is created as well.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the pki directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("the pki path is not a directory")
	}
}

// TestTheAuthorityCertificateIsParseable checks that the published
// certificate authority is a real certificate.
func TestTheAuthorityCertificateIsParseable(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _, err := pki.Ensure(dir, nil, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	cert := readCertificate(t, caPath)
	if !cert.IsCA {
		t.Error("the published certificate is not a certificate authority")
	}
	if cert.Subject.CommonName == "" {
		t.Error("the certificate authority has no common name")
	}
	if cert.NotAfter.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("the certificate expires at %s, want a lifetime of about a day", cert.NotAfter)
	}
}

// TestTheServerCertificateIsIssuedByTheAuthority checks the chain between the
// two certificates.
func TestTheServerCertificateIsIssuedByTheAuthority(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, _, err := pki.Ensure(dir, nil, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	caCert := readCertificate(t, caPath)
	serverCert := readCertificate(t, certPath)
	if err := serverCert.CheckSignatureFrom(caCert); err != nil {
		t.Errorf("the server certificate was not signed by the authority: %v", err)
	}
	if serverCert.Subject.CommonName == "" {
		t.Error("the server certificate has no common name")
	}
}

// TestTheCertificateCoversTheRequestedHosts checks the names and addresses
// the server certificate is valid for.
func TestTheCertificateCoversTheRequestedHosts(t *testing.T) {
	dir := t.TempDir()
	_, certPath, _, err := pki.Ensure(dir, []string{"node.local", "192.168.7.10", "studio.local:7443"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	cert := readCertificate(t, certPath)

	names := map[string]bool{}
	for _, name := range cert.DNSNames {
		names[name] = true
	}
	for _, want := range []string{"node.local", "studio.local", "localhost", "double-agent.local"} {
		if !names[want] {
			t.Errorf("the certificate does not cover %q, it covers %v", want, cert.DNSNames)
		}
	}
	addresses := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		addresses[ip.String()] = true
	}
	if !addresses["192.168.7.10"] {
		t.Errorf("the certificate does not cover the address, it covers %v", cert.IPAddresses)
	}
}

// TestASecondEnsureReusesTheAuthority checks that the certificate authority
// of a node is not replaced behind the back of the operators that installed
// it.
func TestASecondEnsureReusesTheAuthority(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _, err := pki.Ensure(dir, nil, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first := readCertificate(t, caPath)
	before, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read the certificate: %v", err)
	}

	secondPath, _, _, err := pki.Ensure(dir, nil, 24*time.Hour)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if secondPath != caPath {
		t.Errorf("the second call returned the path %q, want %q", secondPath, caPath)
	}
	after, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read the certificate: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the certificate authority was replaced by the second call")
	}
	second := readCertificate(t, caPath)
	if first.SerialNumber.Cmp(second.SerialNumber) != 0 {
		t.Errorf("the serial number changed: %s and %s", first.SerialNumber, second.SerialNumber)
	}
	if first.Subject.String() != second.Subject.String() {
		t.Errorf("the subject changed: %q and %q", first.Subject.String(), second.Subject.String())
	}
}

// TestTheAuthorityLoadsItsMaterial checks the reading of the stored key
// material.
func TestTheAuthorityLoadsItsMaterial(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := pki.Ensure(dir, nil, 24*time.Hour); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	authority := pki.NewAuthority(dir)
	cert, key, err := authority.Load()
	if err != nil {
		t.Fatalf("load the authority: %v", err)
	}
	if cert == nil || key == nil {
		t.Fatal("the authority did not return its material")
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("the certificate carries the key %T, want an ecdsa key", cert.PublicKey)
	}
	if !public.Equal(&key.PublicKey) {
		t.Error("the stored key does not belong to the certificate")
	}
}

// TestAnUnreadableAuthorityIsNotReplaced checks that a broken certificate
// authority is reported instead of being silently overwritten.
func TestAnUnreadableAuthorityIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "ca.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a certificate"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, _, _, err := pki.Ensure(dir, nil, 24*time.Hour); err == nil {
		t.Fatal("a broken certificate authority was replaced")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatalf("read the certificate: %v", err)
	}
	if string(raw) != "not a certificate" {
		t.Error("the broken certificate authority was overwritten")
	}
}

// TestThePrivateKeysAreNotWorldReadable checks the permissions of the key
// material.
func TestThePrivateKeysAreNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := pki.Ensure(dir, nil, 24*time.Hour); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, name := range []string{"ca.key", "server.key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is readable by others: mode %o", name, mode)
		}
	}
}
