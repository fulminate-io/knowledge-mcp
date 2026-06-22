// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// failFirstListCaller wraps a real segmentCaller and fails the FIRST ListDelta call
// (a transient connect.CodeUnavailable — slow/down server at the moment of the
// seed), then passes every subsequent call through. It is the fail-FIRST mirror of
// failAfterWarmCaller: the seed's List(0) trips on call #1 and recovers on call #2.
type failFirstListCaller struct {
	inner segmentCaller

	mu        sync.Mutex
	listCalls int
}

func (c *failFirstListCaller) Ship(ctx context.Context, req *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error) {
	return c.inner.Ship(ctx, req)
}

func (c *failFirstListCaller) ListDelta(ctx context.Context, req *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error) {
	c.mu.Lock()
	c.listCalls++
	first := c.listCalls == 1
	c.mu.Unlock()
	if first {
		return nil, connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)
	}
	return c.inner.ListDelta(ctx, req)
}

func (c *failFirstListCaller) Fetch(ctx context.Context, req *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error) {
	return c.inner.Fetch(ctx, req)
}

func (c *failFirstListCaller) Prune(ctx context.Context, req *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error) {
	return c.inner.Prune(ctx, req)
}

func (c *failFirstListCaller) Publish(ctx context.Context, req *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error) {
	return c.inner.Publish(ctx, req)
}

var _ segmentCaller = (*failFirstListCaller)(nil)

// TestEnsureShippedSeeded_RetriesAfterTransientFailure is the C3 regression: a
// TRANSIENT seed List(0) failure must NOT permanently disable shipping. The first
// ship bails when the seed List errors; a SECOND ship must re-arm the seed, succeed
// at List(0), and ship the corpus.
//
// RED pre-fix: ensureShippedSeeded runs List(0) under a sync.Once and caches the
// error in seedErr; the Once is consumed by the first (failed) attempt, so every
// later ship returns the same stale seedErr — shipping is dead for the process
// lifetime. GREEN after re-arm: the seed only latches on List(0) SUCCESS, so the
// second ship retries and ships.
func TestEnsureShippedSeeded_RetriesAfterTransientFailure(t *testing.T) {
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "seedrearm"}
	ctx := context.Background()

	caller := &failFirstListCaller{inner: gc}
	eng := newMockEngine()
	require.NoError(t, eng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, eng.Add([]searchengine.Document{doc("d2", "beta")}))
	mgr, _ := buildManager(eng, caller, target, t.TempDir())

	// First ship: the seed's List(0) trips on the transient failure → ship bails.
	_, err := mgr.ship(ctx, mgr.locallyShipped)
	require.Error(t, err, "the first ship bails on the transient seed List failure")

	// Second ship: the seed must RE-ARM — List(0) succeeds this time and the ship
	// goes through. RED on current source: the consumed Once returns the cached
	// seedErr again, so this ship still fails and the corpus is never shipped.
	_, err = mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err, "a ship after a transient seed failure must retry the seed and succeed")

	// The corpus actually landed on the server (the seed re-armed AND the ship ran).
	src := newRPCSegmentSource(gc, target, "", ctx)
	metas, listErr := src.List(ctx, 0)
	require.NoError(t, listErr)
	require.Len(t, metas, 2, "both segments must be shipped after the re-armed seed")
}
