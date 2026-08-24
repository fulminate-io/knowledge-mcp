// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestOSSZeroSegmentServiceRPC is the ticket's headline success criterion: a full
// OSS-mode segment lifecycle (write + tick + Flush + Search + PruneCache) over a
// not-logged-in caller runs entirely on the *localSegmentSource, which issues ZERO
// network calls by construction (List==cache.Keys(), Ship/Prune/PublishManifest are
// local/no-op), and search still returns the shipped docs from L2 alone.
//
// With the SegmentService deleted, "zero server RPC" is now a STRUCTURAL property:
// the OSS path resolves to *localSegmentSource, whose every leg is local. This test
// asserts that source selection and drives the whole lifecycle to prove the local
// source round-trips end to end (decouple #2: the ship-seed reads cache.Keys(), not
// a server List).
func TestOSSZeroSegmentServiceRPC(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0))
	require.True(t, mgr.IsL2Authoritative(kgtypes.GraphCode, "ossE2E"), "not-logged-in caller -> OSS-local source")
	require.IsType(t, (*localSegmentSource)(nil), mgr.managerFor(kgtypes.GraphCode, "ossE2E").source,
		"the OSS lifecycle runs on the local source (no network leg exists)")

	docs := hnswVecDocs(60)

	// SHIP path: the write force-seals the sub-MinSegmentDocs batch and the tick
	// ships it; the trailing Flush is the quiescence call. The ship-seed
	// (ensureShippedSeeded) reads cache.Keys() locally.
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, "ossE2E", docs)
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "ossE2E"))

	// SEARCH path: load (L2-only) + query returns the shipped docs from L2.
	hits, err := mgr.Search(ctx, kgtypes.GraphCode, "ossE2E", "", docs[0].Vector, 10)
	require.NoError(t, err)
	require.NotEmpty(t, hits, "search returns the shipped docs from L2 (round-trips end to end with zero network)")

	// RECLAIM path: PruneCache over the local L2 live set.
	_, err = mgr.PruneCache(ctx, []PruneCacheTarget{{GraphType: kgtypes.GraphCode, Name: "ossE2E"}}, true)
	require.NoError(t, err)
}
