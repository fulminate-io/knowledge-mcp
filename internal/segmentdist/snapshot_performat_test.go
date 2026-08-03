// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestSnapshotPerFormat proves the shipped-coverage machinery is scoped to the
// format its caller names: ShippedManifestSnapshot reads THAT format's manifest and
// ShippedDocCountFromSnapshot sums only THAT format's metas, so one format's
// coverage can never be reported as another's.
//
// The two seeded manifests carry DIFFERENT doc counts on purpose. Equal counts would
// collapse the distinction under test — every assertion below would still pass if
// the format argument were ignored entirely and both reads resolved to one manifest.
//
// The third case pins the behavior the format-scoped design leans on: a graph with
// no manifest for the requested format yields an EMPTY snapshot and a zero count
// with a NIL error (gcsSegmentSource.List returns an empty slice on a manifest that
// was never published). That is what lets an arm whose format has never been shipped
// report nothing rather than erroring or borrowing the other arm's numbers.
//
// On a tree where the snapshot API is not yet format-parameterized this file does
// not COMPILE — the two methods took one argument fewer. Its failure mode there is a
// build failure, not an observed behavioral red.
func TestSnapshotPerFormat(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	hnswFormat := hnsw.New().Name()
	bm25Format := bm25.New().Name()

	backend := newFakeSegmentBackend(t)
	// Distinct totals per format: 100+28=128 for one, 60+36=96 for the other.
	backend.seedManifest(string(kgtypes.GraphCode), "repo", hnswFormat, map[string]int{"h1": 100, "h2": 28})
	backend.seedManifest(string(kgtypes.GraphCode), "repo", bm25Format, map[string]int{"b1": 60, "b2": 36})
	// A second graph shipped in ONE format only, for the absent-manifest case.
	backend.seedManifest(string(kgtypes.GraphCode), "oneformat", hnswFormat, map[string]int{"h9": 40})

	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0,
		WithSegmentTransport(func() (SegmentControlTransport, error) { return backend, nil }))

	t.Run("each format reads and sums its own manifest", func(t *testing.T) {
		hnswSnap, err := mgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repo", hnswFormat)
		require.NoError(t, err)
		require.Len(t, hnswSnap, 2)
		for _, m := range hnswSnap {
			require.Equal(t, hnswFormat, m.Format, "the snapshot carries only the requested format")
		}
		covered, anyUnknown := mgr.ShippedDocCountFromSnapshot(hnswSnap, hnswFormat)
		require.Equal(t, 128, covered, "summed from this format's manifest alone (100 + 28)")
		require.False(t, anyUnknown)

		bm25Snap, err := mgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repo", bm25Format)
		require.NoError(t, err)
		require.Len(t, bm25Snap, 2)
		for _, m := range bm25Snap {
			require.Equal(t, bm25Format, m.Format, "the snapshot carries only the requested format")
		}
		covered, anyUnknown = mgr.ShippedDocCountFromSnapshot(bm25Snap, bm25Format)
		require.Equal(t, 96, covered, "summed from this format's manifest alone (60 + 36)")
		require.False(t, anyUnknown)
	})

	t.Run("a format with no manifest yields an empty snapshot and no error", func(t *testing.T) {
		snap, err := mgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "oneformat", bm25Format)
		require.NoError(t, err, "an unpublished manifest is not an error")
		require.Empty(t, snap, "no manifest for this format — nothing shipped")
		require.False(t, mgr.HasShippedFromSnapshot(snap), "presence is false for the unshipped format")

		covered, anyUnknown := mgr.ShippedDocCountFromSnapshot(snap, bm25Format)
		require.Zero(t, covered, "an empty snapshot sums to zero")
		require.False(t, anyUnknown, "an empty snapshot carries no unknown-doc_count meta")

		// The graph's OTHER format is unaffected — the absent arm did not mask it.
		other, err := mgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "oneformat", hnswFormat)
		require.NoError(t, err)
		covered, _ = mgr.ShippedDocCountFromSnapshot(other, hnswFormat)
		require.Equal(t, 40, covered, "the shipped format still reports its own count")
	})
}
