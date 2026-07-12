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

// failFirstListSource wraps a segmentSource and fails the FIRST List call (a
// transient connect.CodeUnavailable — slow/down server at the moment of the seed),
// then passes every subsequent call through. It is the fail-FIRST mirror of
// failAfterWarmSource: the seed's List(0) trips on call #1 and recovers on call #2.
type failFirstListSource struct {
	inner segmentSource

	mu        sync.Mutex
	listCalls int
}

func (c *failFirstListSource) List(ctx context.Context, sinceGen uint64) ([]searchengine.SegmentMeta, error) {
	c.mu.Lock()
	c.listCalls++
	first := c.listCalls == 1
	c.mu.Unlock()
	if first {
		return nil, connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)
	}
	return c.inner.List(ctx, sinceGen)
}

func (c *failFirstListSource) Fetch(ctx context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	return c.inner.Fetch(ctx, ids)
}

func (c *failFirstListSource) Ship(ctx context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return c.inner.Ship(ctx, blobs)
}

func (c *failFirstListSource) Prune(ids []searchengine.SegmentID) (int, error) {
	return c.inner.Prune(ids)
}

func (c *failFirstListSource) PublishManifest(format string, digests []segmentDigest) (int, error) {
	return c.inner.PublishManifest(format, digests)
}

func (c *failFirstListSource) verifiesCompletenessServerSide() bool {
	return c.inner.verifiesCompletenessServerSide()
}

var _ segmentSource = (*failFirstListSource)(nil)

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
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "seedrearm"}
	ctx := context.Background()

	svc := newSharedServerFake()
	src := &failFirstListSource{inner: svc.viewFor(target, "")}
	eng := newMockEngine()
	require.NoError(t, eng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, eng.Add([]searchengine.Document{doc("d2", "beta")}))
	mgr := newDistManager(eng, src, newDiskSegmentCache(t.TempDir(), 0), target, "")

	// First ship: the seed's List(0) trips on the transient failure → ship bails.
	_, err := mgr.ship(ctx, mgr.locallyShipped)
	require.Error(t, err, "the first ship bails on the transient seed List failure")

	// Second ship: the seed must RE-ARM — List(0) succeeds this time and the ship
	// goes through. RED on current source: the consumed Once returns the cached
	// seedErr again, so this ship still fails and the corpus is never shipped.
	_, err = mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err, "a ship after a transient seed failure must retry the seed and succeed")

	// The corpus actually landed on the server (the seed re-armed AND the ship ran).
	metas := svc.listDelta(target, "", 0)
	require.Len(t, metas, 2, "both segments must be shipped after the re-armed seed")
}
