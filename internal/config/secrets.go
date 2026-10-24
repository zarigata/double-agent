package config

import (
	"os"
	"strings"
)

// Environment variables that override sensitive configuration values.
//
// Secrets must never be written to a configuration file: the file is often
// copied, committed or shipped to a support bundle. The environment keeps the
// credential out of that surface and lets an operator rotate it without
// touching the configuration.
const (
	// EnvProxmoxPassword overrides the Proxmox VE password.
	EnvProxmoxPassword = "DOUBLE_AGENT_PROXMOX_PASSWORD"
	// EnvProxmoxTokenSecret overrides the Proxmox VE API token secret.
	EnvProxmoxTokenSecret = "DOUBLE_AGENT_PROXMOX_TOKEN_SECRET"
	// EnvDockerHost overrides the Docker daemon endpoint.
	EnvDockerHost = "DOUBLE_AGENT_DOCKER_HOST"
)

// applyEnvOverrides replaces the credential fields with the values found in
// the environment.
//
// The environment always wins: an operator who exports a variable expects it
// to be honoured, and silently ignoring it would be a security footgun.
func (c *Config) applyEnvOverrides() {
	if v, ok := os.LookupEnv(EnvProxmoxPassword); ok {
		c.Runtime.Proxmox.Password = v
	}
	if v, ok := os.LookupEnv(EnvProxmoxTokenSecret); ok {
		c.Runtime.Proxmox.TokenSecret = v
	}
	if v, ok := os.LookupEnv(EnvDockerHost); ok {
		c.Runtime.Docker.Host = v
	}
}

// Redacted returns a copy of the configuration with every secret replaced by
// a placeholder.
//
// It is used by the doctor command and by the diagnostics endpoint so that a
// configuration can be shown to a human or logged without leaking a
// credential.
func (c Config) Redacted() Config {
	out := c
	if out.Runtime.Proxmox.Password != "" {
		out.Runtime.Proxmox.Password = redacted
	}
	if out.Runtime.Proxmox.TokenSecret != "" {
		out.Runtime.Proxmox.TokenSecret = redacted
	}
	return out
}

// redacted is the placeholder shown in place of a secret.
const redacted = "***redacted***"

// ExpandEnv expands ${VAR} references in the configuration values.
//
// It is applied to the whole document before parsing, which lets an operator
// keep the structure of the configuration file while sourcing the values from
// the environment or from a secrets directory.
func ExpandEnv(raw []byte, lookup func(string) (string, bool)) []byte {
	text := string(raw)
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '$' && i+1 < len(text) && text[i+1] == '{' {
			end := strings.IndexByte(text[i+2:], '}')
			if end < 0 {
				b.WriteByte(text[i])
				i++
				continue
			}
			name := text[i+2 : i+2+end]
			if value, ok := lookup(name); ok {
				b.WriteString(value)
			} else {
				// An unresolved reference is left in place so that the
				// configuration parser reports it as a missing value
				// instead of silently substituting an empty string.
				b.WriteString(text[i : i+2+end+1])
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return []byte(b.String())
}
