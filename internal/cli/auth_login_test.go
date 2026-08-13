// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// fakeAuthStore is the cli-package test fake for [auth.Store]. Production
// uses the keychain-backed [auth.NewStore], unavailable in CI.
type fakeAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{data: make(map[string]string)}
}

func (s *fakeAuthStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return "", auth.ErrNotFound
	}
	return v, nil
}

func (s *fakeAuthStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *fakeAuthStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return auth.ErrNotFound
	}
	delete(s.data, key)
	return nil
}

// withMemoryStore swaps newStoreFn for a fresh in-memory fake store for
// the duration of a test. The returned *fakeAuthStore lets the test
// assert what the subcommand persisted.
func withMemoryStore(t *testing.T) *fakeAuthStore {
	t.Helper()
	mem := newFakeAuthStore()
	orig := newStoreFn
	newStoreFn = func() (auth.Store, error) { return mem, nil }
	t.Cleanup(func() { newStoreFn = orig })
	return mem
}

// withFakeBrowserFlow swaps the browser PKCE flow for one that returns a
// fixed result, so the persist half of login can be driven without a
// browser, a loopback listener, or a network call.
func withFakeBrowserFlow(t *testing.T, clientID string, tr *auth.TokenResponse) {
	t.Helper()
	orig := runBrowserPKCEFlowFn
	runBrowserPKCEFlowFn = func(context.Context, *auth.DiscoveredEndpoints) (string, *auth.TokenResponse, error) {
		return clientID, tr, nil
	}
	t.Cleanup(func() { runBrowserPKCEFlowFn = orig })
}

// TestLoginCmd_PublishesReadableSession asserts login leaves behind a session
// a read-only consumer can use immediately. Without it a freshly logged-in
// machine would have no readable session until the owning process happened to
// refresh — up to a full token lifetime of a reader failing on a machine the
// operator just logged into.
func TestLoginCmd_PublishesReadableSession(t *testing.T) {
	ctx := context.Background()
	mem := withMemoryStore(t)
	withFakeDiscovery(t, "http://revocation.invalid")
	withFakeBrowserFlow(t, "cli-123", &auth.TokenResponse{
		AccessToken:  "at_from_login",
		RefreshToken: "frt_from_login",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	})

	if err := loginBrowserPKCE(ctx); err != nil {
		t.Fatalf("loginBrowserPKCE: %v", err)
	}

	// The pre-existing contract still holds.
	if got, err := mem.Get(ctx, auth.KeyRefreshToken); err != nil || got != "frt_from_login" {
		t.Errorf("refresh token = (%q, %v), want the one login received", got, err)
	}
	if got, err := mem.Get(ctx, auth.KeyClientID); err != nil || got != "cli-123" {
		t.Errorf("client id = (%q, %v), want the registered one", got, err)
	}

	// The session is readable through the consumer that will use it, which
	// is the property that matters — not merely that a key exists.
	tok, _, err := auth.NewReadOnlyTokenSource(mem).Token(ctx)
	if err != nil {
		t.Fatalf("a reader found no session after login: %v", err)
	}
	if tok != "at_from_login" {
		t.Errorf("reader served %q, want the token login received", tok)
	}
}
