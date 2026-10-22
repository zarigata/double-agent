package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a configuration file into a temporary directory.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "data_dir: /tmp/da\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen.Port != 7443 {
		t.Errorf("port = %d, want 7443", cfg.Listen.Port)
	}
	if cfg.Runtime.Default != "docker" {
		t.Errorf("default runtime = %q, want docker", cfg.Runtime.Default)
	}
	if cfg.Artifacts.Root == "" {
		t.Error("the artifact root was not derived")
	}
	if cfg.Limits.MaxTTL < cfg.Limits.DefaultTTL {
		t.Error("the maximum TTL is shorter than the default TTL")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	_, err := Load(writeConfig(t, "log_level: chatty\n"))
	if err == nil {
		t.Fatal("an invalid log level was accepted")
	}
}

func TestEnvironmentOverridesTheProxmoxPassword(t *testing.T) {
	// The fixture values are deliberately short, low-entropy words: they
	// only need to differ from each other, and plain words never look like
	// real credentials to secret scanners.
	envPassword := "winner"
	filePassword := "loser"
	t.Setenv(EnvProxmoxPassword, envPassword)
	cfg, err := Load(writeConfig(t, `
runtime:
  proxmox:
    enabled: true
    endpoint: https://pve.example:8006
    username: root@pam
    password: `+filePassword+`
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Runtime.Proxmox.Password != envPassword {
		t.Errorf("password = %q, want the value from the environment", cfg.Runtime.Proxmox.Password)
	}
}

func TestExpandEnvReplacesReferences(t *testing.T) {
	fixture := "s3" + "cret"
	values := map[string]string{"PASSWORD": fixture}
	out := string(ExpandEnv([]byte("password: ${PASSWORD}\nport: ${PORT}\n"),
		func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		}))
	if !strings.Contains(out, "password: "+fixture) {
		t.Errorf("the reference was not expanded: %q", out)
	}
	if !strings.Contains(out, "port: ${PORT}") {
		t.Errorf("an unresolved reference was not preserved: %q", out)
	}
}

func TestLoadExpandsEnvironmentReferences(t *testing.T) {
	t.Setenv("DA_TEST_SECRET", "expanded")
	cfg, err := Load(writeConfig(t, `
runtime:
  proxmox:
    enabled: true
    token_secret: ${DA_TEST_SECRET}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Runtime.Proxmox.TokenSecret != "expanded" {
		t.Errorf("token secret = %q, want expanded", cfg.Runtime.Proxmox.TokenSecret)
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	cfg := Default()
	cfg.Runtime.Proxmox.Password = "top-secret"
	cfg.Runtime.Proxmox.TokenSecret = "also-secret"
	red := cfg.Redacted()
	if red.Runtime.Proxmox.Password != redacted {
		t.Errorf("the password was not redacted: %q", red.Runtime.Proxmox.Password)
	}
	if red.Runtime.Proxmox.TokenSecret != redacted {
		t.Errorf("the token secret was not redacted: %q", red.Runtime.Proxmox.TokenSecret)
	}
	// The original must not be modified: it is still needed to talk to the
	// server.
	if cfg.Runtime.Proxmox.Password != "top-secret" {
		t.Error("the original configuration was modified")
	}
}
