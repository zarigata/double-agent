package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// contextKey is the type of the context keys of this package. A private type
// keeps the keys from colliding with the keys of another package.
type contextKey int

const (
	// requestIDKey holds the correlation identifier of a request.
	requestIDKey contextKey = iota
	// authKey holds the authentication state of a request.
	authKey
)

// authState is the authentication result of a request.
type authState struct {
	// actor is the authenticated caller. It is empty for an anonymous
	// request.
	actor policy.Actor
	// session is the browser session of the request, when there is one.
	session store.Session
	// hasSession reports whether the request authenticated with a session
	// cookie. A request that carries a bearer token is never cookie
	// authenticated and is therefore exempt from the CSRF check.
	hasSession bool
	// err records why the presented credentials were refused.
	err *Error
}

// requestIDMiddleware attaches a correlation identifier to every request.
//
// The identifier is returned in the X-Request-Id header and travels with the
// error envelope and the audit records, so an operator can follow one
// request through the whole control plane.
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if !validRequestID(id) {
			id = ids.New(ids.PrefixRequest)
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validRequestID reports whether a client supplied identifier may be reused.
func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// loggingMiddleware records one structured line per request.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.written,
			"duration_ms", s.now().Sub(start).Milliseconds(),
			"request_id", requestID(r),
			"actor", actorFrom(r).ID,
		)
	})
}

// recoveryMiddleware turns a panic into an ordinary error answer.
//
// The stack trace stays in the log: a client never learns where the daemon
// runs or how it is built.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			s.log.Error("panic while serving a request",
				"panic", fmt.Sprint(recovered),
				"stack", string(debug.Stack()),
				"request_id", requestID(r))
			if recorder.wroteHeader {
				return
			}
			writeError(recorder, r, CodeInternal, "the request could not be completed", nil)
		}()
		next.ServeHTTP(recorder, r)
	})
}

// securityHeadersMiddleware sets the headers that protect the browser.
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		// The Web UI is served from the same origin as the API, so the
		// policy can forbid every foreign source.
		header.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware implements the cross-origin policy of the node.
//
// Only the origins listed in the configuration may call the API from a
// browser. An empty list therefore refuses every cross-origin request, while
// a same-origin request is always allowed: the browser already applies the
// same-origin policy to it.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && !sameOrigin(r, origin) {
			if !s.originAllowed(origin) {
				s.log.Warn("refused a cross-origin request",
					"origin", origin, "request_id", requestID(r))
				writeError(w, r, CodePermissionDenied, "the request origin is not allowed",
					map[string]any{"origin": origin})
				return
			}
			header := w.Header()
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Allow-Credentials", "true")
			header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token, X-Request-Id")
			header.Set("Access-Control-Max-Age", "600")
			header.Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether an Origin header points at the host that served
// the request.
func sameOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// originAllowed reports whether an origin is on the allow list.
func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.cfg.Listen.AllowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

// authMiddleware resolves the credentials of a request.
//
// A request that presents no credential stays anonymous; the route guard
// decides whether that is acceptable. A request that presents a broken
// credential is refused, because a client that believes it is authenticated
// must not silently act as an anonymous visitor.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.identify(r, authStateFrom(r))
		next.ServeHTTP(w, r)
	})
}

// identify resolves the actor of a request and records it in the state.
//
// The state is shared with the request log, which runs before the
// authentication and can therefore name the actor of a request.
//
// A bearer token wins over a session cookie: an explicit credential is
// never mixed with the session of a browser.
func (s *Server) identify(r *http.Request, state *authState) {
	ctx := r.Context()
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		actor, err := s.actorForAPIKey(ctx, header)
		if err != nil {
			state.err = err
			return
		}
		state.actor = actor
		return
	}
	value := auth.CookieValue(r)
	if value == "" {
		return
	}
	user, session, err := s.sessions.ValidateSession(ctx, value)
	if err != nil {
		state.err = errorf(CodeAuthenticationFailed, "the session is not valid", err)
		return
	}
	state.actor = s.actorForUser(user)
	state.session = session
	state.hasSession = true
}

// actorForUser builds the actor of a browser session.
//
// A session carries the scopes of the role of the account. The administrator
// role is the only source of administrative power in the Web UI.
func (s *Server) actorForUser(user store.User) policy.Actor {
	scopes := policy.DefaultUserScopes
	isAdmin := false
	if user.Role == store.RoleAdmin {
		scopes = policy.AdminScopes
		isAdmin = true
	}
	return policy.Actor{
		Type:     store.ActorUser,
		ID:       user.ID,
		Username: user.Username,
		Scopes:   policy.NewScopeSet(scopes),
		IsAdmin:  isAdmin,
	}
}

// actorForAPIKey builds the actor of a client credential.
//
// A key never inherits the scopes of its owner: it carries exactly the
// scopes it was issued with, so a client cannot pick up the power of a
// browser session.
func (s *Server) actorForAPIKey(ctx context.Context, header string) (policy.Actor, *Error) {
	secret, ok := bearerToken(header)
	if !ok {
		return policy.Actor{}, errorf(CodeAuthenticationFailed,
			"the authorization header does not carry a bearer token", nil)
	}
	key, err := s.store.APIKeyByHash(ctx, auth.HashSecret(secret))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return policy.Actor{}, errorf(CodeAuthenticationFailed, "the credential is not valid", err)
		}
		return policy.Actor{}, errorf(CodeInternal, "the credential could not be verified", err)
	}
	if !key.Active(s.now()) {
		return policy.Actor{}, errorf(CodeAuthenticationFailed, "the credential is not active", nil)
	}
	owner, err := s.store.UserByID(ctx, key.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return policy.Actor{}, errorf(CodeAuthenticationFailed,
				"the owner of the credential no longer exists", err)
		}
		return policy.Actor{}, errorf(CodeInternal, "the credential could not be verified", err)
	}
	if owner.Disabled {
		return policy.Actor{}, errorf(CodeAuthenticationFailed,
			"the owner of the credential is disabled", nil)
	}
	scopes, err := policy.ParseScopes(key.Scopes)
	if err != nil {
		return policy.Actor{}, errorf(CodeAuthenticationFailed,
			"the credential carries unknown scopes", err)
	}
	if err := s.store.TouchAPIKey(ctx, key.ID); err != nil {
		s.log.Warn("failed to record the use of an API key", "key", key.ID, "error", err)
	}
	set := policy.NewScopeSet(scopes)
	return policy.Actor{
		Type:     store.ActorAPIKey,
		ID:       key.UserID,
		Username: key.Name,
		Scopes:   set,
		IsAdmin:  set.Has(policy.ScopeAdmin),
	}, nil
}

// bearerToken extracts the secret of an Authorization header.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// csrfMiddleware protects the cookie authenticated state changes.
//
// A browser sends its session cookie with every request, including the ones
// a hostile page triggers. A request that changes state therefore has to
// prove that it knows the CSRF token of the session. A request that carries
// a bearer token is not cookie authenticated and is exempt.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := authStateFrom(r)
		if state.err == nil && state.hasSession && stateChanging(r.Method) && !isLoginPath(r.URL.Path) {
			submitted := r.Header.Get(auth.CSRFHeaderName)
			if !s.sessions.VerifyCSRF(state.session, submitted) {
				s.audit(r, state.actor, "csrf.check", store.DecisionDeny,
					"the CSRF token is missing or invalid")
				writeError(w, r, CodePermissionDenied,
					"the CSRF token is missing or invalid", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// stateChanging reports whether a method changes the state of the node.
func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// isLoginPath reports whether a path is the login endpoint. The login is the
// one state changing request that a browser may send without a session.
func isLoginPath(path string) bool {
	return path == "/api/v1/auth/login"
}

// guard enforces authentication and a scope on a route.
//
// A public route answers without an authenticated caller. A broken
// credential is ignored there, because the route does not need an identity
// and the client may still be a browser with a stale cookie.
func (s *Server) guard(scope policy.Scope, public bool, action string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := authStateFrom(r)
		if public {
			h(w, r)
			return
		}
		if state.err != nil {
			state.err.write(w, r)
			return
		}
		if state.actor.ID == "" {
			writeError(w, r, CodeAuthenticationFailed, "the request is not authenticated", nil)
			return
		}
		if scope != "" {
			decision := policy.Evaluate(state.actor, scope)
			if !decision.Allowed {
				s.audit(r, state.actor, action, store.DecisionDeny, decision.Reason)
				writeError(w, r, CodePermissionDenied,
					"the request is not allowed for this actor",
					map[string]any{"scope": string(scope)})
				return
			}
		}
		h(w, r)
	}
}

// statusRecorder remembers the status and the size of a response.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	written     int
	wroteHeader bool
}

// WriteHeader records the status of the response.
func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

// Write records the size of the response.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Flush forwards a flush to the wrapped writer when it supports one.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
