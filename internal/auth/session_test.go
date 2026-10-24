package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/store"
)

// newManager creates a session manager on a temporary database.
func newManager(t *testing.T, now func() time.Time) (*auth.SessionManager, *store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "double-agent.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	manager, err := auth.NewSessionManager(auth.SessionOptions{
		Store:  st,
		TTL:    time.Hour,
		Secure: true,
		Now:    now,
	})
	if err != nil {
		t.Fatalf("create the session manager: %v", err)
	}
	// The sessions table refers to the accounts, so the owner has to exist
	// before a session can be created.
	userID := "usr_" + ids.New("")
	hash, err := auth.HashPassword("a-valid-password-1")
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	if _, err := st.CreateUser(context.Background(), store.User{
		ID:           userID,
		Username:     "tester-" + userID,
		PasswordHash: hash,
		Role:         store.RoleUser,
	}); err != nil {
		t.Fatalf("create the user: %v", err)
	}
	return manager, st, userID
}

func TestCreateStoresOnlyTheHashOfTheToken(t *testing.T) {
	manager, st, userID := newManager(t, nil)
	session, token, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	if token == "" {
		t.Fatal("the session carries no token")
	}
	if session.TokenHash == token {
		t.Error("the store holds the clear-text token")
	}
	if session.TokenHash != auth.HashSecret(token) {
		t.Error("the store holds a hash of another value")
	}
	if session.CSRFToken == "" {
		t.Error("the session carries no CSRF token")
	}
	if len(session.CSRFToken) != 32 {
		t.Errorf("the CSRF token holds %d characters, want 32", len(session.CSRFToken))
	}
	// The session is found by the hash of the token.
	if _, err := st.SessionByTokenHash(context.Background(), auth.HashSecret(token)); err != nil {
		t.Errorf("the session is not stored: %v", err)
	}
}

func TestValidateSessionResolvesTheOwner(t *testing.T) {
	manager, _, userID := newManager(t, nil)
	session, token, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	user, resolved, err := manager.ValidateSession(context.Background(), token)
	if err != nil {
		t.Fatalf("validate the session: %v", err)
	}
	if user.ID != userID {
		t.Errorf("the session resolved the user %q", user.ID)
	}
	if resolved.ID != session.ID {
		t.Errorf("the session resolved %q, want %q", resolved.ID, session.ID)
	}
	if !manager.VerifyCSRF(resolved, resolved.CSRFToken) {
		t.Error("the CSRF token of the session does not verify")
	}
	if manager.VerifyCSRF(resolved, "not-the-token") {
		t.Error("a wrong CSRF token verified")
	}
}

func TestAnExpiredSessionIsRefused(t *testing.T) {
	now := time.Now().UTC()
	manager, st, userID := newManager(t, func() time.Time { return now })
	_, token, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	// The clock moves past the deadline of the session.
	now = now.Add(2 * time.Hour)
	if _, _, err := manager.ValidateSession(context.Background(), token); err == nil {
		t.Fatal("an expired session was accepted")
	}
	// The expired session is removed right away.
	if _, err := st.SessionByTokenHash(context.Background(), auth.HashSecret(token)); err == nil {
		t.Error("the expired session is still stored")
	}
}

func TestADisabledAccountLosesItsSession(t *testing.T) {
	manager, st, userID := newManager(t, nil)
	_, token, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	if err := st.SetUserDisabled(context.Background(), userID, true); err != nil {
		t.Fatalf("disable the user: %v", err)
	}
	if _, _, err := manager.ValidateSession(context.Background(), token); err == nil {
		t.Fatal("the session of a disabled account was accepted")
	}
}

func TestCleanupExpiredRemovesTheDeadSessions(t *testing.T) {
	now := time.Now().UTC()
	manager, _, userID := newManager(t, func() time.Time { return now })
	if _, _, err := manager.Create(context.Background(), userID); err != nil {
		t.Fatalf("create the session: %v", err)
	}
	now = now.Add(2 * time.Hour)
	removed, err := manager.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("clean the sessions: %v", err)
	}
	if removed != 1 {
		t.Errorf("the cleanup removed %d sessions, want 1", removed)
	}
}

func TestTheCookieCarriesTheProtectiveAttributes(t *testing.T) {
	manager, _, userID := newManager(t, nil)
	session, token, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	recorder := httptest.NewRecorder()
	auth.SetSessionCookie(recorder, token, session.ExpiresAt, manager.Secure())
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("the answer carries %d cookies", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != auth.SessionCookieName {
		t.Errorf("the cookie is named %q", cookie.Name)
	}
	if cookie.Value != token {
		t.Error("the cookie does not carry the token")
	}
	if !cookie.HttpOnly {
		t.Error("the cookie is not HttpOnly")
	}
	if !cookie.Secure {
		t.Error("the cookie is not Secure")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("the cookie carries the same site policy %v", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("the cookie is bound to %q", cookie.Path)
	}

	// The cookie of a request is read back.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	if value := auth.CookieValue(req); value != token {
		t.Errorf("the request carries the cookie %q", value)
	}
}
