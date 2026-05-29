// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// DefaultAuthCheckTTL is the cache window applied to the "is the user logged
// in?" decision used by the routing layer (cmd/knowledge/internal/graphclient
// Router.pick). Keychain reads on darwin shell out to /usr/bin/security per
// call (~10ms each per zalando/go-keyring docs) and routing fires on every
// MCP tool dispatch — caching the boolean for 5 seconds collapses N kHz of
// keychain pressure into one read per session-of-activity while keeping
// mid-session login swap latency bounded.
const DefaultAuthCheckTTL = 5 * time.Second

// AuthState answers "is the user currently logged in?" with a short-lived
// in-memory cache around a [Store] Get(KeyRefreshToken) lookup. It is the
// runtime signal the routing layer consults to decide whether to dispatch a
// tool call to the local server (logged out) or to the Fulminate cloud
// agent (logged in).
//
// Concurrency: IsLoggedIn is safe for concurrent use — all state is guarded
// by the receiver mutex.
//
// Mid-session login contract: when a user runs `knowledge login` in a
// separate process while an MCP session is open, that login writes a
// refresh token to the shared keychain. The next IsLoggedIn call after the
// TTL expires reads the new token and the cached boolean flips to true; the
// routing layer's next dispatch lands on cloud. No IPC, no signal — keychain
// is the shared state.
type AuthState struct {
	store Store
	ttl   time.Duration

	mu        sync.Mutex
	lastCheck time.Time
	loggedIn  bool
	// warnedOnce keeps backend-failure warnings to one per session so a
	// transient keychain outage does not flood the log. Mirrors the
	// OAuthTokenSource.warnedOnce contract in tokensource.go:88.
	warnedOnce bool
}

// NewAuthState wires an AuthState against the given store and TTL. When ttl
// is zero it falls back to DefaultAuthCheckTTL — callers MAY pass a smaller
// ttl in tests to exercise expiry without sleeping for seconds, but
// production wiring should rely on the default.
func NewAuthState(store Store, ttl time.Duration) *AuthState {
	if ttl <= 0 {
		ttl = DefaultAuthCheckTTL
	}
	return &AuthState{store: store, ttl: ttl}
}

// IsLoggedIn returns true when a refresh token is present in the backing
// store, false otherwise. A fresh check is performed at most once per ttl;
// in between, the cached result is returned without touching the store.
//
// Error handling:
//   - Store.Get returning [ErrNotFound] is the canonical "not logged in"
//     state. IsLoggedIn returns false and caches that result.
//   - Any other backend error (keychain ACL denial, dbus failure, etc.) is
//     treated as a transient failure: IsLoggedIn returns the LAST KNOWN
//     value (zero value = false on first call), refreshes lastCheck so the
//     next call honors the TTL, and emits a single WARN log per session
//     describing the failure. This matches the OAuthTokenSource
//     warnRefreshFailureOnce shape (tokensource.go:239).
func (a *AuthState) IsLoggedIn(ctx context.Context) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.lastCheck.IsZero() && time.Since(a.lastCheck) < a.ttl {
		return a.loggedIn
	}

	_, err := a.store.Get(ctx, KeyRefreshToken)
	a.lastCheck = time.Now()
	switch {
	case err == nil:
		a.loggedIn = true
	case errors.Is(err, ErrNotFound):
		a.loggedIn = false
	default:
		a.warnBackendFailureOnce(err)
		// Keep the prior cached value; lastCheck above bumps the TTL so we
		// do not hammer a failing keychain on every routing decision.
	}
	return a.loggedIn
}

// warnBackendFailureOnce emits a single WARN per session. Caller must hold a.mu.
func (a *AuthState) warnBackendFailureOnce(err error) {
	if a.warnedOnce {
		return
	}
	a.warnedOnce = true
	slog.Warn("auth: keychain probe failed — routing held at last-known state",
		"error", err,
		"hint", "if this persists, rerun `knowledge login` to refresh the credential",
	)
}
