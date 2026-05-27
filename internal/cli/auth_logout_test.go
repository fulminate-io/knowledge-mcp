// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// withFakeDiscovery swaps discoverFn for a stub that returns a fixed
// DiscoveredEndpoints pointing at revocationEndpoint. Used so logout
// tests don't have to mock both .well-known endpoints.
func withFakeDiscovery(t *testing.T, revocationEndpoint string) {
	t.Helper()
	orig := discoverFn
	discoverFn = func(_ context.Context, _ string, _ map[string]struct{}) (*auth.DiscoveredEndpoints, error) {
		return &auth.DiscoveredEndpoints{
			Resource:              "https://fulminate.io/mcp",
			Issuer:                "https://auth.fulminate.io",
			AuthorizationEndpoint: revocationEndpoint + "/authorize",
			TokenEndpoint:         revocationEndpoint + "/token",
			RevocationEndpoint:    revocationEndpoint,
		}, nil
	}
	t.Cleanup(func() { discoverFn = orig })
}

// TestLogoutCmd_RevokesAndDeletesRefreshToken asserts the happy path: a
// stored refresh token is sent to the AuthKit revocation endpoint and
// then removed from the keychain.
func TestLogoutCmd_RevokesAndDeletesRefreshToken(t *testing.T) {
	var revokeHit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&revokeHit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	withFakeDiscovery(t, srv.URL)
	mem := withMemoryStore(t)
	ctx := context.Background()
	if err := mem.Set(ctx, auth.KeyRefreshToken, "frt_old"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := LogoutCmd(nil); err != nil {
		t.Fatalf("LogoutCmd: %v", err)
	}

	if atomic.LoadInt32(&revokeHit) != 1 {
		t.Errorf("revoke endpoint hits: %d (want 1)", revokeHit)
	}
	if _, err := mem.Get(ctx, auth.KeyRefreshToken); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("refresh token not deleted: err=%v", err)
	}
}

// TestLogoutCmd_NoRefreshTokenIsIdempotent asserts logout is safe to
// run when the user is already logged out — no error, no crash.
func TestLogoutCmd_NoRefreshTokenIsIdempotent(t *testing.T) {
	_ = withMemoryStore(t)

	if err := LogoutCmd(nil); err != nil {
		t.Fatalf("LogoutCmd on empty store: %v", err)
	}
}

// TestLogoutCmd_DiscoveryFailureDoesNotBlockLocalCleanup asserts a
// discovery failure (agent unreachable, malformed metadata, host not
// in allowlist) does NOT prevent the local keychain cleanup from
// running. The critical invariant is that the refresh token is gone
// from the keychain by the time LogoutCmd returns.
func TestLogoutCmd_DiscoveryFailureDoesNotBlockLocalCleanup(t *testing.T) {
	orig := discoverFn
	discoverFn = func(_ context.Context, _ string, _ map[string]struct{}) (*auth.DiscoveredEndpoints, error) {
		return nil, errors.New("simulated discovery failure")
	}
	t.Cleanup(func() { discoverFn = orig })

	mem := withMemoryStore(t)
	ctx := context.Background()
	if err := mem.Set(ctx, auth.KeyRefreshToken, "frt_old"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := LogoutCmd(nil); err != nil {
		t.Fatalf("LogoutCmd: %v", err)
	}
	if _, err := mem.Get(ctx, auth.KeyRefreshToken); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("refresh token survived local cleanup: err=%v", err)
	}
}

// TestLogoutCmd_RevokeFailureDoesNotBlockLocalCleanup asserts a failing
// AuthKit revoke call does NOT prevent the local cleanup from running.
func TestLogoutCmd_RevokeFailureDoesNotBlockLocalCleanup(t *testing.T) {
	withFakeDiscovery(t, "http://127.0.0.1:1/revoke")
	mem := withMemoryStore(t)
	ctx := context.Background()
	if err := mem.Set(ctx, auth.KeyRefreshToken, "frt_old"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := LogoutCmd(nil); err != nil {
		t.Fatalf("LogoutCmd: %v", err)
	}
	if _, err := mem.Get(ctx, auth.KeyRefreshToken); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("refresh token survived local cleanup: err=%v", err)
	}
}
