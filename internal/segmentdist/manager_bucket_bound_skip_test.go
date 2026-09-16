// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_skip_test.go — A PARTITION THE BOUND CANNOT CONSOLIDATE, for
// reasons that are not corruption.
//
// These two rows are the mechanisms the RC's corrupt segment EXPOSED but does not
// own: a consolidation can fail for a full disk or a refusing merge just as well,
// and each of the two defects below is a property of the walk and of the census
// latch rather than of why a partition failed. The corruption seam itself is the
// sibling file.
//
// The shared fixture helpers live here because these rows are the simpler drivers of
// them; the corruption file uses the same ones.

package segmentdist

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// corruptBuckets is the partition count every fixture in this file is evaluated
// under. Two is the smallest count that can tell "the failing partition was
// skipped" from "nothing was consolidated", which is the whole question here.
const corruptBuckets = 2

// bucketDoc builds a document whose id occupies the named partition at
// corruptBuckets, SEARCHING for the id rather than assuming a spread — BucketOf is
// a hash, and an assumed spread is the kind of fixture that goes vacuous when the
// hash changes (the same discipline spanningPair and soloBucketDoc state).
func bucketDoc(t *testing.T, bucket, seed int) searchengine.Document {
	t.Helper()
	for i := range 1000 {
		id := fmt.Sprintf("part%d-%05d-%04d", bucket, seed, i)
		if searchengine.BucketOf(id, corruptBuckets) == bucket {
			return doc(id, "alpha beta")
		}
	}
	t.Fatalf("bucketDoc(%d, %d): no id landed in partition %d of %d after 1000 tries", bucket, seed, bucket, corruptBuckets)
	return searchengine.Document{}
}

// sealSolo seals one single-document segment per id in the named partition and
// returns the ids it sealed. A one-document segment occupies exactly one partition
// at every count it will ever be read under, so every segment it produces is a
// candidate a per-partition swap can take.
func sealSolo(t *testing.T, engine *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats], bucket, from, n int) []searchengine.ExternalID {
	t.Helper()
	ids := make([]searchengine.ExternalID, 0, n)
	for i := range n {
		d := bucketDoc(t, bucket, from+i)
		_, err := engine.AddSealAndSupersede([]searchengine.Document{d})
		require.NoError(t, err, "PRECONDITION: the seals must succeed — only the MERGE is interfered with")
		ids = append(ids, d.ID)
	}
	return ids
}

// refusingBM25 is the real BM25 format with ONE behaviour changed: while it is
// ARMED its MergeTo refuses.
//
// IT REFUSES BY PARTITION, and the way it identifies one is the point. MergeTo is
// handed no bucket number — only the constituents and the per-constituent accept
// predicates — but those predicates ARE the partition: ReplaceBucket passes
// acceptLiveMembers(entry, bucket, bucketCount), which answers true only for a live
// member of that entry that belongs to the partition being rebuilt. Probing them
// with a marker id therefore asks the production predicate which partition this
// merge is for, rather than reaching around it for a bucket number the interface
// does not carry. A marker of "" refuses every partition, which is the regime the
// latch row needs.
type refusingBM25 struct {
	bm25.Format
	marker   searchengine.ExternalID
	armed    *atomic.Bool
	refusals *atomic.Int64
}

func (f *refusingBM25) MergeTo(
	dst searchengine.MergeSink,
	segs []searchengine.Segment[bm25.Query, *bm25.CorpusStats],
	accept []func(searchengine.ExternalID) bool,
) (int64, error) {
	if !f.armed.Load() || (f.marker != "" && !acceptsMarker(accept, f.marker)) {
		return f.Format.MergeTo(dst, segs, accept)
	}
	f.refusals.Add(1)
	return 0, errMergeRefused
}

// acceptsMarker reports whether any constituent of this merge accepts the marker
// id — which is true exactly when the merge is rebuilding the partition the marker
// belongs to, and the marker is live in a segment being consolidated.
func acceptsMarker(accept []func(searchengine.ExternalID) bool, marker searchengine.ExternalID) bool {
	for _, a := range accept {
		if a(marker) {
			return true
		}
	}
	return false
}

// TestResidentBoundSkipsAFailingPartitionAndConsolidatesTheRest is requirement 1,
// and the defect it pins is a RETURN where a CONTINUE belongs.
//
// The ordered walk is MOST-POPULATED FIRST, so a partition that cannot be
// consolidated only grows and therefore sorts first on every later crossing. With
// the failure abandoning the whole loop, that one partition prevented every other
// partition from ever being offered: the RC run logged 19 consecutive failures on
// bucket 52 with not one INFO line beside them, and the count climbed the whole
// time with 127 consolidatable partitions sitting resident.
func TestResidentBoundSkipsAFailingPartitionAndConsolidatesTheRest(t *testing.T) {
	// NOT PARALLEL, and the reason is the instrument rather than the fixture: these
	// rows read the ERROR and INFO lines the bound writes, which means replacing the
	// PROCESS-GLOBAL slog default for the duration of a bound call. A sibling running
	// beside them restores that default halfway through and the buffer then holds a
	// fraction of the run — measured here as a missing INFO line on a census that had
	// demonstrably consolidated. The package's other log-reading rows are serial for
	// the same reason.
	var armed atomic.Bool
	var refusals atomic.Int64
	armed.Store(true)
	format := &refusingBM25{armed: &armed, refusals: &refusals}
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](format, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "bound-skip"), bm25.New().Name())

	// The FAILING partition is the fuller one, so the sort puts it first and a
	// bound that abandons on failure reaches nothing else — the RC's own shape.
	const failingCount, healthyCount = 2600, 1600
	failing := sealSolo(t, engine, 0, 0, failingCount)
	sealSolo(t, engine, 1, 0, healthyCount)
	format.marker = failing[0]

	before := engine.ResidentSegmentCount()
	require.Greater(t, before, searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: the set must be over budget, or the bound returns at the crossing gate")
	candidates := consolidatablePartitions(engine.SegmentSpans(corruptBuckets))
	require.Len(t, candidates, corruptBuckets,
		"PRECONDITION: both partitions must be consolidatable, or 'the rest' is an empty set")
	require.Greater(t, len(candidates[0]), len(candidates[1]),
		"PRECONDITION: the FAILING partition must be the fuller one, so the most-populated-first sort offers it first")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	boundResidentSegments(dm, corruptBuckets)
	slog.SetDefault(prev)
	log := buf.String()

	after := engine.ResidentSegmentCount()
	t.Logf("skip row: %d -> %d resident (failing partition %d segments, healthy %d), refusals %d",
		before, after, len(candidates[0]), len(candidates[1]), refusals.Load())

	require.Less(t, after, before,
		"the census must CONTINUE past the partition it could not consolidate: one unconsolidatable partition "+
			"sorts first forever (most-populated-first) and abandoning the walk there leaves every other partition "+
			"resident — 19 consecutive failures on one bucket with no consolidation beside them is what this cost")
	require.Contains(t, log, "partitions_consolidated=1",
		"and the healthy partition must be the one it took")
	require.Equal(t, 1, strings.Count(log, "a resident-bound consolidation failed"),
		"the failure is still reported at ERROR, ONCE per failing partition per census — silence would leave an "+
			"engine over budget with nothing to say why, and a line per retry would flood the log")
	require.Contains(t, log, "bucket=0",
		"and the line must name the partition, because a merge that refuses is a property of ITS constituents")
	require.Len(t, engine.BucketConstituents(0, corruptBuckets), failingCount,
		"the skipped partition's constituents are untouched: a failed ReplaceBucket publishes nothing and removes nothing")
}

// TestResidentBoundLatchNeverRatchetsPastBudgetPlusSlack is requirement 2.
//
// THE LATCH WAS A RATCHET. A census that took nothing armed lastCensusCount at the
// count it OBSERVED, and the gate then declined to census until the set had grown
// by a slack beyond THAT — so every stuck crossing raised the count the bound would
// next act at by a whole slack, without limit. On the RC that was 19 crossings at
// 1,024 apiece: a resident peak of 22,474 against a budget of 4,096, ended by a
// rebuild rather than by the bound.
//
// WHAT THE ROW DRIVES is the regime and then its END: the merges are refused for
// several crossings, and then they are allowed again and a HANDFUL of new segments
// arrive — fewer than one slack. A latch armed at the ratcheted count declines to
// census there and leaves a consolidatable partition resident at a count far over
// the ceiling; a latch that can never be armed above the budget censuses and takes
// it.
func TestResidentBoundLatchNeverRatchetsPastBudgetPlusSlack(t *testing.T) {
	// NOT PARALLEL, and the reason is the instrument rather than the fixture: these
	// rows read the ERROR and INFO lines the bound writes, which means replacing the
	// PROCESS-GLOBAL slog default for the duration of a bound call. A sibling running
	// beside them restores that default halfway through and the buffer then holds a
	// fraction of the run — measured here as a missing INFO line on a census that had
	// demonstrably consolidated. The package's other log-reading rows are serial for
	// the same reason.
	var armed atomic.Bool
	var refusals atomic.Int64
	armed.Store(true)
	// No marker: every partition's merge is refused, which is the stuck regime the
	// latch itself decides — the skip row above is what covers a PARTIAL failure.
	format := &refusingBM25{armed: &armed, refusals: &refusals}
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](format, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "bound-latch"), bm25.New().Name())

	budget := searchengine.ResidentSegmentFanoutBudget
	lowWater, slack := residentLowWater(corruptBuckets)
	const stuckCrossings = 5
	sealSolo(t, engine, 0, 0, budget+1)

	for crossing := range stuckCrossings {
		if crossing > 0 {
			// Exactly one slack of growth, which is what re-arms the gate on EITHER
			// rule — so the row turns on where the latch is armed rather than on
			// whether the census was paid.
			sealSolo(t, engine, 0, 1_000_000+crossing*slack, slack)
		}
		observed := engine.ResidentSegmentCount()
		require.Greater(t, observed, budget, "PRECONDITION: crossing %d must be over budget", crossing+1)
		boundResidentSegments(dm, corruptBuckets)
		require.Equal(t, observed, engine.ResidentSegmentCount(),
			"PRECONDITION: crossing %d consolidated nothing — the regime this row is about", crossing+1)
		require.LessOrEqual(t, dm.lastCensusCount.Load(), int64(budget),
			"the census latch was armed at %d after crossing %d, which is ABOVE the budget of %d: the gate admits "+
				"last+slack without censusing, so an arming point that follows the observed count is a RATCHET and the "+
				"admitted count grows by a slack per crossing with no ceiling at all",
			dm.lastCensusCount.Load(), crossing+1, budget)
	}

	// THE REGIME ENDS, and the count arrives only a little past the ceiling: fewer
	// new segments than one slack, so growth ALONE cannot re-arm a latch that was
	// armed at the ratcheted count.
	armed.Store(false)
	const trickle = 4
	require.Less(t, trickle, slack,
		"PRECONDITION: the trickle must be smaller than the slack (%d), or the gate re-arms on growth and this row is "+
			"green whatever the latch did", slack)
	sealSolo(t, engine, 0, 2_000_000, trickle)
	observed := engine.ResidentSegmentCount()
	require.NotEmpty(t, consolidatablePartitions(engine.SegmentSpans(corruptBuckets)),
		"PRECONDITION: a partition a per-partition swap can reduce is resident, so a census would find work")

	boundResidentSegments(dm, corruptBuckets)

	sustained := engine.ResidentSegmentCount()
	t.Logf("latch row: %d stuck crossings, observed %d -> sustained %d (budget=%d, low_water=%d, slack=%d, ceiling=%d, refusals=%d)",
		stuckCrossings, observed, sustained, budget, lowWater, slack, budget+slack, refusals.Load())
	require.LessOrEqual(t, sustained, budget+slack,
		"the SUSTAINED resident count was %d against an absolute ceiling of budget + slack (%d): the latch must never "+
			"admit a count above that ceiling, whatever a failed census observed — a search fans out one goroutine per "+
			"resident segment, so this count is interactive query latency",
		sustained, budget+slack)
}
