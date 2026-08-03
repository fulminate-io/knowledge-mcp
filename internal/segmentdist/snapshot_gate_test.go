// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestShippedManifestSnapshot_CloudArmReadsGCSManifest proves the cloud (logged-in)
// arm of ShippedManifestSnapshot routes through the gated GCS source: it issues a
// manifest/read and returns metas whose DocCounts match the published manifest; the
// derived HasShippedFromSnapshot / ShippedDocCountFromSnapshot report presence + the
// summed HNSW doc count correctly.
func TestShippedManifestSnapshot_CloudArmReadsGCSManifest(t *testing.T) {
	t.Parallel()

	backend := newFakeSegmentBackend(t)
	backend.seedManifest(string(kgtypes.GraphCode), "repo", "hnsw", map[string]int{"h1": 12, "h2": 30})

	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0,
		WithSegmentTransport(func() (SegmentControlTransport, error) { return backend, nil }))

	snap, err := mgr.ShippedManifestSnapshot(context.Background(), kgtypes.GraphCode, "repo", "hnsw")
	require.NoError(t, err)
	require.Len(t, snap, 2)

	byID := map[string]int{}
	for _, m := range snap {
		require.Equal(t, "hnsw", m.Format)
		require.Equal(t, uint64(0), m.Generation)
		byID[m.ID] = m.DocCount
	}
	require.Equal(t, map[string]int{"h1": 12, "h2": 30}, byID, "doc counts come from the GCS manifest")

	// It read the GCS manifest.
	require.Equal(t, 1, backend.readCalls, "the cloud arm issues one manifest/read")

	// Derived answers over the snapshot.
	require.True(t, mgr.HasShippedFromSnapshot(snap), "presence is true — the manifest holds segments")
	covered, anyUnknown := mgr.ShippedDocCountFromSnapshot(snap, "hnsw")
	require.Equal(t, 42, covered, "summed HNSW doc count = 12 + 30")
	require.False(t, anyUnknown, "no doc_count==0 digest, so anyUnknown is false")
}

// TestShippedManifestSnapshot_OSSArmIsL2Sourced pins the decoupled OSS status seam:
// with the SegmentService deleted, a NOT-logged-in Manager's ShippedManifestSnapshot
// resolves to the L2-local source (its metas carry DocCount=0), and the manage(status)
// coverage column reads its covered count from the L2 RESIDENT doc count
// (ShippedSegmentDocCount branches on IsL2Authoritative to LoadResidentDocCount), NOT
// from the DocCount=0 snapshot — so the OSS coverage column reports the real L2 count
// with ZERO server round-trip.
func TestShippedManifestSnapshot_OSSArmIsL2Sourced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// A NOT-logged-in producer/consumer: its engines run on the L2-local source.
	mgr := NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0)
	require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "repo", hnswVecDocs(searchCorpusN)))
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "repo"))

	// The snapshot resolves to the L2-local set (presence true, DocCounts stamped 0).
	snap, err := mgr.ShippedManifestSnapshot(ctx, kgtypes.GraphCode, "repo", "hnsw")
	require.NoError(t, err)
	require.True(t, mgr.HasShippedFromSnapshot(snap), "the L2 snapshot holds the shipped segments (presence true)")

	// The coverage column reads the L2 RESIDENT doc count via the IsL2Authoritative
	// branch, NOT the DocCount=0 snapshot — so covered is the real L2 count and
	// anyUnknown is never set on the OSS path.
	require.True(t, mgr.IsL2Authoritative(kgtypes.GraphCode, "repo"), "not-logged-in -> L2-authoritative")
	covered, anyUnknown, err := mgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, "repo")
	require.NoError(t, err)
	require.Equal(t, searchCorpusN, covered, "OSS covered = the L2 resident HNSW doc count (NOT 0 from an L2 List)")
	require.False(t, anyUnknown, "the OSS/L2 path never returns anyUnknown")
}
