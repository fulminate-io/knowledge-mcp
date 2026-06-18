// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestStaticTokenSource_ReturnsFields asserts the zero-IO implementation.
func TestStaticTokenSource_ReturnsFields(t *testing.T) {
	s := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeRead: {}}}
	got, perms, err := s.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok" {
		t.Errorf("token: got %q want tok", got)
	}
	if !perms.Has(PermMCPKnowledgeRead) {
		t.Errorf("expected mcp:knowledge:read, got %v", perms.List())
	}
}

// TestStaticTokenSource_NotRefreshing is the load-bearing guard for the
// "no refresh loop" property of the headless machine-auth path: a
// StaticTokenSource implements TokenSource (so it can drive every cloud
// call) but deliberately NOT RefreshingTokenSource, so a 401 on a
// machine-bearer request is surfaced to the caller rather than triggering a
// force-refresh retry it could never satisfy (the opaque token has no
// refresh credential behind it).
func TestStaticTokenSource_NotRefreshing(t *testing.T) {
	var _ TokenSource = StaticTokenSource{}
	if _, ok := any(StaticTokenSource{}).(RefreshingTokenSource); ok {
		t.Fatal("StaticTokenSource must NOT implement RefreshingTokenSource — " +
			"a machine bearer has no refresh credential, so a 401 must surface to the caller, not spin a force-refresh retry")
	}
}

// signTestJWT makes a JWT with the given permissions claim and exp (as
// seconds since epoch). Signature value is irrelevant — the client
// doesn't verify.
func signTestJWT(t *testing.T, permissions []string, expUnix int64) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":         "user-1",
		"permissions": permissions,
		"exp":         expUnix,
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test"))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return tok
}

// testEndpoints returns a DiscoveredEndpoints rooted at the given token
// endpoint, suitable for pre-populating OAuthTokenSource.endpoints so
// tests skip the discovery step.
func testEndpoints(tokenEndpoint string) *DiscoveredEndpoints {
	return &DiscoveredEndpoints{
		Resource:              "https://fulminate.io/mcp",
		Issuer:                "https://auth.fulminate.io",
		AuthorizationEndpoint: tokenEndpoint + "/authorize",
		TokenEndpoint:         tokenEndpoint,
		RevocationEndpoint:    tokenEndpoint + "/revoke",
	}
}

// newOAuthSourceForTest builds an OAuthTokenSource with the discovery
// step pre-resolved so tests don't have to mock .well-known endpoints.
func newOAuthSourceForTest(store Store, tokenEndpoint string) *OAuthTokenSource {
	src := NewOAuthTokenSource(store, "https://fulminate.io", map[string]struct{}{"auth.fulminate.io": {}})
	src.endpoints = testEndpoints(tokenEndpoint)
	// The refresh path loads the DCR-issued client_id from the store; seed
	// it so refresh-exercising tests don't each have to.
	_ = store.Set(context.Background(), KeyClientID, "cli")
	return src
}

// TestOAuthTokenSource_CacheHitNoNetwork asserts that a cached access
// token with a far-future expiry returns immediately without contacting
// the server.
func TestOAuthTokenSource_CacheHitNoNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newTestStore()
	src := newOAuthSourceForTest(store, srv.URL)
	// Pre-populate the in-memory cache with a token valid well beyond the
	// 5-minute margin.
	src.accessToken = signTestJWT(t, []string{PermMCPKnowledgeRead, PermDeployBYOC}, time.Now().Add(1*time.Hour).Unix())
	src.expiresAt = time.Now().Add(1 * time.Hour)
	src.permissions = PermissionSet{PermMCPKnowledgeRead: {}, PermDeployBYOC: {}}

	tok, perms, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == "" || !perms.Has(PermMCPKnowledgeRead) {
		t.Errorf("unexpected return: tok=%q perms=%v", tok, perms.List())
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 network calls, got %d", got)
	}
}

// TestOAuthTokenSource_PreExpiryRefresh asserts a token within the
// accessCacheMargin triggers a refresh call that rotates the stored
// refresh token and caches the new access token.
func TestOAuthTokenSource_PreExpiryRefresh(t *testing.T) {
	var calls atomic.Int32
	newAccess := signTestJWT(t, []string{PermMCPKnowledgeRead, PermMCPKnowledgeWrite, PermDeployBYOC}, time.Now().Add(1*time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil { //nolint:gosec // test fixture: httptest.Server with trusted local input
			t.Fatalf("parse form: %v", err)
		}
		if got := r.PostFormValue("grant_type"); got != "refresh_token" { //nolint:gosec // test fixture
			t.Errorf("unexpected grant_type: %q", got)
		}
		if got := r.PostFormValue("refresh_token"); got != "frt_old" { //nolint:gosec // test fixture
			t.Errorf("unexpected refresh_token: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{ //nolint:gosec // test fixture with literal string values
			AccessToken:  newAccess,
			RefreshToken: "frt_new",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	store := newTestStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "frt_old"); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	src := newOAuthSourceForTest(store, srv.URL)
	// Cache is empty → refresh path.
	tok, perms, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != newAccess {
		t.Errorf("expected new access token")
	}
	if !perms.Has(PermDeployBYOC) {
		t.Errorf("unexpected permissions: %v", perms.List())
	}

	// Rotated refresh token persisted to store.
	persisted, err := store.Get(context.Background(), KeyRefreshToken)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if persisted != "frt_new" {
		t.Errorf("expected frt_new in store, got %q", persisted)
	}

	// Second call hits the cache — no additional network.
	calls.Store(0)
	tok2, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if tok2 != newAccess {
		t.Errorf("second call returned different token")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected cache hit on 2nd call, got %d network calls", got)
	}
}

// TestOAuthTokenSource_RefreshFailureWarnsOnce asserts the warn-once
// guard fires exactly once per failure streak and is returned as an
// error each call.
func TestOAuthTokenSource_RefreshFailureWarnsOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
	}))
	defer srv.Close()

	store := newTestStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "frt_bad"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := newOAuthSourceForTest(store, srv.URL)

	_, _, err := src.Token(context.Background())
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("expected ErrInvalidGrant on first call, got %v", err)
	}
	if !src.warnedOnce {
		t.Error("expected warnedOnce=true after failure")
	}

	// Second failure — still errors, warn is not re-emitted (guard true).
	_, _, err = src.Token(context.Background())
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("expected ErrInvalidGrant on second call, got %v", err)
	}
	if !src.warnedOnce {
		t.Error("warnedOnce guard lost between calls")
	}
}

// TestOAuthTokenSource_NoStoredRefreshToken surfaces ErrNotFound.
func TestOAuthTokenSource_NoStoredRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	src := newOAuthSourceForTest(newTestStore(), srv.URL)
	_, _, err := src.Token(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestOAuthTokenSource_SuccessClearsWarnOnce asserts a successful refresh
// resets warnedOnce so a later failure warns again.
func TestOAuthTokenSource_SuccessClearsWarnOnce(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{ //nolint:gosec // test fixture with literal string values
			AccessToken:  signTestJWT(t, []string{PermMCPKnowledgeRead}, time.Now().Add(time.Hour).Unix()),
			RefreshToken: "frt_ok",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	store := newTestStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "frt_x"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := newOAuthSourceForTest(store, srv.URL)

	if _, _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expected failure on first call")
	}
	if !src.warnedOnce {
		t.Fatal("warnedOnce should be true after first failure")
	}

	// Flip server to succeed.
	fail.Store(false)
	// Force a refresh by clearing the cache.
	src.accessToken = ""
	src.expiresAt = time.Time{}

	if _, _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("expected success after recovery, got %v", err)
	}
	if src.warnedOnce {
		t.Error("warnedOnce should reset after successful refresh")
	}
}
