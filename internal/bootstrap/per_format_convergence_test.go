// SPDX-License-Identifier: Apache-2.0

package bootstrap

// per_format_convergence_test.go covers the CONVERGENCE half of the collapsed-BM25
// property: that the rebuild the detection half triggers actually HEALS the collapsed
// arm, and that a rebuild which cannot heal it is BOUNDED rather than looping.
//
// WHY IT IS A SEPARATE FILE FROM THE DETECTION HALF. per_format_degeneracy_test.go
// proves the predicate splits per format and that all three deciders act on the split
// — healNeedsRebuildLocal declines, healNeedsRebuildBM25 asks for a rebuild, and the
// composite healNeedsRebuild surfaces the BM25 verdict. Every one of those assertions
// stops at the TRIGGER. None of them observes what the triggered rebuild does, so a
// rebuild that fired and healed nothing would satisfy all of them.
//
// THE COVERAGE THIS REPLACES. segment_reconcile_bm25_test.go asserted convergence
// against ArmVerdict.Degenerate and ArmVerdict.ResidentAfterRecover — a per-format
// DECISION the probe used to make and a SHIPPED denominator it used to read. Both are
// deleted: the probe now reports measurements only (ArmObservation) and the decision
// belongs to the caller holding the embedded count. The property did not go with
// them, so it is re-asserted here against the surviving surface — the arm's resident
// count and degenerateAgainstEmbedded — rather than dropped along with the machinery
// that used to express it.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// remnant is how many documents every fixture here seeds into the BM25 arm.
//
// IT IS NOT DECORATION. healNeedsRebuildBM25 runs PRESENCE before RATIO, and it
// declines outright for a graph holding NO BM25 corpus — that population recovers
// through ordinary indexing traffic, and rebuilding it from this gate would fire for
// every such graph on the first tick. So "the arm collapsed" has to be modeled as a
// corpus that EXISTS and is far below its embedded denominator, never as an absent
// one: an absent one is declined on presence and never reaches the ratio at all.
// Eight against an embedded 4096 is two orders of magnitude below the ratio.
const remnant = 8

// armResident reads one format arm's resident count off the surviving observation
// probe. It fails the test rather than returning a zero for a missing arm, because a
// silent zero here would read as "collapsed" and quietly satisfy the very assertions
// this file exists to make.
func armResident(t *testing.T, c *client, gt kgtypes.GraphType, name, format string) int { //nolint:unparam // gt is the intentional named API: it selects the graph the observation is read for, and these fixtures happen to exercise code graphs
	t.Helper()
	obs, err := c.segmentMgr.ResidentObservationsByFormat(context.Background(), gt, name)
	require.NoError(t, err)
	for _, o := range obs {
		if o.Format == format {
			require.NoError(t, o.Err, "the %s arm must be measurable", format)
			return o.ResidentAfterLoad
		}
	}
	require.FailNowf(t, "no observation for format", "format %s not among %d arms", format, len(obs))
	return 0
}

// makeReconcileScanPageNoBM25 is makeReconcileScanPage with the BM25 fields left NIL:
// the items carry vectors but no BM25 content, so the rebuild yields HNSW documents
// and ZERO BM25 documents and the BM25 read pool cannot rise. It is the non-convergent
// rebuild shape — a COMPLETED rebuild that does not move the arm it was fired for.
//
// RELOCATED from segment_reconcile_bm25_test.go, which was deleted with the
// ArmVerdict machinery its assertions read. This helper depends on none of that.
func makeReconcileScanPageNoBM25(graph string, n int) []*knowledgev1.PipelineScanItem {
	page := make([]*knowledgev1.PipelineScanItem, 0, n)
	for i := range n {
		vec := make([]byte, 32)
		vec[0] = byte(i)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       fmt.Sprintf("%s-%08d", graph, i),
			GraphName:    graph,
			BinaryVector: vec,
		})
	}
	return page
}

// TestBM25CollapseConverges is the CONVERGENCE proof: a collapsed BM25 arm behind a
// healthy HNSW arm is not merely detected — the reconcile's rebuild raises it, and
// the arm stops being degenerate.
func TestBM25CollapseConverges(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)

	const (
		repo     = "bm25ConvergeRepo"
		embedded = 4096
	)
	ctx := context.Background()
	c, eng := buildOSSHealClient(t, embedded, repo)
	gt := kgtypes.GraphCode

	// HNSW holds the whole corpus; BM25 holds a REMNANT — the 2026-07-27 shape, a
	// healthy vector arm masking a collapsed field arm.
	//
	// THE REMNANT IS LOAD-BEARING, not incidental. healNeedsRebuildBM25's first check
	// is PRESENCE: a graph with NO BM25 corpus is deliberately out of the gate's scope,
	// because that population recovers through ordinary indexing traffic. A fixture
	// that seeded nothing would be declined on presence and never reach the ratio, so
	// the rebuild this test waits for would never fire — for a reason that has nothing
	// to do with convergence.
	docs := armVerdictFixtureDocs(embedded)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, repo, docs[:remnant]))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, repo))
	require.Positive(t, c.segmentMgr.CachedSegmentCount(gt, repo, bm25.New().Name()),
		"FIXTURE CONTROL: the BM25 arm must HOLD a corpus, or the gate declines on presence")

	// PRE, and it is a fixture control as much as an assertion: if the arms did not
	// start asymmetric, everything below would pass without a rebuild happening.
	preHNSW := armResident(t, c, gt, repo, hnsw.New().Name())
	preBM25 := armResident(t, c, gt, repo, bm25.New().Name())
	require.False(t, degenerateAgainstEmbedded(preHNSW, embedded),
		"PRE: the HNSW arm covers the corpus, so on its own it declines a rebuild")
	require.True(t, degenerateAgainstEmbedded(preBM25, embedded),
		"PRE: the BM25 arm holds %d documents against %d embedded nodes — degenerate",
		preBM25, embedded)

	// Give the rebuild something to scan, then let the reconcile drive it.
	eng.scanItems[repo] = makeReconcileScanPage(repo, embedded)
	c.reconcileSegmentCoverage(ctx)

	require.Positive(t, eng.scanCallCount(repo),
		"a collapsed BM25 arm must FIRE a rebuild — without this the assertions below "+
			"would be measuring a graph nobody rebuilt")

	// POST — the convergence itself.
	postBM25 := armResident(t, c, gt, repo, bm25.New().Name())
	require.Greater(t, postBM25, preBM25,
		"the BM25 read pool must RISE — a rebuild that fires and leaves the arm where it was "+
			"has detected the collapse without healing it")
	require.False(t, degenerateAgainstEmbedded(postBM25, embedded),
		"and the arm must stop being degenerate (resident %d vs embedded %d) — "+
			"convergence, not merely motion", postBM25, embedded)

	// The HNSW arm is untouched by the BM25 heal: a rebuild that healed the field arm
	// by disturbing the vector arm would trade one collapse for another.
	require.False(t, degenerateAgainstEmbedded(armResident(t, c, gt, repo, hnsw.New().Name()), embedded),
		"the HNSW arm stays healthy across the BM25 heal")
}

// TestBM25NonConvergenceIsBounded is the other direction, and it is the one that
// keeps the heal from becoming a loop.
//
// A rebuild that CANNOT raise the arm — here because the scan finds nothing to build
// from — must consume the arm's single shot, so the next pass DECLINES instead of
// re-firing forever. That bound is what makes a permanently-collapsed arm an
// announced, terminal state rather than an infinite rebuild cycle.
func TestBM25NonConvergenceIsBounded(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)

	const (
		repo     = "bm25BoundRepo"
		embedded = 4096
	)
	ctx := context.Background()
	c, eng := buildOSSHealClient(t, embedded, repo)
	gt := kgtypes.GraphCode

	docs := armVerdictFixtureDocs(embedded)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, repo, docs[:remnant]))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, repo))
	require.Positive(t, c.segmentMgr.CachedSegmentCount(gt, repo, bm25.New().Name()),
		"FIXTURE CONTROL: the BM25 arm must HOLD a corpus, or the gate declines on presence")
	require.True(t, degenerateAgainstEmbedded(armResident(t, c, gt, repo, bm25.New().Name()), embedded),
		"fixture: the BM25 arm must start collapsed")

	// The scan yields NOTHING, so the rebuild runs and cannot raise the arm.
	eng.scanItems[repo] = nil

	c.reconcileSegmentCoverage(ctx)
	firstPassScans := eng.scanCallCount(repo)
	require.Positive(t, firstPassScans, "the first pass must actually attempt a rebuild")
	require.True(t, degenerateAgainstEmbedded(armResident(t, c, gt, repo, bm25.New().Name()), embedded),
		"fixture: a rebuild that scanned nothing cannot have healed the arm")

	// SECOND PASS: the shot is spent, so the gate declines rather than re-firing.
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, firstPassScans, eng.scanCallCount(repo),
		"a BM25 arm that failed to rise must not re-fire the rebuild — an unbounded retry on a "+
			"cause that cannot clear is a loop, not a heal")
}

// TestBM25NonConvergenceBoundedWhereBreakerCannot is the sharper half of the bound.
//
// TestBM25NonConvergenceIsBounded uses an EMPTY scan, and on that shape the heal
// breaker would also eventually bound the loop — a scanned==0 pass records
// no-progress. THIS shape is the one where the gate is the ONLY bound: the scan
// returns items, the rebuild COMPLETES and raises the HNSW arm, so the breaker sees
// progress and resets its streak — while the BM25 arm, whose items carry no field
// content, never rises. Without the gate's own no-progress record this rebuilds
// forever on a cause that cannot clear.
func TestBM25NonConvergenceBoundedWhereBreakerCannot(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)

	const (
		repo     = "bm25NoRiseRepo"
		embedded = 4096
	)
	ctx := context.Background()
	warns := installBootstrapCapturingSlog(t)
	c, eng := buildOSSHealClient(t, embedded, repo)
	gt := kgtypes.GraphCode

	docs := armVerdictFixtureDocs(embedded)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, repo, docs[:remnant]))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, repo))
	require.Positive(t, c.segmentMgr.CachedSegmentCount(gt, repo, bm25.New().Name()),
		"FIXTURE CONTROL: the BM25 arm must HOLD a corpus, or the gate declines on presence")
	require.True(t, degenerateAgainstEmbedded(armResident(t, c, gt, repo, bm25.New().Name()), embedded),
		"fixture: the BM25 arm must start collapsed")

	// Items with vectors but NO field content: the rebuild completes and cannot raise
	// the BM25 arm.
	eng.scanItems[repo] = makeReconcileScanPageNoBM25(repo, embedded)

	c.reconcileSegmentCoverage(ctx)
	afterPass1 := eng.scanCallCount(repo)
	require.Positive(t, afterPass1, "pass 1: the gate authorized a rebuild over a non-empty scan")
	require.True(t, degenerateAgainstEmbedded(armResident(t, c, gt, repo, bm25.New().Name()), embedded),
		"the scanned items carry no BM25 content, so the arm did not rise")
	require.True(t, c.healBreaker.Allow(gt, repo),
		"THE BREAKER DID NOT LATCH — it saw HNSW progress and reset its streak, which is exactly "+
			"why the gate's own bound is the only thing standing between this and a loop")

	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, afterPass1, eng.scanCallCount(repo),
		"pass 2: declined by the gate's no-progress bound")

	// Matched on the gate-unique substring: several breaker messages also contain
	// "no-progress", so asserting on that alone would not discriminate.
	declines := warns.warnsContaining("BM25 arm rebuild declined")
	require.NotEmpty(t, declines, "the gate emits its terminal WARN on the decline")
	require.Contains(t, declines[0], repo, "the WARN carries the graph identity")
}

// TestBM25ArmingRequiresCompletedRebuild pins the arming rule: the single shot is
// consumed ONLY by a rebuild that COMPLETED.
//
// WHY IT MATTERS, in the production doc's own terms: "a rebuild that did not run
// cannot have failed to raise the arm, and arming on one would let a single transient
// error disable BM25 self-heal for the lifetime of the process." Pass 1's scan is
// made to fail, so the rebuild returns without completing and must NOT arm; pass 2
// must therefore still fire.
func TestBM25ArmingRequiresCompletedRebuild(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)

	const (
		repo     = "bm25ArmingRepo"
		embedded = 4096
	)
	ctx := context.Background()
	c, eng := buildOSSHealClient(t, embedded, repo)
	gt := kgtypes.GraphCode

	docs := armVerdictFixtureDocs(embedded)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, repo, docs[:remnant]))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, repo))
	require.Positive(t, c.segmentMgr.CachedSegmentCount(gt, repo, bm25.New().Name()),
		"FIXTURE CONTROL: the BM25 arm must HOLD a corpus, or the gate declines on presence")
	eng.scanItems[repo] = makeReconcileScanPageNoBM25(repo, embedded)

	eng.setScanErr(errors.New("injected scan failure"))
	c.reconcileSegmentCoverage(ctx)
	failedPassScans := eng.scanCallCount(repo)
	require.Positive(t, failedPassScans, "pass 1 must have attempted the scan that then failed")

	eng.setScanErr(nil)
	c.reconcileSegmentCoverage(ctx)
	require.Greater(t, eng.scanCallCount(repo), failedPassScans,
		"pass 2 MUST still fire: the shot was not consumed by a rebuild that never completed. "+
			"An equality here means one transient error disabled BM25 self-heal for the process")
}
