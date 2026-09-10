// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_search_accepting_kslot_test.go — the accept predicate is applied
// DURING top-k collection, and this file is the only thing in the tree that can
// tell that apart from a trim of the results.
//
// WHY IT HAD TO BE WRITTEN AGAINST THE REAL ENGINES. Every other test that looks
// like it covers hub scoping runs against a double that filters the hits it was
// handed. A post-rank trim and a during-rank accept produce identical output
// through such a double, so the property requirement 3 is built on — "a small
// hub returns its full top-N", because a rejected candidate consumes no slot in
// k — was asserted nowhere and both halves of the predicate could be replaced by
// nil with the whole suite still green.
//
// THE FIXTURE IS BUILT SO THE TWO ANSWERS DIFFER MAXIMALLY. The hub's three
// members are the WORST-ranked documents in the corpus on both arms, and k is
// larger than the hub but far smaller than the corpus. A trim of an unfiltered
// top-k therefore returns ZERO hub members; an accept applied inside collection
// returns all three. There is no implementation that passes this by accident.

const (
	// kslotCorpus stays under the HNSW beam width (defaultEfSearch = 50) so the
	// vector arm's candidate pool provably reaches the far documents. This test
	// is about which candidates consume a slot in k, not about how deep an
	// approximate beam searches, and a corpus wider than the beam would confound
	// the two.
	kslotCorpus  = 48
	kslotHubSize = 3
	kslotK       = 10
	// kslotMatching is how many NON-HUB documents carry the query term. The rest
	// of the corpus deliberately does not: a term present in every document has
	// an inverse document frequency of zero and BM25 scores the whole corpus at
	// nothing, which is a ranking with no order for the hub to sit at the bottom
	// of. Measured, not reasoned — the first draft made the term universal and
	// the BM25 arm returned zero hits for it.
	kslotMatching = 25
	kslotTerm     = "kslotcommon"
)

// kslotHubID names the hub members, which are the last kslotHubSize documents.
func kslotHubID(j int) string { return fmt.Sprintf("hub-%02d", j) }

// kslotDocs builds the corpus: kslotCorpus-kslotHubSize LOUD non-members that
// win on both arms, and kslotHubSize hub members that lose on both.
//
// LOSING ON BOTH ARMS IS THE POINT. A hub that lost only on BM25 would leave the
// vector arm's predicate untested, and the RRF fusion blends the two — so a
// predicate reaching one arm and not the other would still surface the hub
// through the arm that got it. Both arms must rank the hub out of the top-k for
// the fused assertion to mean the predicate reached both.
// The slice is preallocated to kslotCorpus rather than grown from nil so its
// SIZE RESOLVES TO A CONSTANT. This package's measurement census decides whether
// a test belongs behind the opt-in measurement gate by the corpus size it hands
// an HNSW leg, and a size it cannot bound is a membership it cannot decide — it
// fails the census rather than guessing. The cap is that bound.
func kslotDocs() []searchengine.Document {
	docs := make([]searchengine.Document, 0, kslotCorpus)
	loud := kslotCorpus - kslotHubSize

	for i := range loud {
		// ONE BIT SET, so every non-member sits at Hamming distance 1 from the
		// all-zero query vector while remaining distinct from its siblings.
		vec := make([]byte, 32)
		vec[i/8] |= 1 << (i % 8)
		// The first kslotMatching carry the term five times in a short document:
		// high term frequency, low length normalization, top of the ranking. The
		// remainder carry an unrelated term so the corpus has documents the query
		// does NOT match and the term keeps a non-zero inverse document
		// frequency; on the vector arm they are non-members exactly like the
		// others.
		body := strings.TrimSpace(strings.Repeat(kslotTerm+" ", 5))
		if i >= kslotMatching {
			body = "unrelated corpus filler"
		}
		docs = append(docs, searchengine.Document{
			ID:     fmt.Sprintf("loud-%02d", i),
			Vector: vec,
			Fields: map[string]string{searchengine.FieldContent: body},
		})
	}

	filler := strings.TrimSpace(strings.Repeat("padding ", 60))
	for j := range kslotHubSize {
		// TWENTY BYTES SET: Hamming distance 160, the far end of the corpus.
		vec := make([]byte, 32)
		for b := range 20 {
			vec[b] = 0xFF
		}
		vec[31] = byte(j + 1) // distinct vectors, all equally far
		docs = append(docs, searchengine.Document{
			ID:     kslotHubID(j),
			Vector: vec,
			// One occurrence in a long document: the bottom of the BM25 ranking.
			Fields: map[string]string{searchengine.FieldContent: kslotTerm + " " + filler},
		})
	}
	return docs
}

// kslotHubMembership is the hub's member set, built from the same ids kslotDocs
// assigns. It is a SEPARATE function from the corpus builder so the corpus
// builder returns one value whose size the measurement census can resolve.
func kslotHubMembership() map[searchengine.ExternalID]bool {
	hub := make(map[searchengine.ExternalID]bool, kslotHubSize)
	for j := range kslotHubSize {
		hub[kslotHubID(j)] = true
	}
	return hub
}

// kslotIDs projects hits to their ids, in rank order.
func kslotIDs(hits []searchengine.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}

// TestManagerSearchAccepting_HubMembersSurviveBelowK is requirement 3c, on both
// arms and on their fusion.
func TestManagerSearchAccepting_HubMembersSurviveBelowK(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphPractice, "default"

	docs, hub := kslotDocs(), kslotHubMembership()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, docs))
	// BOTH POOLS, because the fusion is what is under test. The vector engine and
	// the field engine are re-emitted through separate entry points, and seeding
	// only the first leaves the BM25 arm empty — which reads as "the predicate
	// filtered everything" rather than as "there was nothing to filter".
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs))

	accept := func(id searchengine.ExternalID) bool { return hub[id] }

	// The all-zero query vector every non-member sits one bit away from.
	queryVec := make([]byte, 32)

	for _, arm := range []struct {
		name string
		text string
		vec  []byte
	}{
		// BM25 ALONE: an empty query vector skips the HNSW arm outright, so the
		// fused list is the BM25 ranking unchanged.
		{"bm25_only", kslotTerm, nil},
		// HNSW ALONE: an empty query text matches nothing in BM25, so the fused
		// list is the vector ranking unchanged.
		{"hnsw_only", "", queryVec},
		// BOTH, FUSED: the assertion that the two halves of the RRF agree about
		// which documents exist. A predicate given to one arm only blends a
		// filtered ranking with an unfiltered one.
		{"rrf_fused", kslotTerm, queryVec},
	} {
		t.Run(arm.name, func(t *testing.T) {
			// THE PRECONDITION IS AN ASSERTION, not a comment. If the fixture ever
			// stops ranking the hub below k this test would pass vacuously — a
			// trim and an accept agree when the hub is in the top-k anyway.
			unfiltered, err := mgr.Search(ctx, gt, name, arm.text, arm.vec, kslotK)
			require.NoError(t, err)
			require.Len(t, unfiltered, kslotK,
				"the unscoped search must fill k from a corpus of %d", kslotCorpus)
			for _, id := range kslotIDs(unfiltered) {
				require.False(t, hub[id],
					"fixture precondition: no hub member may rank inside the unscoped top-%d, or a post-rank trim would pass this test too (got %v)",
					kslotK, kslotIDs(unfiltered))
			}

			// THE PROPERTY. Every hub member comes back even though every one of
			// them ranks below k in the corpus-wide ordering: a rejected candidate
			// consumed no slot.
			scoped, err := mgr.SearchAccepting(ctx, gt, name, arm.text, arm.vec, kslotK, accept)
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{kslotHubID(0), kslotHubID(1), kslotHubID(2)}, kslotIDs(scoped),
				"a hub smaller than k returns its FULL membership; a post-rank trim of the top-%d returns none of it", kslotK)
		})
	}

	// THE NEGATIVE CONTROL. A predicate that admits nothing returns nothing, so
	// the rows above are the predicate being CONSULTED rather than the engine
	// returning whatever it likes.
	t.Run("control_reject_everything", func(t *testing.T) {
		none, err := mgr.SearchAccepting(ctx, gt, name, kslotTerm, queryVec, kslotK,
			func(searchengine.ExternalID) bool { return false })
		require.NoError(t, err)
		assert.Empty(t, none, "a predicate that accepts nothing must return nothing")
	})

	// AND THE NIL CONTROL: a nil predicate is exactly Search, so the k-slot
	// machinery cannot have narrowed the unscoped path.
	t.Run("control_nil_predicate_is_search", func(t *testing.T) {
		nilAccept, err := mgr.SearchAccepting(ctx, gt, name, kslotTerm, queryVec, kslotK, nil)
		require.NoError(t, err)
		plain, err := mgr.Search(ctx, gt, name, kslotTerm, queryVec, kslotK)
		require.NoError(t, err)
		assert.Equal(t, kslotIDs(plain), kslotIDs(nilAccept),
			"a nil predicate must leave the ranking byte-for-byte what Search returns")
	})
}
