// SPDX-License-Identifier: Apache-2.0

// rebuild_layer_equivalence_test.go answers the one question the release has to answer
// before any memory reading matters: does a layer serving MAPPED bytes return exactly
// what the same layer serving the encoder's HEAP output returns, and does it keep
// returning it after the collector has run?
//
// BOTH REAL FORMATS, ONE SUBTEST EACH, and that is the file's reason for existing apart
// from its siblings. The release runs for bm25v2 and hnswv3 through one generic
// finalize, and both decode IN PLACE over the mapping — bm25's mappedSegment walks its
// postings, dictionaries and member table over the mapped bytes, hnsw's openGraphV3 is
// zero-copy over its vectors and neighbor lists — so the correctness claim and the
// use-after-unmap fault window are the same for each. A row over bm25 alone would be
// asserting the property for half the release.
//
// THE MOCK FORMAT IS DELIBERATELY ABSENT from this file: it decodes by COPYING its
// input, so a payload whose bytes were wrong or whose mapping was freed early answers
// correctly and every assertion here passes.

package segmentdist

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// TestReleasedRebuildLayerServesTheSameResultsAsAHeapBackedOne is the CORRECTNESS
// PROOF: the release changes where a published partition's bytes live and nothing
// else, so two engines over one corpus — one serving the encoder's heap output, one
// serving a mapping of the stored copy — must answer identically.
//
// IT RUNS BOTH REAL FORMATS, one subtest each, and the reason is the fault window
// rather than symmetry: the release runs for bm25v2 AND hnswv3, and hnsw's decoded
// graph reads its vectors and neighbor lists in place over the mapped bytes exactly as
// bm25's postings do (formats/hnsw/mapped.go openGraphV3). A row over bm25 alone leaves
// the vector arm's scoring path — a second in-place reader — unasserted.
//
// AND IT COMPARES AFTER TWO FORCED COLLECTIONS AS WELL AS BEFORE. The mapping's lifetime
// is a cleanup keyed on the winning entry's reachability, so an arm that compared only
// before a GC would pass against a release that had already handed the mapping away.
//
// THE MUTATION THAT REDS IT: install any payload other than a decode of the bytes that
// were written — a stale blob, a different partition's mapping, a truncated one — and
// the hit lists or the scores diverge; free the mapping instead of attaching it
// (attachBlobCleanup → releaseUnattached) and the post-GC half faults, on EITHER arm.
func TestReleasedRebuildLayerServesTheSameResultsAsAHeapBackedOne(t *testing.T) {
	t.Parallel()
	t.Run(bm25FormatName, func(t *testing.T) {
		t.Parallel()
		docs := bm25ReleaseCorpus(64)
		queries := make([]bm25.Query, 0, 6)
		for _, term := range []string{"alpha", "beta", "gamma", "Symbol7", "Symbol41", "absent"} {
			queries = append(queries, bm25.NewQuery(term))
		}
		assertReleasedLayerMatchesHeapBacked(t, "equivalence-bm25", bm25ReleasePool, docs, queries)
	})
	t.Run(hnswFormatName, func(t *testing.T) {
		t.Parallel()
		docs := vecContentDocs(64)
		// FOUR QUERY VECTORS drawn from the corpus itself, spread across it so the
		// compared lists are built from every partition's neighbor graph rather than
		// from one.
		queries := [][]byte{docs[0].Vector, docs[7].Vector, docs[31].Vector, docs[63].Vector}
		assertReleasedLayerMatchesHeapBacked(t, "equivalence-hnsw", hnswReleasePool, docs, queries)
	})
}

// assertReleasedLayerMatchesHeapBacked is one format's arm: publish the same corpus
// twice — once through finalizeResetLayer, once through BuildLayer + ReplaceLayer with
// no hook — and require identical answers before and after two collections.
//
// IT IS GENERIC OVER THE POOL because the two live instantiations carry different type
// arguments, which is the same reason finalizeResetLayer itself is generic; a per-format
// copy of an assertion whose POINT is that the two formats behave alike is exactly where
// the two would drift.
func assertReleasedLayerMatchesHeapBacked[Q, S any](
	t *testing.T, name string,
	pool func(t *testing.T, name string) *distManager[Q, S],
	docs []searchengine.Document, queries []Q,
) {
	t.Helper()
	work := resetWorkForDocs(docs, 4)
	require.Greater(t, len(work), 1,
		"fixture control: the corpus must span more than one partition, or per-partition release is untested")
	require.NotEmpty(t, queries, "fixture control: an arm with no queries compares nothing")

	released := pool(t, name+"-released")
	_, swapped, err := finalizeResetLayer(t.Context(), kgtypes.GraphCode, name+"-released", released, work)
	require.NoError(t, err)
	require.True(t, swapped)
	require.Empty(t, released.engine.HeapBackedResidentIDs(),
		"control: the released arm must really be serving mappings, or this compares two identical engines")

	// THE REFERENCE ARM publishes the same layer through the engine primitives alone,
	// with no persist hook, so its payloads are the encoder's own heap output.
	reference := pool(t, name+"-heap")
	built, err := reference.engine.BuildLayer(work)
	require.NoError(t, err)
	_, _, err = reference.engine.ReplaceLayer(built)
	require.NoError(t, err)
	require.Len(t, reference.engine.HeapBackedResidentIDs(), reference.engine.ResidentSegmentCount(),
		"control: the reference arm must really be heap-backed")

	require.Equal(t, reference.engine.ResidentSegmentCount(), released.engine.ResidentSegmentCount(),
		"both arms publish one segment per partition of the same corpus")

	// k IS THE CORPUS SIZE, so every partition contributes to the compared list. A
	// default top-k would compare the head alone and pass over a partition whose
	// bytes the release had corrupted.
	compare := func(t *testing.T, when string) {
		t.Helper()
		for i, q := range queries {
			want := reference.engine.Search(q, len(docs))
			got := released.engine.Search(q, len(docs))
			require.Len(t, got, len(want), "%s: query %d must return the same number of hits", when, i)
			for h := range want {
				require.Equal(t, want[h].ID, got[h].ID,
					"%s: query %d hit %d must be the same document in the same position", when, i, h)
				require.InDelta(t, want[h].Score, got[h].Score, 0,
					"%s: query %d hit %s must score identically", when, i, want[h].ID)
			}
		}
		require.NotEmpty(t, released.engine.Search(queries[0], len(docs)),
			"%s control: the corpus answers, so the equality above is between two populated results", when)
	}

	compare(t, "before a collection")
	// THE FAULT WINDOW: a release handed over rather than attached to the winning entry
	// unmaps bytes the resident payload reads in place, and the comparison below faults
	// instead of failing.
	runtime.GC()
	runtime.GC()
	compare(t, "after two collections")
}

// TestReleasedRebuildLayerSurvivesAGC is the release-before-attach row for the RESET
// path, driven through BOTH real formats.
//
// THE FAULT WINDOW: the mapping a released partition reads is owned by a cleanup keyed
// on the ENTRY's reachability. A release handed over rather than attached to the entry
// the layer publishes unmaps bytes the resident payload reads in place, and the search
// below faults instead of failing — which is why this row runs the real formats, whose
// mapped payloads read their postings, dictionaries, vectors and neighbor lists over
// the mapped bytes, rather than the mock, which decodes by copying and would pass
// either way. hnswv3's openGraphV3 is zero-copy on the same terms as bm25v2's
// mappedSegment, so a bm25-only row would leave half the release unasserted.
func TestReleasedRebuildLayerSurvivesAGC(t *testing.T) {
	t.Parallel()
	t.Run(bm25FormatName, func(t *testing.T) {
		t.Parallel()
		docs := bm25ReleaseCorpus(64)
		queries := []bm25.Query{bm25.NewQuery("alpha"), bm25.NewQuery("beta"), bm25.NewQuery("gamma"), bm25.NewQuery("Symbol7")}
		assertReleasedLayerSurvivesAGC(t, "release-gc-bm25", bm25ReleasePool, docs, queries, len(docs))
	})
	t.Run(hnswFormatName, func(t *testing.T) {
		t.Parallel()
		docs := vecContentDocs(64)
		queries := [][]byte{docs[0].Vector, docs[7].Vector, docs[31].Vector, docs[63].Vector}
		assertReleasedLayerSurvivesAGC(t, "release-gc-hnsw", hnswReleasePool, docs, queries, len(docs))
	})
}

// assertReleasedLayerSurvivesAGC publishes one format's layer through the release and
// requires each query to answer IDENTICALLY — the same ids in the same order — across
// two forced collections.
//
// IT COMPARES AGAINST ITS OWN PRE-GC ANSWER rather than a count, because a count is
// satisfied by a payload that returned the right NUMBER of wrong documents, and the
// failure mode this row exists for reads memory that has been unmapped or reused.
func assertReleasedLayerSurvivesAGC[Q, S any](
	t *testing.T, name string,
	pool func(t *testing.T, name string) *distManager[Q, S],
	docs []searchengine.Document, queries []Q, wantFirst int,
) {
	t.Helper()
	dm := pool(t, name)

	_, swapped, err := finalizeResetLayer(t.Context(), kgtypes.GraphCode, name, dm, resetWorkForDocs(docs, 4))
	require.NoError(t, err)
	require.True(t, swapped)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"control: the layer must be mapping-backed, or the collections below free nothing this reads")

	// k IS THE CORPUS SIZE, so the hit list spans every partition: a row taking the
	// default top-k would read one partition's payload and say nothing about the rest.
	before := make([][]searchengine.ExternalID, 0, len(queries))
	for _, q := range queries {
		hits := dm.engine.Search(q, len(docs))
		ids := make([]searchengine.ExternalID, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		before = append(before, ids)
	}
	require.Len(t, before[0], wantFirst,
		"control: the first query must reach the whole corpus, or the comparison below is over a short list")

	runtime.GC()
	runtime.GC()

	for i, q := range queries {
		hits := dm.engine.Search(q, len(docs))
		ids := make([]searchengine.ExternalID, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		require.Equal(t, before[i], ids,
			"query %d: the mapped payloads must answer identically after two collections", i)
	}
}
