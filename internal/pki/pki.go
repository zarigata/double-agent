// Package pki implements the local certificate authority of the Double Agent
// control plane.
//
// The node runs its own HTTPS listener and the browsers on the LAN must trust
// it, so the daemon generates a self signed CA and issues the server
// certificate from it. The CA certificate is published over the network so an
// operator can install it once.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Authority is the local certificate authority.
type Authority struct {
	// Dir is the directory that holds the key material.
	Dir string
	// CertPath is the path of the CA certificate.
	CertPath string
	// KeyPath is the path of the CA private key.
	KeyPath string
}

// File names inside the PKI directory.
const (
	caCertFile   = "ca.crt"
	caKeyFile    = "ca.key"
	serverCert   = "server.crt"
	serverKey    = "server.key"
	caCertPerm   = 0o644
	caKeyPerm    = 0o600
	serverCertPe = 0o644
	serverKeyPe  = 0o600
)

// NewAuthority returns the authority rooted at dir.
func NewAuthority(dir string) Authority {
	return Authority{
		Dir:      dir,
		CertPath: filepath.Join(dir, caCertFile),
		KeyPath:  filepath.Join(dir, caKeyFile),
	}
}

// Load reads the CA material from disk.
func (a Authority) Load() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(a.CertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: read ca certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(a.KeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: read ca key: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("pki: ca certificate is not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parse ca certificate: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("pki: ca key is not a PEM block")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parse ca key: %w", err)
	}
	return cert, key, nil
}

// Ensure creates the CA and the server certificate if they are missing.
//
// The certificate is issued for the host names the node is reachable under,
// including every local address, so an operator can open the Web UI from any
// machine on the LAN.
func Ensure(dir string, hosts []string, validFor time.Duration) (caPath, certPath, keyPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", fmt.Errorf("pki: create directory: %w", err)
	}
	authority := NewAuthority(dir)

	caCert, caKey, err := authority.Load()
	if err != nil {
		if !os.IsNotExist(errUnwrap(err)) {
			// A malformed existing CA is reported rather than silently
			// replaced: overwriting it would invalidate the trust every
			// browser already installed.
			if _, statErr := os.Stat(authority.CertPath); statErr == nil {
				return "", "", "", fmt.Errorf("pki: the existing ca is unreadable: %w", err)
			}
		}
		caCert, caKey, err = generateCA(dir, validFor)
		if err != nil {
			return "", "", "", err
		}
	}

	certFile := filepath.Join(dir, serverCert)
	keyFile := filepath.Join(dir, serverKey)
	if fileExists(certFile) && fileExists(keyFile) {
		return authority.CertPath, certFile, keyFile, nil
	}
	if err := issueServer(authority, caCert, caKey, certFile, keyFile, hosts, validFor); err != nil {
		return "", "", "", err
	}
	return authority.CertPath, certFile, keyFile, nil
}

// generateCA creates a new self signed certificate authority.
func generateCA(dir string, validFor time.Duration) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate ca key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Double Agent Local CA",
			Organization: []string{"Double Agent"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: create ca certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parse generated ca: %w", err)
	}
	if err := writePEM(filepath.Join(dir, caCertFile), "CERTIFICATE", der, caCertPerm); err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshal ca key: %w", err)
	}
	if err := writePEM(filepath.Join(dir, caKeyFile), "EC PRIVATE KEY", keyDER, caKeyPerm); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// issueServer issues the HTTPS server certificate from the CA.
func issueServer(authority Authority, caCert *x509.Certificate, caKey *ecdsa.PrivateKey,
	certFile, keyFile string, hosts []string, validFor time.Duration) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "double-agent.local",
			Organization: []string{"Double Agent"},
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(validFor),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	template.DNSNames = append(template.DNSNames, "double-agent.local", "localhost")
	for _, h := range hosts {
		h = trimHost(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("pki: create server certificate: %w", err)
	}
	if err := writePEM(certFile, "CERTIFICATE", der, serverCertPe); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("pki: marshal server key: %w", err)
	}
	return writePEM(keyFile, "EC PRIVATE KEY", keyDER, serverKeyPe)
}

// writePEM writes a PEM encoded file with the given permissions.
func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("pki: write %s: %w", path, err)
	}
	// WriteFile only applies the permission when it creates the file, so the
	// mode is set explicitly for an existing file too.
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("pki: chmod %s: %w", path, err)
	}
	return nil
}

// randomSerial returns a random certificate serial number.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("pki: generate serial: %w", err)
	}
	return serial, nil
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// trimHost removes a port and brackets from a host specification.
func trimHost(h string) string {
	if h == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// errUnwrap extracts the underlying error of a wrapped chain.
func errUnwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
