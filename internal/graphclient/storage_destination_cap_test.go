// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// burstAccount spells the nth distinct, well-formed account id of a burst.
func burstAccount(n int) string {
	return fmt.Sprintf("%08d-0000-4000-8000-000000000000", n)
}

// TestBoundCloudClientsAreCapped is the key-space bound. Until a request could
// name its own account, only the selected account could key the per-destination
// client map; now any caller-supplied account id can, so a burst of distinct
// header values would grow one client per id forever. The map is capped, the
// least recently used non-selected destination is evicted, and the expectation
// READS the constant rather than repeating its value.
func TestBoundCloudClientsAreCapped(t *testing.T) {
	const selected = "11111111-1111-4111-8111-111111111111"
	installSelection(t, selected)

	cloudURL, engine := startAccountRoutedEngine(t)
	r := NewRouterWithMachineAuth(nil, cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)

	ctx := opCtx()
	selectedDestination := Destination{Storage: "cloud", AccountID: selected}
	kept := r.cloudForDestination(ctx, selectedDestination)

	for n := range MaxBoundDestinations + 8 {
		r.cloudForDestination(ctx, Destination{Storage: "cloud", AccountID: burstAccount(n)})
	}

	r.mu.Lock()
	size := len(r.boundCloud)
	survivor := r.boundCloud[selectedDestination]
	r.mu.Unlock()

	assert.LessOrEqual(t, size, MaxBoundDestinations,
		"a burst of %d caller-named accounts must not grow the client map past the cap",
		MaxBoundDestinations+8)
	assert.Same(t, kept, survivor,
		"the SELECTED account's client must survive a burst of foreign accounts")

	// And the selected account is still served: eviction releases clients, it
	// does not break routing.
	_, err := r.Execute(WithDestination(ctx, selectedDestination), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, engine.executesFor(selected))
}

// TestBoundCloudClientsKeepTheDaemonsOwnDestination is the keep-rule the
// segment cache has too: the cap bounds CALLER-DRIVEN key space, so the
// accountless cloud binding the daemon's own machine-auth work runs on is never
// an eviction candidate, however many header accounts a caller names.
func TestBoundCloudClientsKeepTheDaemonsOwnDestination(t *testing.T) {
	installSelection(t, "11111111-1111-4111-8111-111111111111")

	r := NewRouterWithMachineAuth(nil, "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)

	ctx := opCtx()
	accountless := Destination{Storage: "cloud"}
	kept := r.cloudForDestination(ctx, accountless)

	for n := range MaxBoundDestinations + 8 {
		r.cloudForDestination(ctx, Destination{Storage: "cloud", AccountID: burstAccount(n)})
	}

	r.mu.Lock()
	survivor := r.boundCloud[accountless]
	r.mu.Unlock()
	assert.Same(t, kept, survivor,
		"the accountless cloud binding is the daemon's own work, not caller-driven key space")
}

// TestBoundCloudClientEvictionSparesAnInFlightRequest is the in-flight axis on
// this side: an RPC already dispatched through a client the cap then evicts must
// still complete. Eviction therefore releases IDLE connections only — tearing
// the transport down would surface to that request as a transport error, which
// is what GraphClient.Close's own doc comment warns about.
//
// WHAT THIS TEST CANNOT DISCRIMINATE, proven by mutation: it passes under BOTH
// spellings, because newCloudGraphClient records no connections in
// GraphClient.conns, so Close degrades to the idle release for a cloud client
// and the two calls are the same operation today. A corpus check carries that
// half — "the destination cap's eviction releases idle connections, never
// Close, under an in-flight request", which fires on a Close inside
// evictBoundCloudLocked — until the cloud constructor tracks its connections
// and this test starts telling the two apart on its own.
func TestBoundCloudClientEvictionSparesAnInFlightRequest(t *testing.T) {
	const inFlightAccount = "33333333-3333-4333-8333-333333333333"
	installSelection(t, "11111111-1111-4111-8111-111111111111")

	entered := make(chan struct{})
	release := make(chan struct{})
	engineHandler := newAccountCapture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(auth.AccountHeaderName) == inFlightAccount {
			close(entered)
			<-release
		}
		engineHandler.ServeHTTP(w, req)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	r := NewRouterWithMachineAuth(nil, srv.URL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)

	ctx := WithDestination(opCtx(), Destination{Storage: "cloud", AccountID: inFlightAccount})
	done := make(chan error, 1)
	go func() {
		_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		done <- err
	}()

	<-entered // the request is on the wire and the server is holding it
	for n := range MaxBoundDestinations + 8 {
		r.cloudForDestination(opCtx(), Destination{Storage: "cloud", AccountID: burstAccount(n)})
	}

	// CONTROL, taken while the request is still blocked: the burst really DID
	// evict this destination's client. Without it a green below could mean the
	// eviction never reached the destination under test, which is the vacuous
	// way for this pin to pass.
	r.mu.Lock()
	_, stillCached := r.boundCloud[Destination{Storage: "cloud", AccountID: inFlightAccount}]
	r.mu.Unlock()
	require.False(t, stillCached, "CONTROL: the burst must have evicted the in-flight request's client")

	close(release)
	require.NoError(t, <-done, "a request already in flight must not be torn down by the cap evicting its client")

	// ...and it was a real round trip, stamped with the account under test.
	seen, present := engineHandler.observed()
	assert.True(t, present)
	assert.Equal(t, inFlightAccount, seen)
}

// TestBoundCloudClientCacheStillCaches is the cap's known-positive control: the
// same destination asked for twice is the SAME client, so the cap above is a
// bound on a working cache rather than a cache that never hits.
func TestBoundCloudClientCacheStillCaches(t *testing.T) {
	r := NewRouterWithMachineAuth(nil, "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)

	d := Destination{Storage: "cloud", AccountID: burstAccount(1)}
	first := r.cloudForDestination(opCtx(), d)
	assert.Same(t, first, r.cloudForDestination(opCtx(), d))
}
