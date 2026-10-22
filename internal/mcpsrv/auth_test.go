package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// httptestRequest builds a request that carries a bearer token.
func httptestRequest(method, secret string) *http.Request {
	request := httptest.NewRequest(method, "/mcp", nil)
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	return request
}

// TestAuthenticationRefusesMissingToken checks that an anonymous request is
// refused before the transport is reached.
func TestAuthenticationRefusesMissingToken(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	assertFailureCode(t, recorder, CodeAuthenticationFailed)
}

// TestAuthenticationRefusesInvalidToken checks that an unknown credential is
// refused.
func TestAuthenticationRefusesInvalidToken(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptestRequest(http.MethodPost, "dak_not_a_real_secret"))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	assertFailureCode(t, recorder, CodeAuthenticationFailed)
}

// TestAuthenticationRefusesRevokedKey checks that the revocation of a key is
// immediate.
func TestAuthenticationRefusesRevokedKey(t *testing.T) {
	env := newTestEnv(t)
	secret, key := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	if err := env.store.RevokeAPIKey(context.Background(), key.ID); err != nil {
		t.Fatalf("revoke the key: %v", err)
	}
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptestRequest(http.MethodPost, secret))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	assertFailureCode(t, recorder, CodeAuthenticationFailed)
}

// TestAuthenticationRefusesExpiredKey checks that an expired key is refused.
func TestAuthenticationRefusesExpiredKey(t *testing.T) {
	env := newTestEnv(t)
	secret, _, prefix, hash, err := auth.IssueKey()
	if err != nil {
		t.Fatalf("issue the key: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	_, err = env.store.CreateAPIKey(context.Background(), store.APIKey{
		ID:        ids.New(ids.PrefixAPIKey),
		UserID:    env.user.ID,
		Name:      "expired",
		Prefix:    prefix,
		KeyHash:   hash,
		Scopes:    scopeStrings(policy.DefaultUserScopes),
		ExpiresAt: &past,
	})
	if err != nil {
		t.Fatalf("store the key: %v", err)
	}
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptestRequest(http.MethodPost, secret))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	assertFailureCode(t, recorder, CodeAuthenticationFailed)
}

// TestAuthenticationRefusesKeyWithoutScope checks that a key that carries no
// usable scope is refused with the permission code.
func TestAuthenticationRefusesKeyWithoutScope(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, string(policy.ScopeAdmin))
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptestRequest(http.MethodPost, secret))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusForbidden)
	}
	assertFailureCode(t, recorder, CodePermissionDenied)
}

// TestAuthenticationDropsAdminScope checks the rule that an MCP key never
// inherits the power of a browser session.
func TestAuthenticationDropsAdminScope(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t,
		string(policy.ScopeAdmin),
		string(policy.ScopeUsersManage),
		string(policy.ScopeKeysManage),
		string(policy.ScopeJobsRead),
	)

	actor, failure := env.server.authenticate(httptestRequest(http.MethodPost, secret))
	if failure != nil {
		t.Fatalf("the credential was refused: %v", failure)
	}
	if actor.IsAdmin {
		t.Error("the actor of an MCP key is an administrator")
	}
	if actor.Scopes.Has(policy.ScopeAdmin) {
		t.Error("the admin scope survived the authentication")
	}
	if actor.Scopes.Has(policy.ScopeUsersManage) {
		t.Error("the user management scope survived the authentication")
	}
	if actor.Scopes.Has(policy.ScopeKeysManage) {
		t.Error("the key management scope survived the authentication")
	}
	if !actor.Scopes.Has(policy.ScopeJobsRead) {
		t.Error("the ordinary scope of the key was dropped")
	}
}

// TestAuthenticationTouchesTheKey checks that the use of a key is recorded.
func TestAuthenticationTouchesTheKey(t *testing.T) {
	env := newTestEnv(t)
	secret, key := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)

	if _, failure := env.server.authenticate(httptestRequest(http.MethodPost, secret)); failure != nil {
		t.Fatalf("the credential was refused: %v", failure)
	}
	stored, err := env.store.APIKeyByID(context.Background(), key.ID)
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if stored.LastUsedAt == nil {
		t.Error("the use of the key was not recorded")
	}
}

// TestHandlerRejectsUnsupportedMethods checks that the transport refuses a
// method it does not serve.
func TestHandlerRejectsUnsupportedMethods(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptestRequest(http.MethodPut, secret))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if allow := recorder.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("the answer allows %q", allow)
	}
}

// TestHandlerServesTheCertificateAuthority checks that the anchor of the
// trust of the node is published.
func TestHandlerServesTheCertificateAuthority(t *testing.T) {
	env := newTestEnv(t)
	path := filepath.Join(env.dir, "ca.crt")
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write the certificate: %v", err)
	}
	env.server.caPath = path
	handler := NewHandler(env.server)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ca.crt", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("the answer carries the status %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "BEGIN CERTIFICATE") {
		t.Errorf("the answer carries %q", recorder.Body.String())
	}
}

// assertFailureCode checks the code of a refused request.
func assertFailureCode(t *testing.T, recorder *httptest.ResponseRecorder, want Code) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code          Code   `json:"code"`
			Message       string `json:"message"`
			CorrelationID string `json:"correlation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the answer %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error.Code != want {
		t.Errorf("the answer carries the code %q, want %q", envelope.Error.Code, want)
	}
	if envelope.Error.Message == "" {
		t.Error("the answer carries no message")
	}
}
