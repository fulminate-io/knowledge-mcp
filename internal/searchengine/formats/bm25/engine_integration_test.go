// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fieldDocs builds n documents that all share a common term plus a per-doc unique
// term, so an exact-match query for a doc's unique term recovers exactly that doc
// (BM25 is exact, unlike HNSW's approximate recall — so we assert exact recovery).
func fieldDocs(n int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		docs[i] = searchengine.Document{
			ID: fmt.Sprintf("n%d", i),
			Fields: map[string]string{
				searchengine.FieldSymbolName: fmt.Sprintf("uniqueterm%d", i),
				searchengine.FieldSummary:    fmt.Sprintf("shared corpus body token%d common", i),
			},
		}
	}
	return docs
}

// containsID reports whether any hit carries the given id.
func containsID(hits []searchengine.Hit, id string) bool {
	for _, h := range hits {
		if h.ID == id {
			return true
		}
	}
	return false
}

// TestEngineIntegrationBM25Lifecycle is Phase 2 Step 2's criterion: drive the REAL
// BM25 format through the segmented engine end-to-end — Add across multiple sealed
// segments, cross-segment fan-out Search, Delete (liveDocs), Export, Import into a
// fresh engine, and Merge — asserting correctness at each stage under the engine's
// recompute-S-on-set-change behavior.
func TestEngineIntegrationBM25Lifecycle(t *testing.T) {
	const (
		corpus = 2048
		minSeg = 256 // → 8 sealed segments, real cross-segment fan-out
	)
	docs := fieldDocs(corpus)

	eng := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     minSeg,
		DeletesPctAllowed:  2.0,     // never auto-merge during the test
		SegmentCountTarget: 1 << 30, // never auto-merge during the test
	})
	defer eng.Close()

	for i := 0; i < corpus; i += minSeg {
		end := min(i+minSeg, corpus)
		require.NoError(t, eng.Add(docs[i:end]), "Add[%d:%d]", i, end)
	}
	require.GreaterOrEqual(t, eng.Metrics().SegmentCount, 4, "want >= 4 segments for real fan-out")

	// Cross-segment exact-match: a unique-term query recovers exactly its doc as the
	// top hit (BM25 is exact; the unique term appears in exactly one doc).
	for _, i := range []int{0, 137, 1000, 2047} {
		hits := eng.Search(NewQuery(fmt.Sprintf("uniqueterm%d", i)), 5)
		require.NotEmpty(t, hits, "uniqueterm%d returned no hits", i)
		require.Equal(t, fmt.Sprintf("n%d", i), hits[0].ID, "uniqueterm%d top hit", i)
	}

	// Delete a member; its exact-match query must no longer return it (liveDocs).
	dead := docs[1000]
	eng.Delete(dead.ID)
	afterDel := eng.Search(NewQuery("uniqueterm1000"), 5)
	require.False(t, containsID(afterDel, dead.ID), "deleted id still returned by Search")

	// Export all segments, Import into a fresh engine seeding the deleted id as a
	// tombstone, and assert the same live behavior.
	blobs := eng.Export()
	require.GreaterOrEqual(t, len(blobs), 4, "Export returned too few blobs")

	fresh := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     minSeg,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	})
	defer fresh.Close()
	require.NoError(t, fresh.Import(blobs, []searchengine.ExternalID{dead.ID}))

	// Live docs still resolve to themselves in the imported engine.
	for _, i := range []int{0, 137, 2047} {
		hits := fresh.Search(NewQuery(fmt.Sprintf("uniqueterm%d", i)), 5)
		require.NotEmpty(t, hits, "imported: uniqueterm%d returned no hits", i)
		require.Equal(t, fmt.Sprintf("n%d", i), hits[0].ID, "imported uniqueterm%d top hit", i)
	}
	// The tombstoned doc must not appear in the imported engine.
	require.False(t, containsID(fresh.Search(NewQuery("uniqueterm1000"), 5), dead.ID),
		"tombstoned id returned by imported engine")

	// Force a Merge via format.Merge directly (same call the engine's background
	// merger makes), consolidating all segments into one all-live segment, and
	// assert it still scores correctly under recomputed AggregateStats.
	assertMergeConsolidation(t, blobs, dead.ID)
}

// assertMergeConsolidation decodes the exported blobs, merges them with an accept
// filter that drops the dead id, and asserts the consolidated segment scores
// correctly under AggregateStats recomputed over the single merged segment.
func assertMergeConsolidation(t *testing.T, blobs []searchengine.SegmentBlob, deadID string) {
	t.Helper()
	f := Format{}
	segs := make([]searchengine.Segment[Query, *CorpusStats], 0, len(blobs))
	accept := make([]func(searchengine.ExternalID) bool, 0, len(blobs))
	for _, b := range blobs {
		seg, err := f.Decode(b.Bytes)
		require.NoError(t, err)
		segs = append(segs, seg)
		accept = append(accept, func(id searchengine.ExternalID) bool { return id != deadID })
	}
	merged, err := mergeSegments(t, segs, accept)
	require.NoError(t, err)

	mergedStats := f.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{merged})
	// The dead id is gone from the merged member set.
	require.NotContains(t, merged.IDs(), deadID, "merged segment must exclude the dropped id")

	// A unique-term query still scores its live doc as the top hit under the
	// recomputed corpus-global stats.
	hits := merged.Search(NewQuery("uniqueterm137"), mergedStats, 5, nil)
	require.NotEmpty(t, hits)
	require.Equal(t, "n137", hits[0].ID, "merged segment top hit under recomputed stats")
	require.False(t, containsID(merged.Search(NewQuery("uniqueterm1000"), mergedStats, 5, nil), deadID))
}
