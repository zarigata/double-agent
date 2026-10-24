package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/store"
)

// Session parameters.
const (
	// SessionCookieName is the name of the cookie that carries the session
	// token. The name is prefixed so that it never collides with a cookie of
	// another application served from the same host.
	SessionCookieName = "da_session"

	// CSRFHeaderName is the header that carries the CSRF token of a
	// cookie-authenticated request.
	CSRFHeaderName = "X-CSRF-Token"

	// sessionTokenBytes is the length of the random session token. Thirty two
	// bytes puts the token far beyond the reach of an offline search.
	sessionTokenBytes = 32

	// csrfTokenBytes is the length of the random CSRF token.
	csrfTokenBytes = 16

	// defaultTouchInterval is the shortest interval between two LastSeen
	// writes. Touching on every request would turn the metadata database
	// into a hot write path for no benefit.
	defaultTouchInterval = time.Minute
)

// Errors returned by the session manager.
var (
	// ErrSessionInvalid is returned when a cookie value does not match a
	// live session.
	ErrSessionInvalid = errors.New("auth: the session is not valid")
	// ErrSessionExpired is returned when the session is past its deadline.
	ErrSessionExpired = errors.New("auth: the session has expired")
	// ErrUserDisabled is returned when the account behind a session was
	// disabled by an administrator.
	ErrUserDisabled = errors.New("auth: the account is disabled")
)

// SessionManager issues and validates browser sessions.
//
// Only the hash of a session token is persisted, so a dump of the metadata
// database cannot be replayed as a live session. The clear-text token only
// ever exists in the cookie of the browser that owns it.
type SessionManager struct {
	store *store.Store
	ttl   time.Duration
	now   func() time.Time

	// secure marks the session cookie as Secure. It is false only when the
	// daemon is explicitly configured for plaintext HTTP.
	secure bool

	// touchInterval is the shortest interval between two LastSeen writes.
	touchInterval time.Duration
}

// SessionOptions configures a SessionManager.
type SessionOptions struct {
	// Store persists the sessions.
	Store *store.Store
	// TTL is the lifetime of a new session.
	TTL time.Duration
	// Secure marks the session cookie as Secure.
	Secure bool
	// Now returns the current time. Tests replace it to get deterministic
	// deadlines.
	Now func() time.Time
}

// NewSessionManager creates a session manager.
func NewSessionManager(opts SessionOptions) (*SessionManager, error) {
	if opts.Store == nil {
		return nil, errors.New("auth: a store is required")
	}
	if opts.TTL <= 0 {
		opts.TTL = 12 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &SessionManager{
		store:         opts.Store,
		ttl:           opts.TTL,
		now:           opts.Now,
		secure:        opts.Secure,
		touchInterval: defaultTouchInterval,
	}, nil
}

// TTL returns the lifetime of a new session.
func (m *SessionManager) TTL() time.Duration { return m.ttl }

// Secure reports whether the session cookie is marked as Secure.
func (m *SessionManager) Secure() bool { return m.secure }

// Create issues a session for a user.
//
// The returned clear-text token is the cookie value; it is never written to
// the store.
func (m *SessionManager) Create(ctx context.Context, userID string) (store.Session, string, error) {
	if userID == "" {
		return store.Session{}, "", errors.New("auth: a user is required")
	}
	token, err := randomToken(sessionTokenBytes)
	if err != nil {
		return store.Session{}, "", err
	}
	csrf, err := randomHex(csrfTokenBytes)
	if err != nil {
		return store.Session{}, "", err
	}
	now := m.now()
	session := store.Session{
		ID:        ids.New(ids.PrefixSession),
		UserID:    userID,
		TokenHash: HashSecret(token),
		CSRFToken: csrf,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
		LastSeen:  now,
	}
	created, err := m.store.CreateSession(ctx, session)
	if err != nil {
		return store.Session{}, "", fmt.Errorf("auth: create session: %w", err)
	}
	return created, token, nil
}

// ValidateSession resolves a cookie value into the session and its owner.
//
// An expired session is removed on the spot so that a copied cookie cannot
// be replayed after its deadline. A disabled account invalidates the session
// immediately, even before it expires.
func (m *SessionManager) ValidateSession(ctx context.Context, cookieValue string) (store.User, store.Session, error) {
	if cookieValue == "" {
		return store.User{}, store.Session{}, fmt.Errorf("%w: the cookie is empty", ErrSessionInvalid)
	}
	session, err := m.store.SessionByTokenHash(ctx, HashSecret(cookieValue))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.User{}, store.Session{}, fmt.Errorf("%w: the session does not exist", ErrSessionInvalid)
		}
		return store.User{}, store.Session{}, fmt.Errorf("auth: load session: %w", err)
	}
	now := m.now()
	if session.Expired(now) {
		if err := m.store.DeleteSession(ctx, session.ID); err != nil {
			return store.User{}, session, fmt.Errorf("%w: the session expired at %s",
				ErrSessionExpired, session.ExpiresAt.UTC().Format(time.RFC3339))
		}
		return store.User{}, session, fmt.Errorf("%w: the session expired at %s",
			ErrSessionExpired, session.ExpiresAt.UTC().Format(time.RFC3339))
	}
	user, err := m.store.UserByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.User{}, session, fmt.Errorf("%w: the account no longer exists", ErrSessionInvalid)
		}
		return store.User{}, session, fmt.Errorf("auth: load user: %w", err)
	}
	if user.Disabled {
		return user, session, fmt.Errorf("%w: the account %q is disabled", ErrUserDisabled, user.Username)
	}
	m.touch(ctx, session, now)
	return user, session, nil
}

// Delete removes a session by identifier.
func (m *SessionManager) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("auth: a session is required")
	}
	if err := m.store.DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// DeleteByToken removes the session that a cookie value points at.
func (m *SessionManager) DeleteByToken(ctx context.Context, cookieValue string) error {
	if cookieValue == "" {
		return fmt.Errorf("%w: the cookie is empty", ErrSessionInvalid)
	}
	session, err := m.store.SessionByTokenHash(ctx, HashSecret(cookieValue))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: the session does not exist", ErrSessionInvalid)
		}
		return fmt.Errorf("auth: load session: %w", err)
	}
	return m.Delete(ctx, session.ID)
}

// CleanupExpired removes the sessions that are past their deadline and
// returns how many were removed.
func (m *SessionManager) CleanupExpired(ctx context.Context) (int64, error) {
	removed, err := m.store.DeleteExpiredSessions(ctx, m.now())
	if err != nil {
		return 0, fmt.Errorf("auth: delete expired sessions: %w", err)
	}
	return removed, nil
}

// VerifyCSRF compares a submitted CSRF token with the token of a session.
//
// The comparison is constant time so that a client cannot learn the token
// from the time the comparison takes.
func (m *SessionManager) VerifyCSRF(session store.Session, submitted string) bool {
	if session.CSRFToken == "" || submitted == "" {
		return false
	}
	return ConstantTimeEqual(submitted, session.CSRFToken)
}

// touch records that the session was just used.
//
// A failure is ignored on purpose: LastSeen is advisory bookkeeping and the
// session stays valid until its deadline.
func (m *SessionManager) touch(ctx context.Context, session store.Session, now time.Time) {
	if m.touchInterval > 0 && now.Sub(session.LastSeen) < m.touchInterval {
		return
	}
	_ = m.store.TouchSession(ctx, session.ID)
}

// CookieValue returns the session cookie of a request, or an empty string
// when the request carries no session.
func CookieValue(r *http.Request) string {
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie == nil {
		return ""
	}
	return cookie.Value
}

// SetSessionCookie writes the session cookie.
//
// The cookie is HttpOnly so that a script cannot read it, SameSite=Lax so
// that another site cannot trigger a state change with it, and Secure
// whenever the daemon speaks HTTPS.
func SetSessionCookie(w http.ResponseWriter, value string, expires time.Time, secure bool) {
	maxAge := int(expires.Sub(time.Now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie of the browser.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// randomToken returns a random token rendered in base64url.
func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// randomHex returns a random token rendered in hexadecimal.
func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
