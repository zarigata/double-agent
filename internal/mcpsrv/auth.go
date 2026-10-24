package mcpsrv

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// administrativeScopes are the scopes that only the browser surface uses.
//
// An MCP client key never inherits the power of a browser session: the
// administrative scopes are removed from the key whatever the key was issued
// with, and the actor of an MCP call is never an administrator. The MCP
// surface exposes no tool that would need them, so dropping them costs
// nothing and keeps a key that was issued with the admin scope from becoming
// more powerful than its owner intended.
var administrativeScopes = []policy.Scope{
	policy.ScopeAdmin,
	policy.ScopeUsersManage,
	policy.ScopeKeysManage,
}

// authenticate resolves the actor of an MCP request.
//
// The credential is a client key presented as a bearer token. Only the hash of
// the secret is stored, so the secret itself never reaches the database. A
// key that is revoked, expired, owned by a disabled account or carrying an
// unknown scope is refused with the authentication_failed code; a key that is
// valid but carries no usable scope is refused with permission_denied.
func (s *Server) authenticate(r *http.Request) (policy.Actor, *ToolError) {
	correlationID := requestCorrelationID(r)
	secret, ok := bearerToken(r)
	if !ok {
		return policy.Actor{}, &ToolError{
			Code:          CodeAuthenticationFailed,
			Message:       "the request does not carry a bearer token",
			CorrelationID: correlationID,
		}
	}

	key, err := s.store.APIKeyByHash(r.Context(), auth.HashSecret(secret))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return policy.Actor{}, &ToolError{
				Code:          CodeAuthenticationFailed,
				Message:       "the credential is not valid",
				CorrelationID: correlationID,
			}
		}
		s.log.Error("failed to read the credential", "error", err, "correlation_id", correlationID)
		return policy.Actor{}, &ToolError{
			Code:          CodeInternal,
			Message:       "the credential could not be checked",
			CorrelationID: correlationID,
			Err:           err,
		}
	}

	now := time.Now().UTC()
	switch {
	case key.Revoked():
		return policy.Actor{}, &ToolError{
			Code:          CodeAuthenticationFailed,
			Message:       "the credential was revoked",
			CorrelationID: correlationID,
		}
	case key.Expired(now):
		return policy.Actor{}, &ToolError{
			Code:          CodeAuthenticationFailed,
			Message:       "the credential expired",
			CorrelationID: correlationID,
		}
	}

	owner, err := s.store.UserByID(r.Context(), key.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return policy.Actor{}, &ToolError{
				Code:          CodeAuthenticationFailed,
				Message:       "the credential is not valid",
				CorrelationID: correlationID,
			}
		}
		s.log.Error("failed to read the owner of the credential", "error", err, "correlation_id", correlationID)
		return policy.Actor{}, &ToolError{
			Code:          CodeInternal,
			Message:       "the credential could not be checked",
			CorrelationID: correlationID,
			Err:           err,
		}
	}
	if owner.Disabled {
		return policy.Actor{}, &ToolError{
			Code:          CodeAuthenticationFailed,
			Message:       "the account of the credential is disabled",
			CorrelationID: correlationID,
		}
	}

	scopes, err := policy.ParseScopes(key.Scopes)
	if err != nil {
		s.log.Warn("the credential carries an unknown scope", "key", key.ID, "error", err)
		return policy.Actor{}, &ToolError{
			Code:          CodeAuthenticationFailed,
			Message:       "the credential carries a scope that is not known",
			CorrelationID: correlationID,
			Err:           err,
		}
	}
	usable := dropAdministrativeScopes(scopes)
	if len(usable) == 0 {
		return policy.Actor{}, &ToolError{
			Code:          CodePermissionDenied,
			Message:       "the credential carries no usable scope",
			CorrelationID: correlationID,
		}
	}

	// The last use of the key is recorded so that an operator can spot a
	// credential that nobody uses anymore. A failure here must not refuse the
	// call: the credential itself was already accepted.
	if err := s.store.TouchAPIKey(r.Context(), key.ID); err != nil {
		s.log.Warn("failed to record the use of the credential", "key", key.ID, "error", err)
	}

	return policy.Actor{
		Type:     store.ActorAPIKey,
		ID:       owner.ID,
		Username: owner.Username,
		Scopes:   policy.NewScopeSet(usable),
		// An MCP key is never an administrator: the browser session of the
		// owner and the machine credentials of a client are different things.
		IsAdmin: false,
	}, nil
}

// dropAdministrativeScopes removes the administrative scopes from a key.
//
// The rule is unconditional: a key that was issued with the admin scope keeps
// the scopes it needs and loses the ones that would let it administer the
// node.
func dropAdministrativeScopes(scopes []policy.Scope) []policy.Scope {
	out := make([]policy.Scope, 0, len(scopes))
	for _, scope := range scopes {
		if isAdministrative(scope) {
			continue
		}
		out = append(out, scope)
	}
	return out
}

// isAdministrative reports whether a scope belongs to the administration of
// the node.
func isAdministrative(scope policy.Scope) bool {
	for _, administrative := range administrativeScopes {
		if scope == administrative {
			return true
		}
	}
	return false
}

// bearerToken extracts the bearer token of a request.
//
// The scheme is matched without regard to case, as the HTTP specification
// requires.
func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", false
	}
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
