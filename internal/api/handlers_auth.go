package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// userView is the public shape of an account. The password hash is never
// part of it.
type userView struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
}

// loginRequest is the body of the login endpoint.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse describes an authenticated session.
type sessionResponse struct {
	User      userView `json:"user"`
	ActorType string   `json:"actor_type"`
	Scopes    []string `json:"scopes"`
	// CSRFToken is the token a browser has to send with every state
	// changing request. It is only present for a session.
	CSRFToken string `json:"csrf_token,omitempty"`
	// ExpiresAt is the deadline of the session.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// handleLogin exchanges a password for a session cookie.
//
// The answer is the same for an unknown account, a wrong password and a
// disabled account, so that the endpoint cannot be used to find out which
// accounts exist.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" || request.Password == "" {
		writeError(w, r, CodeInvalidRequest, "the username and the password are required", nil)
		return
	}
	ctx := r.Context()
	user, err := s.store.UserByUsername(ctx, request.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.audit(r, policy.Actor{Type: store.ActorUser, Username: request.Username},
				"auth.login", store.DecisionDeny, "the account does not exist")
			writeError(w, r, CodeAuthenticationFailed, "the credentials are not valid", nil)
			return
		}
		writeError(w, r, CodeInternal, "the credentials could not be verified", nil)
		return
	}
	actor := s.actorForUser(user)
	if user.Disabled {
		s.audit(r, actor, "auth.login", store.DecisionDeny, "the account is disabled")
		writeError(w, r, CodeAuthenticationFailed, "the credentials are not valid", nil)
		return
	}
	if err := auth.VerifyPassword(request.Password, user.PasswordHash); err != nil {
		s.audit(r, actor, "auth.login", store.DecisionDeny, "the password does not match")
		writeError(w, r, CodeAuthenticationFailed, "the credentials are not valid", nil)
		return
	}
	session, token, err := s.sessions.Create(ctx, user.ID)
	if err != nil {
		writeError(w, r, CodeInternal, "the session could not be created", nil)
		return
	}
	auth.SetSessionCookie(w, token, session.ExpiresAt, s.sessions.Secure())
	s.audit(r, actor, "auth.login", store.DecisionAllow, "")
	writeJSON(w, r, http.StatusOK, sessionResponse{
		User:      viewUser(user),
		ActorType: actor.Type,
		Scopes:    scopeNames(actor),
		CSRFToken: session.CSRFToken,
		ExpiresAt: &session.ExpiresAt,
	})
}

// handleLogout closes the session of the caller and clears its cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	state := authStateFrom(r)
	if !state.hasSession {
		writeError(w, r, CodeInvalidRequest,
			"the request does not carry a browser session", nil)
		return
	}
	if err := s.sessions.Delete(r.Context(), state.session.ID); err != nil {
		writeError(w, r, CodeInternal, "the session could not be closed", nil)
		return
	}
	auth.ClearSessionCookie(w, s.sessions.Secure())
	s.audit(r, state.actor, "auth.logout", store.DecisionAllow, "")
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

// handleMe describes the caller of the request.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	state := authStateFrom(r)
	actor := state.actor
	response := sessionResponse{
		ActorType: actor.Type,
		Scopes:    scopeNames(actor),
	}
	user, err := s.store.UserByID(r.Context(), actor.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeAuthenticationFailed, "the account no longer exists", nil)
			return
		}
		writeError(w, r, CodeInternal, "the account could not be loaded", nil)
		return
	}
	response.User = viewUser(user)
	if state.hasSession {
		response.CSRFToken = state.session.CSRFToken
		expires := state.session.ExpiresAt
		response.ExpiresAt = &expires
	}
	writeJSON(w, r, http.StatusOK, response)
}

// createUserRequest is the body of the user creation endpoint.
type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleUserList answers with every account of the node.
func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, r, CodeInternal, "the accounts could not be loaded", nil)
		return
	}
	views := make([]userView, 0, len(users))
	for _, user := range users {
		views = append(views, viewUser(user))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"users": views})
}

// handleUserCreate adds an account to the node.
func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var request createUserRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" {
		writeError(w, r, CodeInvalidRequest, "the username is required", nil)
		return
	}
	if len(request.Username) > 64 {
		writeError(w, r, CodeInvalidRequest, "the username is too long", nil)
		return
	}
	role := store.Role(strings.TrimSpace(request.Role))
	if role == "" {
		role = store.RoleUser
	}
	if role != store.RoleAdmin && role != store.RoleUser {
		writeError(w, r, CodeInvalidRequest, "the role is not known",
			map[string]any{"role": string(role)})
		return
	}
	hash, err := auth.HashPassword(request.Password)
	if err != nil {
		writeDecodeError(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.store.UserByUsername(ctx, request.Username); err == nil {
		writeError(w, r, CodeConflict, "the username is already taken",
			map[string]any{"username": request.Username})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, r, CodeInternal, "the account could not be created", nil)
		return
	}
	user, err := s.store.CreateUser(ctx, store.User{
		ID:           ids.New(ids.PrefixUser),
		Username:     request.Username,
		PasswordHash: hash,
		Role:         role,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, CodeConflict, "the username is already taken", nil)
			return
		}
		writeError(w, r, CodeInternal, "the account could not be created", nil)
		return
	}
	s.audit(r, actorFrom(r), "user.create", store.DecisionAllow, user.ID)
	writeJSON(w, r, http.StatusCreated, map[string]any{"user": viewUser(user)})
}

// handleUserDisable disables an account.
//
// The sessions of a disabled account stop working at once: the session
// manager refuses a session of a disabled user.
func (s *Server) handleUserDisable(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	ctx := r.Context()
	if err := s.store.SetUserDisabled(ctx, id, true); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the account does not exist",
				map[string]any{"user_id": id})
			return
		}
		writeError(w, r, CodeInternal, "the account could not be disabled", nil)
		return
	}
	user, err := s.store.UserByID(ctx, id)
	if err != nil {
		writeError(w, r, CodeInternal, "the account could not be loaded", nil)
		return
	}
	s.audit(r, actorFrom(r), "user.disable", store.DecisionAllow, user.ID)
	writeJSON(w, r, http.StatusOK, map[string]any{"user": viewUser(user)})
}

// keyView is the public shape of a client credential. The secret is never
// part of it.
type keyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	UserID     string     `json:"user_id"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// Secret is only filled in by the creation endpoint. The secret is
	// shown to the operator exactly once.
	Secret string `json:"secret,omitempty"`
}

// createKeyRequest is the body of the key creation endpoint.
type createKeyRequest struct {
	Name             string   `json:"name"`
	UserID           string   `json:"user_id"`
	Scopes           []string `json:"scopes"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
}

// handleKeyList answers with the client credentials of the node.
func (s *Server) handleKeyList(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context(), "")
	if err != nil {
		writeError(w, r, CodeInternal, "the credentials could not be loaded", nil)
		return
	}
	views := make([]keyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, viewKey(key))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"keys": views})
}

// handleKeyCreate issues a client credential.
//
// The secret is returned exactly once: only its hash is stored, so the
// operator has to keep it.
func (s *Server) handleKeyCreate(w http.ResponseWriter, r *http.Request) {
	var request createKeyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	ctx := r.Context()
	actor := actorFrom(r)
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "api key"
	}
	userID := strings.TrimSpace(request.UserID)
	if userID == "" {
		userID = actor.ID
	}
	if _, err := s.store.UserByID(ctx, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the account does not exist",
				map[string]any{"user_id": userID})
			return
		}
		writeError(w, r, CodeInternal, "the account could not be loaded", nil)
		return
	}
	scopes := request.Scopes
	if len(scopes) == 0 {
		scopes = scopeStrings(policy.DefaultUserScopes)
	}
	parsed, err := policy.ParseScopes(scopes)
	if err != nil {
		writeError(w, r, CodeInvalidRequest, "the scopes are not known", nil)
		return
	}
	ttl := s.cfg.Auth.APIKeyTTL.Duration()
	if request.ExpiresInSeconds > 0 {
		ttl = time.Duration(request.ExpiresInSeconds) * time.Second
	}
	var expiresAt *time.Time
	if ttl > 0 {
		deadline := s.now().Add(ttl)
		expiresAt = &deadline
	}
	secret, _, prefix, hash, err := auth.IssueKey()
	if err != nil {
		writeError(w, r, CodeInternal, "the credential could not be issued", nil)
		return
	}
	key, err := s.store.CreateAPIKey(ctx, store.APIKey{
		ID:        ids.New(ids.PrefixAPIKey),
		UserID:    userID,
		Name:      name,
		Prefix:    prefix,
		KeyHash:   hash,
		Scopes:    scopeStrings(parsed),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, r, CodeInternal, "the credential could not be stored", nil)
		return
	}
	s.audit(r, actor, "key.create", store.DecisionAllow, key.ID)
	response := viewKey(key)
	response.Secret = secret
	writeJSON(w, r, http.StatusCreated, map[string]any{"key": response})
}

// handleKeyRevoke revokes a client credential.
//
// Revocation is immediate: the key stops working on the next request.
func (s *Server) handleKeyRevoke(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	ctx := r.Context()
	if err := s.store.RevokeAPIKey(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the credential does not exist",
				map[string]any{"key_id": id})
			return
		}
		writeError(w, r, CodeInternal, "the credential could not be revoked", nil)
		return
	}
	s.audit(r, actorFrom(r), "key.revoke", store.DecisionAllow, id)
	writeJSON(w, r, http.StatusOK, map[string]any{"id": id, "revoked": true})
}

// handleAuditList answers with the audit records of the node.
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.AuditFilter{
		ActorID: strings.TrimSpace(query.Get("actor_id")),
		Action:  strings.TrimSpace(query.Get("action")),
		Limit:   limitQuery(r),
	}
	if since := strings.TrimSpace(query.Get("since")); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			writeError(w, r, CodeInvalidRequest, "the since parameter is not a timestamp", nil)
			return
		}
		filter.Since = &parsed
	}
	events, err := s.store.ListAuditEvents(r.Context(), filter)
	if err != nil {
		writeError(w, r, CodeInternal, "the audit records could not be loaded", nil)
		return
	}
	views := make([]map[string]any, 0, len(events))
	for _, event := range events {
		views = append(views, map[string]any{
			"id":             event.ID,
			"timestamp":      event.Timestamp,
			"actor_type":     event.ActorType,
			"actor_id":       event.ActorID,
			"action":         event.Action,
			"target_type":    event.TargetType,
			"target_id":      event.TargetID,
			"decision":       event.Decision,
			"correlation_id": event.CorrelationID,
			"result":         event.Result,
			"metadata":       event.Metadata,
		})
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"events": views})
}

// viewUser renders an account for a client.
func viewUser(user store.User) userView {
	return userView{
		ID:        user.ID,
		Username:  user.Username,
		Role:      string(user.Role),
		Disabled:  user.Disabled,
		CreatedAt: user.CreatedAt,
	}
}

// viewKey renders a client credential for a client.
func viewKey(key store.APIKey) keyView {
	return keyView{
		ID:         key.ID,
		Name:       key.Name,
		Prefix:     key.Prefix,
		UserID:     key.UserID,
		Scopes:     key.Scopes,
		ExpiresAt:  key.ExpiresAt,
		LastUsedAt: key.LastUsedAt,
		RevokedAt:  key.RevokedAt,
		CreatedAt:  key.CreatedAt,
	}
}

// scopeNames renders the scopes of an actor as a list of strings.
func scopeNames(actor policy.Actor) []string {
	scopes := actor.Scopes.List()
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}
	return names
}

// scopeStrings renders a list of scopes as a list of strings.
func scopeStrings(scopes []policy.Scope) []string {
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}
	return names
}
