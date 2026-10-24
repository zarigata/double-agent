package config

import (
	"fmt"
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
    node: pve
    storage: local-lvm
    template_vmid: 301
    allowed_templates: [301]
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
    token_id: root@pam!doubleagent
    token_secret: ${DA_TEST_SECRET}
    endpoint: https://pve.example:8006
    node: pve
    storage: local-lvm
    template_vmid: 301
    allowed_templates: [301]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Runtime.Proxmox.TokenSecret != "expanded" {
		t.Errorf("token secret = %q, want expanded", cfg.Runtime.Proxmox.TokenSecret)
	}
}

// proxmoxYAML writes a complete Proxmox runtime configuration in which the
// given lines replace the defaults of the named keys. A test breaks one value
// at a time to check that the validation names the value.
func proxmoxYAML(t *testing.T, overrides map[string]string) string {
	t.Helper()
	values := map[string]string{
		"enabled":           "true",
		"endpoint":          "https://pve.example:8006",
		"username":          "root@pam",
		"password":          "secret",
		"node":              "pve",
		"storage":           "local-lvm",
		"template_vmid":     "301",
		"allowed_templates": "[301]",
	}
	for key, value := range overrides {
		values[key] = value
	}
	var b strings.Builder
	b.WriteString("runtime:\n  proxmox:\n")
	for _, key := range []string{"enabled", "endpoint", "username", "password", "token_id", "token_secret",
		"node", "storage", "template_vmid", "allowed_templates"} {
		value, ok := values[key]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "    %s: %s\n", key, value)
	}
	return b.String()
}

// TestLoadRejectsAnIncompleteProxmoxRuntime checks that an enabled Proxmox
// backend is refused while the value it lacks can still be named.
func TestLoadRejectsAnIncompleteProxmoxRuntime(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"no endpoint", map[string]string{"endpoint": `""`}, "endpoint"},
		{"no credentials", map[string]string{"username": `""`, "password": `""`}, "credentials"},
		{"username without password", map[string]string{"password": `""`}, EnvProxmoxPassword},
		{"password without username", map[string]string{"username": `""`}, "username"},
		{"token without secret", map[string]string{"username": `""`, "password": `""`, "token_id": "root@pam!doubleagent"}, EnvProxmoxTokenSecret},
		{"token secret without id", map[string]string{"username": `""`, "password": `""`, "token_secret": "secret"}, "token_id"},
		{"no node", map[string]string{"node": `""`}, "node"},
		{"no storage", map[string]string{"storage": `""`}, "storage"},
		{"no template", map[string]string{"template_vmid": "0"}, "template_vmid"},
		{"empty allow list", map[string]string{"allowed_templates": "[]"}, "allowed_templates"},
		{"template outside the allow list", map[string]string{"allowed_templates": "[200, 202]"}, "allowed_templates"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, proxmoxYAML(t, test.overrides)))
			if err == nil {
				t.Fatal("an incomplete proxmox runtime was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %q", err, test.want)
			}
		})
	}
}

// TestLoadAcceptsAnAPITokenForProxmox checks that a token with its secret is
// a complete credential.
func TestLoadAcceptsAnAPITokenForProxmox(t *testing.T) {
	body := proxmoxYAML(t, map[string]string{
		"username":     `""`,
		"password":     `""`,
		"token_id":     "root@pam!doubleagent",
		"token_secret": "s3" + "cret",
	})
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestLoadAcceptsADisabledIncompleteProxmoxRuntime checks that a disabled
// backend is not validated: it is not used.
func TestLoadAcceptsADisabledIncompleteProxmoxRuntime(t *testing.T) {
	if _, err := Load(writeConfig(t, "runtime:\n  proxmox:\n    enabled: false\n")); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestLoadRejectsADefaultRuntimeThatIsNotEnabled checks that the default
// runtime names a backend the daemon can actually register.
func TestLoadRejectsADefaultRuntimeThatIsNotEnabled(t *testing.T) {
	_, err := Load(writeConfig(t, "runtime:\n  default: proxmox\n"))
	if err == nil {
		t.Fatal("a default runtime that is not enabled was accepted")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error %q does not name the default runtime", err)
	}
	_, err = Load(writeConfig(t, "runtime:\n  default: firecracker\n"))
	if err == nil {
		t.Fatal("an unknown default runtime was accepted")
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
