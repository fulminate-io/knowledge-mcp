// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// collector_bm25_crossgraph_test.go asserts the CORPUS relationship between the
// retired producer and the new one: the BM25 arm must end up with the same document
// set the embed-axis ship would have produced once everything is embedded, and a
// LARGER one before that — never a smaller one.
//
// WHAT MAKES A SMALLER SET THE REAL HAZARD. Every other gate in this ticket judges
// the arm's plumbing — that it drains, ships, advances, and holds on failure. None
// of them can see the arm shipping a CORRECT-looking but SHORT corpus, which is what
// a re-introduced eligibility narrowing would produce: the segments would be valid,
// the cursor would advance, and search would simply return fewer results forever.
//
// WHERE THE VECTOR-INDEPENDENCE ITSELF IS PROVEN. Not here. This package sees only
// the wire page; whether the SERVER declines to compose an unvectored node is a
// server-side fact and is asserted where that decision lives, in store's
// TestCorpusDeltaBM25Item_AdmitsUnvectoredNodesTheRebuildAxisExcludes, which
// contrasts the feed's composer against the vector-gated segment_rebuild axis over
// the same two nodes. This file assumes that page shape and asserts what the CLIENT
// does with it; the pairing is deliberate and neither half stands alone.

// The fixture's node ids and its EXTERNAL cardinality expectations. The counts are
// hand-written from the fixture rather than derived from either producer's output —
// two sets that both lost the same members are still equal, so a set-equality
// assertion alone cannot tell a healthy corpus from a hollowed one.
const (
	xgVectoredA   = "pkg/a.go:Alpha"
	xgVectoredB   = "pkg/b.go:Beta"
	xgNotYetEmbed = "pkg/c.go:Gamma"

	// The embed axis shipped a BM25 document only for a node that got a vector in
	// that tick, so before Gamma embeds its corpus is the two vectored nodes.
	xgOldPathDocsBeforeGammaEmbeds = 2
	// The feed is not vector-gated, so the arm's corpus is every embed-eligible
	// node in the fixture — all three — from the first drain.
	xgArmDocs = 3
	// Once Gamma embeds, the old producer would have caught up to the same three.
	xgOldPathDocsAtQuiescence = 3
)

// xgSegmentDocsFor builds the OLD producer's input for a given set of ids — the
// shape the retired embed-axis ship assembled from the EmbedWork it had just
// embedded. The retired function is deliberately NOT named: a tombstone comment
// naming a hard-cut function is itself the violation, and this file tripped that
// gate once already.
func xgSegmentDocsFor(ids ...string) []SegmentDoc {
	out := make([]SegmentDoc, 0, len(ids))
	for _, id := range ids {
		out = append(out, SegmentDoc{NodeID: id, Fields: map[string]string{"summary": id + " summary"}})
	}
	return out
}

func xgDocIDs(docs []searchengine.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	sort.Strings(out)
	return out
}

// TestBM25Arm_CorpusEquivalenceAtQuiescence drives the REAL arm over a real page
// and compares its shipped document set against the real old-path builder's.
//
// BOTH SIDES RUN THEIR PRODUCTION BUILDER. The arm side goes through drainBM25 →
// partitionBM25Page → the ship closure, and the ship closure builds and round-trips
// an actual BM25 segment (fakeShipManager.AddAndMarkDirtyFields encodes and decodes
// one), so the ids compared are the ids a shipped segment would carry. The old side
// runs BuildBM25Documents, the same exported builder the retired ship called and the
// rebuild driver still calls. Comparing two hand-written lists would have proven
// nothing about either.
func TestBM25Arm_CorpusEquivalenceAtQuiescence(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "xgrepo"

	fsm := &fakeShipManager{}
	c := bm25TestCollector(fsm, gt, name, func() (int64, bool) { return 9_999, true })
	scanner := &fakeCorpusScanner{pages: []*knowledgev1.CorpusDeltaResponse{
		bm25Page([]string{xgVectoredA, xgVectoredB, xgNotYetEmbed}, nil),
	}}

	shipped, err := c.drainBM25(ctx, scanner, nil)
	require.NoError(t, err)
	require.Equal(t, xgArmDocs, shipped,
		"CARDINALITY GUARD against a fixture-derived constant: the arm must ship all %d "+
			"embed-eligible rows the page carried", xgArmDocs)

	armIDs := xgDocIDs(fsm.fieldDocs)
	require.Len(t, armIDs, xgArmDocs)
	require.Equal(t, armIDs, fsm.bm25DecodedID,
		"and the ids must survive a real segment build/encode/decode, not just the input slice")

	t.Run("before_gamma_embeds_the_arm_set_is_a_strict_superset", func(t *testing.T) {
		old := BuildBM25Documents(xgSegmentDocsFor(xgVectoredA, xgVectoredB))
		oldIDs := xgDocIDs(old)
		require.Len(t, oldIDs, xgOldPathDocsBeforeGammaEmbeds,
			"CARDINALITY GUARD on the OTHER side too — asserting one set's length against "+
				"the other's would let both hollow out together and still read equal")

		for _, id := range oldIDs {
			assert.Contains(t, armIDs, id,
				"the arm must never LOSE a document the embed axis produced — %q", id)
		}
		assert.NotContains(t, oldIDs, xgNotYetEmbed,
			"the old producer could not have shipped a node that had not embedded yet; if it "+
				"could, this fixture models nothing and the superset below is not a difference")
		assert.Contains(t, armIDs, xgNotYetEmbed,
			"THE DECOUPLING ITSELF: the not-yet-embedded node is in the keyword corpus NOW "+
				"rather than whenever its embedding happens to land")
		assert.Len(t, armIDs, len(oldIDs)+1,
			"and the difference is exactly that one node — a WIDER difference would mean the "+
				"feed admitted rows the BM25 corpus never had, which is a widening rather than "+
				"a decoupling")
	})

	t.Run("at_quiescence_the_two_sets_are_equal", func(t *testing.T) {
		// Gamma has embedded, so the old producer would have caught up.
		old := BuildBM25Documents(xgSegmentDocsFor(xgVectoredA, xgVectoredB, xgNotYetEmbed))
		oldIDs := xgDocIDs(old)
		require.Len(t, oldIDs, xgOldPathDocsAtQuiescence,
			"CARDINALITY GUARD, fixture-derived, on the converged old set")
		assert.Equal(t, oldIDs, armIDs,
			"at quiescence the decoupled producer's corpus is the SAME corpus — this ticket "+
				"changes WHEN a keyword document is produced, never WHICH nodes have one")
	})
}
