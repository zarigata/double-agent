package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfigFile writes a configuration file into a temporary directory.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
	return path
}

// TestDefaultIsValid checks that the built-in configuration passes its own
// validation.
func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the default configuration is invalid: %v", err)
	}
}

// TestDefaultCarriesTheSafeValues checks the values a node runs with when no
// configuration file is given.
func TestDefaultCarriesTheSafeValues(t *testing.T) {
	cfg := Default()
	if cfg.Listen.Port != 7443 {
		t.Errorf("port = %d, want 7443", cfg.Listen.Port)
	}
	if cfg.Listen.Address != "0.0.0.0" {
		t.Errorf("address = %q, want 0.0.0.0", cfg.Listen.Address)
	}
	if !cfg.Listen.TLS.Enabled {
		t.Error("the default configuration does not use TLS")
	}
	if cfg.Runtime.Default != "docker" {
		t.Errorf("the default runtime is %q, want docker", cfg.Runtime.Default)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log level = %q, want info", cfg.LogLevel)
	}
	if cfg.Auth.SessionTTL.Duration() != 12*time.Hour {
		t.Errorf("the session lifetime is %s, want 12h", cfg.Auth.SessionTTL.Duration())
	}
	if cfg.Limits.MaxConcurrentJobs != 4 {
		t.Errorf("the job limit is %d, want 4", cfg.Limits.MaxConcurrentJobs)
	}
}

// TestLoadWithoutAFileReturnsTheDefaults checks that an empty path yields a
// working configuration.
func TestLoadWithoutAFileReturnsTheDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load the defaults: %v", err)
	}
	if cfg.Listen.Port != 7443 {
		t.Errorf("port = %d, want 7443", cfg.Listen.Port)
	}
	if cfg.Path() != "" {
		t.Errorf("the configuration claims to come from %q", cfg.Path())
	}
}

// TestLoadAppliesTheDefaultsOfTheMissingKeys checks that a minimal file
// still produces a complete configuration.
func TestLoadAppliesTheDefaultsOfTheMissingKeys(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, "log_level: debug\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log level = %q, want debug", cfg.LogLevel)
	}
	if cfg.Listen.Port != 7443 {
		t.Errorf("port = %d, want 7443", cfg.Listen.Port)
	}
	if cfg.Runtime.Default != "docker" {
		t.Errorf("the default runtime is %q, want docker", cfg.Runtime.Default)
	}
	if cfg.Auth.SessionTTL.Duration() != 12*time.Hour {
		t.Errorf("the session lifetime is %s, want 12h", cfg.Auth.SessionTTL.Duration())
	}
	if cfg.Artifacts.MaxUploadBytes != 256<<20 {
		t.Errorf("the upload limit is %d, want %d", cfg.Artifacts.MaxUploadBytes, 256<<20)
	}
}

// TestLoadRejectsAnInvalidPort checks that a port outside of the valid range
// is refused.
func TestLoadRejectsAnInvalidPort(t *testing.T) {
	for _, port := range []string{"70000", "-1", "65536"} {
		t.Run(port, func(t *testing.T) {
			_, err := Load(writeConfigFile(t, "listen:\n  port: "+port+"\n"))
			if err == nil {
				t.Fatalf("the port %s was accepted", port)
			}
		})
	}
}

// TestLoadRejectsRequireTLSWithoutTLS checks that the transport requirement
// cannot be satisfied by a plaintext listener.
func TestLoadRejectsRequireTLSWithoutTLS(t *testing.T) {
	_, err := Load(writeConfigFile(t, `
auth:
  require_tls: true
listen:
  tls:
    enabled: false
`))
	if err == nil {
		t.Fatal("a plaintext listener was accepted while TLS is required")
	}
}

// TestLoadAcceptsRequireTLSWithTLS checks the valid combination.
func TestLoadAcceptsRequireTLSWithTLS(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, `
auth:
  require_tls: true
listen:
  tls:
    enabled: true
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Auth.RequireTLS || !cfg.Listen.TLS.Enabled {
		t.Error("the transport settings were not applied")
	}
}

// TestLoadRejectsAnInvalidDuration checks that a duration that cannot be
// parsed is refused.
func TestLoadRejectsAnInvalidDuration(t *testing.T) {
	_, err := Load(writeConfigFile(t, "auth:\n  session_ttl: soon\n"))
	if err == nil {
		t.Fatal("an invalid duration was accepted")
	}
}

// TestTheDerivedPathsArePopulated checks the paths the daemon derives from
// the data directory.
func TestTheDerivedPathsArePopulated(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfigFile(t, "data_dir: "+dir+"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if want := filepath.Join(dir, "state", "double-agent.db"); cfg.DatabasePath() != want {
		t.Errorf("the database path is %q, want %q", cfg.DatabasePath(), want)
	}
	if want := filepath.Join(dir, "pki"); cfg.PKIPath() != want {
		t.Errorf("the pki path is %q, want %q", cfg.PKIPath(), want)
	}
	if want := filepath.Join(dir, "artifacts"); cfg.Artifacts.Root != want {
		t.Errorf("the artifact root is %q, want %q", cfg.Artifacts.Root, want)
	}
	if want := filepath.Join(dir, "logs"); cfg.LogPath() != want {
		t.Errorf("the log path is %q, want %q", cfg.LogPath(), want)
	}
	if want := filepath.Join(dir, "state"); cfg.StatePath() != want {
		t.Errorf("the state path is %q, want %q", cfg.StatePath(), want)
	}
}

// TestAnExplicitArtifactRootIsKept checks that a configured artifact root is
// not overwritten by the derived one.
func TestAnExplicitArtifactRootIsKept(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "elsewhere")
	cfg, err := Load(writeConfigFile(t, "data_dir: "+dir+"\nartifacts:\n  root: "+root+"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Artifacts.Root != root {
		t.Errorf("the artifact root is %q, want %q", cfg.Artifacts.Root, root)
	}
}

// TestTheMaximumLifetimeIsNeverShorterThanTheDefaultOne checks the clamping
// of the lifetimes.
func TestTheMaximumLifetimeIsNeverShorterThanTheDefaultOne(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, "limits:\n  default_ttl: 10h\n  max_ttl: 1h\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Limits.MaxTTL < cfg.Limits.DefaultTTL {
		t.Errorf("the maximum lifetime %s is shorter than the default %s",
			cfg.Limits.MaxTTL.Duration(), cfg.Limits.DefaultTTL.Duration())
	}
}
