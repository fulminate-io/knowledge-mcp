// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestRouter_LoggedIn_KeychainOnly proves Router.LoggedIn and the pick-driven
// Execute forwarder consult ONLY the keychain auth state (KeyRefreshToken
// presence) — there is no per-session context override. The context carries an
// unrelated value to demonstrate that no ctx key flips routing: only the
// keychain refresh token does.
func TestRouter_LoggedIn_KeychainOnly(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := NewGraphClientForURL(localURL)
	store := newFakeAuthStore() // empty initially → not logged in
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)

	// A context carrying an arbitrary value — no context-bearer mechanism exists
	// to read it, so it must not influence routing. Routing flips only on the
	// keychain refresh token, never on a context value.
	type unrelatedKey struct{}
	ctx := context.WithValue(opCtx(), unrelatedKey{}, "ignored-by-routing")

	// Empty keychain → LoggedIn false → routes local, even with the ctx value.
	assert.False(t, r.LoggedIn(ctx), "LoggedIn must be false with an empty keychain regardless of ctx")
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load(), "empty keychain must route local")
	assert.Equal(t, int32(0), cloudEng.execute.Load(), "empty keychain must not route cloud")

	// Populate the keychain refresh token (the `knowledge login` effect); wait
	// past the TTL so the next IsLoggedIn re-reads the store.
	require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond)

	// Refresh token present → LoggedIn true → routes cloud, off the keychain
	// alone (the same ctx, still carrying only the unrelated value).
	assert.True(t, r.LoggedIn(ctx), "LoggedIn must be true once the keychain holds a refresh token")
	_, err = r.Execute(ctx, &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.execute.Load(), "local count must not advance after the keychain login")
	assert.Equal(t, int32(1), cloudEng.execute.Load(), "keychain refresh token must route cloud")
}
