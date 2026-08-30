// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// bucketCorpusVec returns a deterministic 32-byte vector for one corpus id. The
// seed is folded in so the RE-EMBEDDED generation of a document is byte-distinct
// from its original, which is what lets the assertions tell a fresh copy from a
// stale one.
func bucketCorpusVec(i int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, uint64(i)+1))
	v := make([]byte, 32)
	for j := range v {
		v[j] = byte(rng.UintN(256))
	}
	return v
}

// TestEmbedShipLandsInBucket is the SEAL-BEFORE-KILL catcher for the embed
// writeback seam: it asserts that a document written through the seam and then
// consolidated by a reconcile tick ends up with exactly ONE live copy carrying its
// FRESH vector, and that the copy is still reachable by search.
//
// WHICH CALL WOULD HAVE TO BE WRONG. The seam under test is shipEmbedHNSW's write
// into the segment manager, and the ordering contract that write delegates to:
// capture, SEAL, then supersede. Reordering it to kill the bucket copy BEFORE the
// tail is sealed opens a window in which a re-embedded document has NO live copy,
// and this test fails on the VectorByID / search assertions when it does. It is
// not a fan-out bound and says nothing about how many segments exist.
//
// FIXTURE SIZE IS LOAD-BEARING. The corpus is held clear of a power-of-two
// partition-count straddle: the count is derived from corpus size, so a corpus
// sitting on a boundary re-partitions wholesale on the next tick and the resulting
// membership churn would swamp the one-live-copy signal this test exists to read.
// 20480 documents plus a 100-document re-embed stays inside one count.
//
// The bulk corpus is seeded directly on the manager because it is FIXTURE, not
// subject; the re-embedded generation — the part the ordering contract governs —
// is driven through the real embed worker.
func TestEmbedShipLandsInBucket(t *testing.T) {
	ctx := context.Background()
	gt, graphName := kgtypes.GraphCode, "bucketRepo"

	const (
		corpusN  = 20480
		reEmbedN = 100
	)

	mgr := segmentdist.NewManager(t.TempDir(), 0)
	// Every graph this manager touches lazily constructs an engine, and every engine
	// starts a merger goroutine that only Close stops — segmentdist's own
	// TestManagerCloseStopsEveryEngineMerger pins exactly that. Without this the
	// merger outlives the test on a mergeTickInterval ticker for the rest of the test
	// BINARY, which is invisible per-test and is what this package's goleak gate
	// caught.
	t.Cleanup(mgr.Close)

	// FIXTURE: the prior corpus, written and consolidated the way the production
	// path does it — every batch force-sealed, durability taken by the tick.
	corpus := make([]searchengine.Document, corpusN)
	for i := range corpus {
		corpus[i] = searchengine.Document{
			ID:     fmt.Sprintf("bkt-n%d", i),
			Vector: bucketCorpusVec(i, 0xC0FFEE),
		}
	}
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, graphName, corpus))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, graphName))
	require.Equal(t, corpusN, mgr.ResidentDocCount(gt, graphName),
		"the seeded corpus is resident exactly once per document")

	// SUBJECT: re-embed a slice of the corpus through the real embed writeback seam
	// with byte-DISTINCT vectors, so every one of these ids now has an older copy
	// living in a consolidated partition and a newer one arriving at the seam.
	be := newFakeWireClient()
	fresh := make(map[string][]byte, reEmbedN)
	batch := make([]EmbedWork, 0, reEmbedN)
	for i := range reEmbedN {
		id := fmt.Sprintf("bkt-n%d", i)
		fresh[id] = bucketCorpusVec(i, 0xFEEDFACE)
		batch = append(batch, EmbedWork{
			GraphType: gt, GraphName: graphName, NodeID: id, EmbedText: id, Backend: be,
		})
	}
	fe := &fakeEmbedder{vectors: fresh}
	p := New(Config{}, be, nil, fe.call)
	p.AttachSegmentManager(mgr)

	runEmbedWorkerBatch(ctx, p, batch)
	require.Equal(t, int64(reEmbedN), p.Metrics().EmbedSucceeded, "every re-embed landed at the seam")

	// The drain itself must not have shipped; durability is the tick's job.
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, graphName))

	// ONE LIVE COPY PER DOCUMENT. A re-embed that superseded nothing would leave the
	// stale copy resident alongside the fresh one and drive this above corpusN; a
	// kill-before-seal would drop copies and drive it below.
	require.Equal(t, corpusN, mgr.ResidentDocCount(gt, graphName),
		"after the tick every id still has exactly one live copy — no duplicate, no hole")

	// THE FRESH VECTOR WON. Reading each re-embedded id back must yield the NEW
	// bytes: the stale partition copy is dead, not merely outranked.
	for id, want := range fresh {
		got, ok, err := mgr.VectorByID(ctx, gt, graphName, id)
		require.NoError(t, err)
		require.Truef(t, ok, "re-embedded id %s must still resolve to a live vector", id)
		require.Equalf(t, want, got, "re-embedded id %s must carry its FRESH vector", id)
	}

	// STILL SEARCHABLE, AND ONCE. Querying with a re-embedded document's own fresh
	// vector must recover that document, and it must occupy at most one slot of that
	// query's result — two slots would mean two live copies in two segments.
	for id, vec := range fresh {
		hits, err := mgr.Search(ctx, gt, graphName, "", vec, 16)
		require.NoError(t, err)
		slots := 0
		found := false
		for _, h := range hits {
			if h.ID == id {
				slots++
				found = true
			}
		}
		require.Truef(t, found, "re-embedded id %s must still be RETURNED BY SEARCH for its own vector", id)
		require.LessOrEqualf(t, slots, 1, "re-embedded id %s occupies %d slots of one query — duplicate live copies", id, slots)
	}
}
