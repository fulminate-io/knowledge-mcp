// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// publishedHNSWDigests snapshots the content hashes in the graph's PUBLISHED hnsw
// The on-disk content-hash ids — the routed bucket blobs as they are stored. A
// DURABLE observable on purpose: a delete that only cleared an in-memory live bit
// leaves this set byte-identical, and that is precisely the gap this criterion
// exists to close.

// searchHitsK bounds every probe in these fixtures: wide enough that a document
// losing its slot is a real absence rather than a ranking accident, and every caller
// wants the same window — so it is a constant rather than a parameter.
const searchHitsK = 64

// searchHits runs the graph's vector search and returns the ranked ids.
func searchHits(
	t *testing.T, c *client, ctx context.Context, repo string, vec []byte,
) []searchengine.Hit {
	t.Helper()
	hits, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, repo, "", vec, searchHitsK)
	require.NoError(t, err)
	return hits
}

// hitsContain reports whether id holds one of the returned rank slots.
func hitsContain(hits []searchengine.Hit, id searchengine.ExternalID) bool {
	for _, h := range hits {
		if h.ID == id {
			return true
		}
	}
	return false
}

// TestCollectorDeleteReachesPoolWithinOneReconcile asserts a
// COLLECTOR-originated delete reaches the local pool within ONE reconcile interval,
// with no human issuing a rebuild.
//
// WHAT WAS BROKEN. The server-side tombstone feed had exactly one reader and
// SetGraphTombstones exactly one writer, both inside the manually-invoked rebuild
// driver. A delete this daemon did not perform itself therefore reached neither the
// routed buckets nor the import seed until an operator ran manage(rebuild_segments):
// the routed bucket blob stayed byte-identical past the full delta window while the
// dead document kept competing for — and discarding — a top-k slot.
//
// THE FIXTURE GRAPH IS DELIBERATELY HEALTHY. The per-graph loop skips a healthy
// graph with a `continue`, so a consumer placed after the degeneracy probe would
// never run for the ordinary graph this models, and a degenerate fixture would hide
// that by reaching the misplaced call. It also keeps the rebuild out of the picture
// entirely: nothing here may depend on a rebuild firing, because the whole point is
// that no rebuild is needed.
//
// THE DELETE ARRIVES ONLY ON THE FEED. Nothing in this test touches the local delete
// surface — the daemon learns about it exactly the way it learns about a delete
// performed by a collect on another machine, or by a collect whose only change was a
// removal.
//
// THE GRAPH IS SEEDED WITH A MERGE HORIZON, which is a PRECONDITION rather than part
// of the subject. A graph with no horizon of any kind pulls no delta window at all —
// a zero-watermark read of that axis is the whole vectored corpus — so the seed puts
// the fixture in the state every graph reaches within one backstop rotation, which is
// where this contract applies. The no-horizon-no-pull rule itself is gated separately
// by TestMergeSkippedUntilWatermarkSeeded.
func TestCollectorDeleteReachesPoolWithinOneReconcile(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "deltaConsumerRepo"
		corpusN  = 128 // clears the resident backstop floor
		embedded = 100 // resident 128 >= 0.5*100, so the graph is HEALTHY
		// A horizon an earlier landed merge left behind, well before the window this
		// test feeds — so the pull happens and its window still contains the delete.
		seededHorizon = int64(1_600_000_000_000_000_000)
	)

	c, eng, dir := buildReconcileClientWithDir(t, embedded, repo)
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seededHorizon))

	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
	// BOTH FORMATS ARE SEEDED, AND THE FIELD ONE IS A PRECONDITION RATHER THAN
	// SYMMETRY. A delete's vector partition is re-emitted by the drain rather than by
	// the delete, and the drain declines to serve that work unless BOTH pools hold a
	// corpus clearing the residency floor — a count derived from a near-empty pool
	// collapses every masked id onto one partition. A vector-only fixture would
	// therefore never re-emit, and the digest assertion below would be reporting the
	// fixture's own asymmetry rather than the delete path.
	//
	// THE FIELD NAME MATTERS. The BM25 format indexes a FIXED vocabulary, so a document
	// whose only field is outside it carries no terms and leaves the field pool empty —
	// which is the same below-the-floor state as seeding nothing at all.
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, repo, fieldDocsOf(docs)))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	degenerate := armIsDegenerate(t, c.segmentMgr, kgtypes.GraphCode, repo, embedded)
	require.False(t, degenerate,
		"PRECONDITION: the fixture graph must be HEALTHY, so the pass reaches the healthy-graph continue and no rebuild can do this work instead")

	victim, survivor := docs[0], docs[1]
	require.True(t, hitsContain(searchHits(t, c, ctx, repo, victim.Vector), victim.ID),
		"PRECONDITION: the victim must be searchable before the delete, or its absence afterwards means nothing")

	before := l2SegmentIDs(t, dir, repo, hnsw.New().Name())
	require.NotEmpty(t, before, "PRECONDITION: the corpus must be published as real routed-bucket blobs")

	// THE COLLECTOR-ORIGINATED DELETE, arriving only as a tombstone on the feed.
	eng.mu.Lock()
	eng.scanItems[repo] = []*knowledgev1.PipelineScanItem{
		{NodeId: victim.ID, GraphName: repo, Tombstoned: true},
	}
	eng.mu.Unlock()

	// ONE reconcile pass — one interval, no manual rebuild.
	c.reconcileSegmentCoverage(ctx)

	// It has to be the DELTA CONSUMER that carried this, not a rebuild doing the work
	// incidentally. Without these two legs the test would pass on a pass that healed
	// the graph by rebuilding it, which is the very thing the criterion says must not
	// be necessary.
	require.Zero(t, eng.scanCallCount(repo),
		"no REBUILD may fire for a healthy graph — the delete must reach the pool without one")
	require.Positive(t, eng.deltaScanCallCount(repo),
		"the bounded tombstone-delta read is what must have carried the delete")

	// THE RE-EMIT STILL LANDS IN THIS PASS, THOUGH NOT ON THE DELETE'S OWN GOROUTINE.
	// The delete kills the id's live bit and seals it into the durable tombstone mask;
	// the mask is the drain's work ledger, and the delta consume runs BEFORE the drain
	// in this same reconcile, so the drain that follows re-emits the partition the mask
	// names. A byte-identical digest set here means neither half happened.
	after := l2SegmentIDs(t, dir, repo, hnsw.New().Name())
	require.NotEqual(t, before, after,
		"the routed bucket must RE-EMIT: an unchanged published digest set means the blob is byte-identical and the delete never left this process's memory")

	// AND THE DELETE WAS DURABLE BEFORE THAT RE-EMIT RAN, which is what makes the
	// deferral safe rather than merely faster: the mask is written synchronously by the
	// delete, so a process that died between the two would still mask the id at import.
	// Here the drain has already discharged it, so the record is empty — asserting the
	// discharge is the observable half of "the mask carried it".
	_, masked, err := c.segmentMgr.LoadRebuildState(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.NotContains(t, masked, victim.ID,
		"the drain re-emitted this id's partition, so the mask must have discharged it — an id still "+
			"masked after its partition was published means the trim never fired and the drain will "+
			"re-offer the same partition forever")

	require.False(t, hitsContain(searchHits(t, c, ctx, repo, victim.Vector), victim.ID),
		"the deleted id must lose its rank slot within one reconcile interval, with no manual rebuild")
	require.True(t, hitsContain(searchHits(t, c, ctx, repo, survivor.Vector), survivor.ID),
		"a document nobody deleted must survive — without this leg an implementation that emptied the pool would pass")
}

// fieldDocsOf re-labels a vector fixture's documents into the BM25 format's own field
// vocabulary, so seeding them actually produces a field corpus.
func fieldDocsOf(docs []searchengine.Document) []searchengine.Document {
	out := make([]searchengine.Document, 0, len(docs))
	for _, d := range docs {
		out = append(out, searchengine.Document{
			ID:     d.ID,
			Vector: d.Vector,
			Fields: map[string]string{searchengine.FieldContent: "fixture content for " + d.ID},
		})
	}
	return out
}
