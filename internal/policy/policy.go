// Package policy implements the authorization rules of the Double Agent
// control plane.
//
// Every request is evaluated against a scope set, the ownership of the target
// resource and the resource quotas configured on the node. The decision is
// recorded in the audit log.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Scope is a permission granted to an actor.
type Scope string

// The scopes documented in docs/MCP_API_AND_DISCOVERY.md.
const (
	ScopeCapabilitiesRead Scope = "capabilities:read"
	ScopeTemplatesRead    Scope = "templates:read"
	ScopeDocsRead         Scope = "docs:read"
	ScopeInstancesCreate  Scope = "instances:create"
	ScopeInstancesRead    Scope = "instances:read"
	ScopeInstancesExec    Scope = "instances:exec"
	ScopeInstancesStop    Scope = "instances:stop"
	ScopeInstancesDestroy Scope = "instances:destroy"
	ScopeArtifactsRead    Scope = "artifacts:read"
	ScopeArtifactsWrite   Scope = "artifacts:write"
	ScopeJobsRead         Scope = "jobs:read"
	ScopeJobsCancel       Scope = "jobs:cancel"
	ScopeDelegateRun      Scope = "delegate:run"
	ScopeUsersManage      Scope = "users:manage"
	ScopeKeysManage       Scope = "keys:manage"
	ScopeAdmin            Scope = "admin"
)

// AllScopes lists every scope the control plane understands.
var AllScopes = []Scope{
	ScopeCapabilitiesRead, ScopeTemplatesRead, ScopeDocsRead,
	ScopeInstancesCreate, ScopeInstancesRead, ScopeInstancesExec,
	ScopeInstancesStop, ScopeInstancesDestroy,
	ScopeArtifactsRead, ScopeArtifactsWrite,
	ScopeJobsRead, ScopeJobsCancel, ScopeDelegateRun,
	ScopeUsersManage, ScopeKeysManage, ScopeAdmin,
}

// DefaultUserScopes are the scopes granted to a regular user key.
var DefaultUserScopes = []Scope{
	ScopeCapabilitiesRead, ScopeTemplatesRead, ScopeDocsRead,
	ScopeInstancesCreate, ScopeInstancesRead, ScopeInstancesExec,
	ScopeInstancesStop, ScopeInstancesDestroy,
	ScopeArtifactsRead, ScopeArtifactsWrite,
	ScopeJobsRead, ScopeJobsCancel,
}

// AdminScopes are the scopes granted to an administrator key.
var AdminScopes = append(append([]Scope{}, DefaultUserScopes...), ScopeDelegateRun, ScopeUsersManage, ScopeKeysManage, ScopeAdmin)

// Errors returned by the package.
var (
	// ErrDenied is returned when the actor lacks a required scope.
	ErrDenied = errors.New("policy: permission denied")
	// ErrUnknownScope is returned when a requested scope is not recognised.
	ErrUnknownScope = errors.New("policy: unknown scope")
)

// ParseScopes validates and normalises a list of scope strings.
func ParseScopes(raw []string) ([]Scope, error) {
	out := make([]Scope, 0, len(raw))
	seen := map[Scope]bool{}
	for _, raw := range raw {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		scope := Scope(trimmed)
		if !knownScope(scope) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownScope, trimmed)
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// knownScope reports whether the scope is part of the vocabulary.
func knownScope(s Scope) bool {
	for _, known := range AllScopes {
		if known == s {
			return true
		}
	}
	return false
}

// ScopeSet is an immutable set of granted scopes.
type ScopeSet map[Scope]bool

// NewScopeSet builds a scope set from a list of scopes.
func NewScopeSet(scopes []Scope) ScopeSet {
	set := ScopeSet{}
	for _, s := range scopes {
		set[s] = true
	}
	return set
}

// List returns the granted scopes in a stable order.
func (s ScopeSet) List() []Scope {
	out := make([]Scope, 0, len(s))
	for scope := range s {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Has reports whether the scope is granted.
func (s ScopeSet) Has(scope Scope) bool { return s[scope] }

// IsAdmin reports whether the actor holds the administrative scope.
func (s ScopeSet) IsAdmin() bool { return s[ScopeAdmin] }

// Allows reports whether the actor holds the scope. Administrators hold every
// scope.
func (s ScopeSet) Allows(scope Scope) bool {
	return s[scope] || s[ScopeAdmin]
}

// Actor is the subject of an authorization decision.
type Actor struct {
	// Type is "user", "api_key" or "system".
	Type string
	// ID identifies the actor.
	ID string
	// Username is the human readable name, when known.
	Username string
	// Scopes are the granted permissions.
	Scopes ScopeSet
	// IsAdmin marks an administrator session. Administrators are not
	// automatically granted MCP scopes: an MCP key never inherits the power
	// of a browser session.
	IsAdmin bool
}

// Decision is the outcome of an authorization check.
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// Reason explains a denial in operator vocabulary.
	Reason string
	// Scope is the scope that was evaluated.
	Scope Scope
}

// Evaluate checks that the actor holds the required scope.
func Evaluate(actor Actor, scope Scope) Decision {
	if actor.Scopes.Allows(scope) {
		return Decision{Allowed: true, Scope: scope}
	}
	return Decision{
		Allowed: false,
		Scope:   scope,
		Reason:  fmt.Sprintf("missing scope %q", scope),
	}
}

// Owns reports whether the actor owns the resource identified by ownerID.
// Administrators may act on any resource.
func Owns(actor Actor, ownerID string) bool {
	if actor.IsAdmin && actor.Scopes.Has(ScopeAdmin) {
		return true
	}
	return actor.ID != "" && actor.ID == ownerID
}
