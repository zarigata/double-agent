// Package config loads and validates the Double Agent daemon configuration.
//
// The configuration file is YAML. Every value has a safe default so that a
// minimal configuration file (or none at all) still produces a working
// development daemon.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a YAML string such as
// "15m" or "24h".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Duration returns the standard library duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// ListenConfig describes the HTTP listener.
type ListenConfig struct {
	// Address is the bind address. Only administrator selected LAN
	// interfaces should be bound; the default binds all interfaces.
	Address string `yaml:"address"`
	// Port is the TCP port for the Web UI, API and MCP endpoint.
	Port int `yaml:"port"`
	// TLS enables HTTPS with the local certificate authority.
	TLS TLSConfig `yaml:"tls"`
	// AllowedOrigins lists browser origins permitted to call the API. An
	// empty list rejects every cross-origin request.
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// TLSConfig describes the transport security settings.
type TLSConfig struct {
	// Enabled turns on HTTPS using the generated local PKI.
	Enabled bool `yaml:"enabled"`
	// CertFile overrides the generated server certificate.
	CertFile string `yaml:"cert_file"`
	// KeyFile overrides the generated server private key.
	KeyFile string `yaml:"key_file"`
	// CAFile overrides the generated certificate authority.
	CAFile string `yaml:"ca_file"`
}

// RuntimeConfig selects and configures the execution backends.
type RuntimeConfig struct {
	// Default is the backend used when a template does not pin one.
	Default string `yaml:"default"`
	// Docker configures the Docker Engine backend.
	Docker DockerConfig `yaml:"docker"`
	// Local enables the host process backend. It exists for development and
	// tests only; it is reported as unsupported by the doctor command.
	Local LocalConfig `yaml:"local"`
	// Proxmox configures the Proxmox VE backend. The backend provisions
	// disposable guests by cloning a template of the operator.
	Proxmox ProxmoxConfig `yaml:"proxmox"`
}

// ProxmoxConfig configures the Proxmox VE backend.
type ProxmoxConfig struct {
	// Enabled turns the backend on.
	Enabled bool `yaml:"enabled"`
	// Endpoint is the address of the pveproxy, e.g.
	// "https://192.168.1.79:8006".
	Endpoint string `yaml:"endpoint"`
	// Username is the Proxmox user, e.g. "root@pam".
	Username string `yaml:"username"`
	// Password is the password of the user. Prefer an API token.
	Password string `yaml:"password"`
	// TokenID names an API token, e.g. "root@pam!doubleagent".
	TokenID string `yaml:"token_id"`
	// TokenSecret is the secret of the API token.
	TokenSecret string `yaml:"token_secret"`
	// Insecure skips the verification of the TLS certificate of the server.
	Insecure bool `yaml:"insecure"`
	// Node is the node the guests are created on.
	Node string `yaml:"node"`
	// Storage is the storage the disks of the guests are placed on.
	Storage string `yaml:"storage"`
	// TemplateVMID is the guest that is cloned for a new instance.
	TemplateVMID int `yaml:"template_vmid"`
	// AllowedTemplates restricts the guests that may be cloned.
	AllowedTemplates []int `yaml:"allowed_templates"`
	// Bridge is the bridge the guests are attached to.
	Bridge string `yaml:"bridge"`
	// WorkspaceRoot is the directory that holds the instance workspaces.
	WorkspaceRoot string `yaml:"workspace_root"`
}

// DockerConfig configures the Docker Engine backend.
type DockerConfig struct {
	// Host is the Docker daemon endpoint.
	Host string `yaml:"host"`
	// AllowedImages restricts which image references may be started. An
	// empty list allows any image that is already present locally.
	AllowedImages []string `yaml:"allowed_images"`
	// PullImages allows the backend to pull missing images.
	PullImages bool `yaml:"pull_images"`
	// Network is the default egress policy applied to instances.
	Network string `yaml:"network"`
	// WorkspaceRoot is the directory that holds instance workspaces.
	WorkspaceRoot string `yaml:"workspace_root"`
}

// LocalConfig configures the development-only host process backend.
type LocalConfig struct {
	// Enabled turns the backend on. It must be explicitly enabled.
	Enabled bool `yaml:"enabled"`
	// WorkspaceRoot is the directory that holds instance workspaces.
	WorkspaceRoot string `yaml:"workspace_root"`
}

// AuthConfig holds authentication and authorization settings.
type AuthConfig struct {
	// SessionTTL is the lifetime of a browser session.
	SessionTTL Duration `yaml:"session_ttl"`
	// APIKeyTTL is the default lifetime of a client key.
	APIKeyTTL Duration `yaml:"api_key_ttl"`
	// BootstrapAdmin is the username of the first administrator.
	BootstrapAdmin string `yaml:"bootstrap_admin"`
	// RequireTLS refuses to serve plaintext HTTP.
	RequireTLS bool `yaml:"require_tls"`
}

// LimitsConfig holds the resource policy enforced by the scheduler.
type LimitsConfig struct {
	// HostReserve is the amount of CPU and memory kept free on the host.
	HostReserve Reserve `yaml:"host_reserve"`
	// MaxConcurrentJobs is the number of jobs running at the same time.
	MaxConcurrentJobs int `yaml:"max_concurrent_jobs"`
	// MaxInstancesPerUser limits how many live instances one user owns.
	MaxInstancesPerUser int `yaml:"max_instances_per_user"`
	// MaxInstances is the total number of live instances on the node.
	MaxInstances int `yaml:"max_instances"`
	// DefaultTTL is applied when a request does not specify one.
	DefaultTTL Duration `yaml:"default_ttl"`
	// MaxTTL is the longest lifetime an instance may be given.
	MaxTTL Duration `yaml:"max_ttl"`
}

// Reserve is a resource amount kept free on the host.
type Reserve struct {
	CPU      float64 `yaml:"cpu"`
	MemoryMB int     `yaml:"memory_mb"`
}

// ArtifactsConfig holds the artifact store settings.
type ArtifactsConfig struct {
	// Root is the directory that stores exported artifacts.
	Root string `yaml:"root"`
	// MaxUploadBytes is the largest single upload accepted.
	MaxUploadBytes int64 `yaml:"max_upload_bytes"`
	// Retention is how long artifacts are kept by default.
	Retention Duration `yaml:"retention"`
	// MaxSizeBytes is the largest single artifact accepted.
	MaxSizeBytes int64 `yaml:"max_size_bytes"`
}

// DiscoveryConfig holds the mDNS advertisement settings.
type DiscoveryConfig struct {
	// Enabled turns on mDNS advertisement.
	Enabled bool `yaml:"enabled"`
	// InstanceName is the advertised node name.
	InstanceName string `yaml:"instance_name"`
	// Port is the advertised port. Zero uses the listener port.
	Port int `yaml:"port"`
}

// Config is the complete daemon configuration.
type Config struct {
	// DataDir is the root of all persistent daemon state.
	DataDir string `yaml:"data_dir"`
	// LogLevel is one of debug, info, warn or error.
	LogLevel string `yaml:"log_level"`
	// Listen configures the HTTP listener.
	Listen ListenConfig `yaml:"listen"`
	// Runtime configures the execution backends.
	Runtime RuntimeConfig `yaml:"runtime"`
	// Auth configures authentication.
	Auth AuthConfig `yaml:"auth"`
	// Limits configures the scheduler policy.
	Limits LimitsConfig `yaml:"limits"`
	// Artifacts configures the artifact store.
	Artifacts ArtifactsConfig `yaml:"artifacts"`
	// Discovery configures mDNS advertisement.
	Discovery DiscoveryConfig `yaml:"discovery"`
	// TemplatesDir holds extra template manifests.
	TemplatesDir string `yaml:"templates_dir"`

	// path records where the configuration was loaded from.
	path string
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		DataDir:  "/var/lib/double-agent",
		LogLevel: "info",
		Listen: ListenConfig{
			Address: "0.0.0.0",
			Port:    7443,
			TLS:     TLSConfig{Enabled: true},
		},
		Runtime: RuntimeConfig{
			Default: "docker",
			Docker: DockerConfig{
				Host:       "unix:///var/run/docker.sock",
				PullImages: true,
				Network:    "none",
			},
		},
		Auth: AuthConfig{
			SessionTTL:     Duration(12 * time.Hour),
			APIKeyTTL:      Duration(30 * 24 * time.Hour),
			BootstrapAdmin: "admin",
		},
		Limits: LimitsConfig{
			HostReserve:         Reserve{CPU: 1, MemoryMB: 1024},
			MaxConcurrentJobs:   4,
			MaxInstancesPerUser: 5,
			MaxInstances:        20,
			DefaultTTL:          Duration(time.Hour),
			MaxTTL:              Duration(4 * time.Hour),
		},
		Artifacts: ArtifactsConfig{
			MaxUploadBytes: 256 << 20,
			MaxSizeBytes:   1 << 30,
			Retention:      Duration(7 * 24 * time.Hour),
		},
		Discovery: DiscoveryConfig{
			Enabled:      true,
			InstanceName: "double-agent",
		},
	}
}

// Load reads the configuration from path. An empty path returns the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.apply()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	// Environment references are expanded before parsing so that the file
	// stays free of credentials.
	raw = ExpandEnv(raw, os.LookupEnv)
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.path = path
	return cfg, cfg.apply()
}

// Path returns the file the configuration was loaded from.
func (c Config) Path() string { return c.path }

// apply fills in derived defaults and validates the result.
func (c *Config) apply() error {
	// The environment is applied last so that an exported variable always
	// wins over the value written in the file.
	c.applyEnvOverrides()
	if c.DataDir == "" {
		c.DataDir = "/var/lib/double-agent"
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = 7443
	}
	if c.Listen.Address == "" {
		c.Listen.Address = "0.0.0.0"
	}
	if c.Runtime.Default == "" {
		c.Runtime.Default = "docker"
	}
	if c.Runtime.Docker.Host == "" {
		c.Runtime.Docker.Host = "unix:///var/run/docker.sock"
	}
	if c.Runtime.Docker.Network == "" {
		c.Runtime.Docker.Network = "none"
	}
	if c.Auth.SessionTTL.Duration() <= 0 {
		c.Auth.SessionTTL = Duration(12 * time.Hour)
	}
	if c.Auth.APIKeyTTL.Duration() <= 0 {
		c.Auth.APIKeyTTL = Duration(30 * 24 * time.Hour)
	}
	if c.Limits.MaxConcurrentJobs <= 0 {
		c.Limits.MaxConcurrentJobs = 4
	}
	if c.Limits.DefaultTTL.Duration() <= 0 {
		c.Limits.DefaultTTL = Duration(time.Hour)
	}
	if c.Limits.MaxTTL.Duration() <= 0 {
		c.Limits.MaxTTL = Duration(4 * time.Hour)
	}
	if c.Limits.MaxTTL.Duration() < c.Limits.DefaultTTL.Duration() {
		c.Limits.MaxTTL = c.Limits.DefaultTTL
	}
	if c.Artifacts.MaxUploadBytes <= 0 {
		c.Artifacts.MaxUploadBytes = 256 << 20
	}
	if c.Artifacts.MaxSizeBytes <= 0 {
		c.Artifacts.MaxSizeBytes = 1 << 30
	}
	if c.Artifacts.Retention.Duration() <= 0 {
		c.Artifacts.Retention = Duration(7 * 24 * time.Hour)
	}
	if c.Artifacts.Root == "" {
		c.Artifacts.Root = filepath.Join(c.DataDir, "artifacts")
	}
	if c.Runtime.Docker.WorkspaceRoot == "" {
		c.Runtime.Docker.WorkspaceRoot = filepath.Join(c.DataDir, "instances")
	}
	if c.Runtime.Local.WorkspaceRoot == "" {
		c.Runtime.Local.WorkspaceRoot = filepath.Join(c.DataDir, "instances")
	}
	if c.Runtime.Proxmox.WorkspaceRoot == "" {
		c.Runtime.Proxmox.WorkspaceRoot = filepath.Join(c.DataDir, "instances")
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	return c.Validate()
}

// Validate reports configuration values that cannot work.
func (c Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: invalid log_level %q", c.LogLevel)
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("config: invalid listen port %d", c.Listen.Port)
	}
	if c.Auth.RequireTLS && !c.Listen.TLS.Enabled {
		return fmt.Errorf("config: auth.require_tls needs listen.tls.enabled")
	}
	if c.Runtime.Local.Enabled {
		return nil
	}
	return nil
}

// StatePath returns a path inside the persistent state directory.
func (c Config) StatePath(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir, "state"}, parts...)...)
}

// CachePath returns a path inside the rebuildable cache directory.
func (c Config) CachePath(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir, "cache"}, parts...)...)
}

// DatabasePath returns the path of the SQLite metadata database.
func (c Config) DatabasePath() string {
	return c.StatePath("double-agent.db")
}

// PKIPath returns the path of the local certificate authority material.
func (c Config) PKIPath() string {
	return filepath.Join(c.DataDir, "pki")
}

// LogPath returns the directory used for daemon logs.
func (c Config) LogPath() string {
	return filepath.Join(c.DataDir, "logs")
}
