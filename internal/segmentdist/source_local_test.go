// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestLocalSegmentSource_ZeroNetworkOverL2 exercises the OSS-local segmentSource
// against a REAL diskSegmentCache (no caller, no server): all five legs operate
// over L2 alone. The type holds only a cache + format — there is no transport
// field, so a network call is structurally impossible to issue.
func TestLocalSegmentSource_ZeroNetworkOverL2(t *testing.T) {
	ctx := context.Background()
	cache := newDiskSegmentCache(t.TempDir(), 0)
	src := newLocalSegmentSource(cache, "hnsw")

	// Seed two content-addressed blobs into L2.
	cache.Put("id-a", []byte("bytes-a"))
	cache.Put("id-b", []byte("bytes-b"))

	t.Run("List maps cache.Keys to Gen0 metas, sinceGen ignored", func(t *testing.T) {
		metas0, err := src.List(ctx, 0)
		require.NoError(t, err)
		require.Len(t, metas0, 2, "one meta per cache.Keys() id")
		byID := map[searchengine.SegmentID]searchengine.SegmentMeta{}
		for _, m := range metas0 {
			byID[m.ID] = m
			require.Equal(t, "hnsw", m.Format, "meta carries the source format")
			require.Equal(t, uint64(0), m.Generation, "OSS metas are Generation 0 (content-hash identity)")
		}
		require.Contains(t, byID, searchengine.SegmentID("id-a"))
		require.Contains(t, byID, searchengine.SegmentID("id-b"))

		// sinceGen is IGNORED — List(999) returns the SAME full set as List(0).
		metas999, err := src.List(ctx, 999)
		require.NoError(t, err)
		require.ElementsMatch(t, metas0, metas999, "sinceGen is ignored: List(0) == List(999)")
	})

	t.Run("Fetch returns cached bytes for present ids, silently omits misses", func(t *testing.T) {
		blobs, err := src.Fetch(ctx, []searchengine.SegmentID{"id-a", "missing", "id-b"})
		require.NoError(t, err, "a miss is not an error — no server to fall back to")
		got := map[searchengine.SegmentID][]byte{}
		for _, b := range blobs {
			got[b.ID] = b.Bytes
		}
		require.Len(t, got, 2, "the missing id is silently omitted")
		require.Equal(t, []byte("bytes-a"), got["id-a"])
		require.Equal(t, []byte("bytes-b"), got["id-b"])
		require.NotContains(t, got, searchengine.SegmentID("missing"))
	})

	t.Run("Ship maps blobs to Gen0 metas carrying the blob DocCount", func(t *testing.T) {
		in := []*knowledgev1.SegmentBlobProto{
			{Id: "id-c", Format: "hnsw", Generation: 7, DocCount: 42, Bytes: []byte("c")},
		}
		metas, err := src.Ship(ctx, in)
		require.NoError(t, err)
		require.Len(t, metas, 1)
		require.Equal(t, "id-c", metas[0].GetId())
		require.Equal(t, "hnsw", metas[0].GetFormat())
		require.Equal(t, uint64(0), metas[0].GetGeneration(), "Ship stamps Generation 0 locally (no server ordering)")
		require.Equal(t, int32(42), metas[0].GetDocCount(), "DocCount is carried from the input blob")
	})

	t.Run("Prune and PublishManifest are local no-ops", func(t *testing.T) {
		n, err := src.Prune([]searchengine.SegmentID{"id-a"})
		require.NoError(t, err)
		require.Equal(t, 0, n, "OSS Prune deletes nothing server-side (0, nil)")

		n, err = src.PublishManifest("hnsw", []segmentDigest{{ID: "id-a", DocCount: 1}, {ID: "id-b", DocCount: 2}})
		require.NoError(t, err)
		require.Equal(t, 0, n, "OSS PublishManifest reaps nothing server-side (0, nil)")
	})
}
