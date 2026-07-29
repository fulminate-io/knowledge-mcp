// SPDX-License-Identifier: Apache-2.0

package bootstrap

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

// segment_reconcile_bm25_test.go covers the BM25 arm of the rebuild gate. It shares
// the reconcile fixtures defined in segment_reconcile_test.go (same package) and is
// a separate file both for the 500-line cap and because segment_reconcile_gate_test.go
// is content-pinned — a new test may not be added there.
//
// EVERY test here registers t.Cleanup(resetBM25HealProgress) as its first statement.
// The gate's no-progress state is package-level and does not self-clear, so a test
// that leaves an armed record silently makes the next test's rebuild decline.

// shipBM25For is shipHNSWFor's BM25 sibling: it seeds a BM25-format manifest for
// (gt, name) on the fake backend. The backend keys manifests by graph type, name AND
// format, so seeding one format leaves the other unseeded — which is what lets a
// fixture model a shipped BM25 corpus alongside a separately-shipped HNSW one.
func shipBM25For(t *testing.T, backend *fakeSegBackend, gt kgtypes.GraphType, name string, docCounts ...int) {
	t.Helper()
	digests := make([]segManifestDigest, 0, len(docCounts))
	for i, dc := range docCounts {
		digests = append(digests, segManifestDigest{
			ContentHash: name + "-b" + string(rune('A'+i)),
			DocCount:    dc,
		})
	}
	backend.seedManifest(string(gt), name, bm25.New().Name(), digests)
}

// makeReconcileScanPageNoBM25 is makeReconcileScanPage with the BM25 fields left NIL:
// the items carry vectors but no BM25 content, so the rebuild yields HNSW documents
// and ZERO BM25 documents and the BM25 read pool cannot rise. It is the non-convergent
// rebuild shape — a completed rebuild that does not move the arm it was fired for.
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

// resetSegReadCalls and segReadCalls read/reset the backend's manifest-read counter
// under the backend mutex — an unlocked access races the handler goroutine.
func resetSegReadCalls(backend *fakeSegBackend) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.readCalls = 0
}

func segReadCalls(backend *fakeSegBackend) int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.readCalls
}

// bm25ArmVerdict probes both arms and returns the BM25 one.
func bm25ArmVerdict(t *testing.T, c *client, repo string) (resident int, degenerate bool) {
	t.Helper()
	verdicts, err := c.segmentMgr.ReconcileResidentDegenerateByFormat(context.Background(), kgtypes.GraphCode, repo)
	require.NoError(t, err)
	v, ok := armVerdictFor(verdicts, bm25.New().Name())
	require.True(t, ok, "the per-format probe reports a bm25 arm")
	return v.ResidentAfterRecover, v.Degenerate
}

// seedCollapsedBM25 is the shared fixture: a shipped HNSW corpus that COVERS the
// embedded count (so the HNSW-scoped check declines a rebuild of its own) behind a
// shipped BM25 manifest whose read pool loads empty. Neither manifest has backing
// objects, so both read pools load empty.
func seedCollapsedBM25(t *testing.T, backend *fakeSegBackend, repo string) {
	t.Helper()
	shipHNSWFor(t, backend, kgtypes.GraphCode, repo, 64)
	shipBM25For(t, backend, kgtypes.GraphCode, repo, 64, 64)
}

// TestReconcileBM25CollapseRebuilds is the reproduction: a collapsed BM25 arm behind
// a healthy HNSW arm fires the rebuild escalation AND converges.
//
// It cannot pass by the HNSW arm rebuilding instead — the PRE assertions pin the
// attribution: the shipped HNSW corpus covers the embedded count (so the HNSW-scoped
// check returns no-rebuild) while the BM25 arm reads degenerate against its own
// manifest denominator.
//
// Convergence is real rather than a disarm: a rebuild feeds every scanned item
// through the SAME BM25 engine the read path reads, and 128 resident clears
// 0.5 x the shipped 128.
func TestReconcileBM25CollapseRebuilds(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const (
		repo     = "bm25CollapseRepo"
		embedded = 64
		// >= the ratio applied to the shipped 128, so clearing it is genuine recovery.
		rebuiltDocs = 128
	)
	c, eng, backend := buildReconcileClientWithSeg(t, embedded, repo)
	ctx := context.Background()

	seedCollapsedBM25(t, backend, repo)
	eng.scanItems[repo] = makeReconcileScanPage(repo, rebuiltDocs)

	pre, err := c.segmentMgr.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	preHNSW, ok := armVerdictFor(pre, hnsw.New().Name())
	require.True(t, ok, "the per-format probe reports an hnsw arm")
	require.GreaterOrEqual(t, preHNSW.Shipped, embedded,
		"PRE: the shipped HNSW corpus covers the embedded count, so the HNSW arm declines a rebuild")
	preBM25, ok := armVerdictFor(pre, bm25.New().Name())
	require.True(t, ok, "the per-format probe reports a bm25 arm")
	require.True(t, preBM25.Degenerate,
		"PRE: the BM25 arm is degenerate against its OWN manifest denominator")

	c.reconcileSegmentCoverage(ctx)

	require.Positive(t, eng.scanCallCount(repo),
		"a shipped-but-collapsed BM25 arm fires RebuildSegments")

	resident, degenerate := bm25ArmVerdict(t, c, repo)
	require.Equal(t, rebuiltDocs, resident,
		"the BM25 read pool rose to the rebuilt corpus — a rebuild writes the engine the read path reads")
	require.False(t, degenerate, "the BM25 arm converged")
}

// TestReconcileBM25NonConvergentScannedZero is the scanned==0 non-convergence: the
// rebuild completes (ran=true) having found nothing to scan, so the arm cannot rise,
// and the gate declines the second pass.
//
// BOUND HONESTY: on THIS shape the gate merely declines EARLIER — the existing heal
// breaker would also bound it, since a scanned==0 pass records no-progress and
// latches after the trip threshold. TestReconcileBM25NonConvergentScannedNonZero is
// the shape where the gate is the ONLY bound.
func TestReconcileBM25NonConvergentScannedZero(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const repo = "bm25ScannedZeroRepo"
	c, eng, backend := buildReconcileClientWithSeg(t, 64, repo)
	ctx := context.Background()

	// The manifests of the collapse fixture, but NOTHING to scan: RebuildSegments
	// returns early with ran=true and scanned=0.
	seedCollapsedBM25(t, backend, repo)

	c.reconcileSegmentCoverage(ctx)
	afterPass1 := eng.scanCallCount(repo)
	require.Positive(t, afterPass1, "pass 1: the gate authorized a rebuild")
	resident, degenerate := bm25ArmVerdict(t, c, repo)
	require.Equal(t, 0, resident, "pass 1 scanned nothing, so the BM25 arm did not rise")
	require.True(t, degenerate, "the BM25 arm is still degenerate")

	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, afterPass1, eng.scanCallCount(repo),
		"pass 2: the no-progress bound declines — a completed rebuild that did not raise the arm is not repeated")
}

// TestReconcileBM25NonConvergentScannedNonZero is the motivating shape and the case
// where the gate's bound is the ONLY bound. The rebuild completes over a NON-empty
// scan (scanned>0), but the items carry no BM25 content so the arm cannot rise.
//
// The existing heal breaker cannot catch this: it measures progress against the HNSW
// corpus, which here is already complete, so it records PROGRESS and RESETS its
// streak after every pass. The test asserts Allow is still true precisely to prove
// that the gate — not the breaker — is what stopped the second rebuild.
func TestReconcileBM25NonConvergentScannedNonZero(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const repo = "bm25NoRiseRepo"
	warns := installBootstrapCapturingSlog(t)
	c, eng, backend := buildReconcileClientWithSeg(t, 64, repo)
	ctx := context.Background()

	seedCollapsedBM25(t, backend, repo)
	eng.scanItems[repo] = makeReconcileScanPageNoBM25(repo, 128)

	c.reconcileSegmentCoverage(ctx)
	afterPass1 := eng.scanCallCount(repo)
	require.Positive(t, afterPass1, "pass 1: the gate authorized a rebuild over a non-empty scan")
	resident, degenerate := bm25ArmVerdict(t, c, repo)
	require.Equal(t, 0, resident, "the scanned items carry no BM25 content, so the arm did not rise")
	require.True(t, degenerate, "the BM25 arm is still degenerate")
	require.True(t, c.healBreaker.Allow(kgtypes.GraphCode, repo),
		"the breaker did NOT latch — it saw HNSW progress and reset its streak, so it cannot bound this shape")

	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, afterPass1, eng.scanCallCount(repo),
		"pass 2: declined by the gate's no-progress bound, which is the only bound on this shape")

	// Matched on the gate-unique substring: three breaker messages contain
	// "no-progress", so asserting on that alone would not discriminate.
	declines := warns.warnsContaining("BM25 arm rebuild declined")
	require.NotEmpty(t, declines, "the gate emits its terminal WARN on the decline")
	require.Contains(t, declines[0], repo, "the WARN carries the graph identity")
}

// TestReconcileBM25ArmingRequiresCompletedRebuild pins the arming rule: the bound is
// consumed ONLY by a rebuild that COMPLETED. Pass 1's scan is made to fail, so
// RebuildSegments returns ran=false with an error and the reconcile continues without
// arming; pass 2 must therefore still fire.
//
// Without the pending/armed split a single transient rebuild error would arm the
// bound and disable BM25 self-heal for the lifetime of the process.
//
// The counts follow the harness's contract that PipelineScan counts every invocation:
// pass 1's erroring scan increments once and returns before any terminator call (1),
// and pass 2's completed rebuild over a non-empty page costs the page plus the empty
// terminator (+2 = 3). Contrast TestReconcileBM25NonConvergentScannedNonZero, whose
// COMPLETED pass 1 does consume the shot and leaves pass 2 declined.
func TestReconcileBM25ArmingRequiresCompletedRebuild(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const repo = "bm25ArmingRepo"
	c, eng, backend := buildReconcileClientWithSeg(t, 64, repo)
	ctx := context.Background()

	seedCollapsedBM25(t, backend, repo)
	eng.scanItems[repo] = makeReconcileScanPageNoBM25(repo, 128)

	eng.setScanErr(errors.New("injected scan failure"))
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 1, eng.scanCallCount(repo),
		"pass 1: the erroring scan counts as one invocation and returns before any terminator call")

	eng.setScanErr(nil)
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 3, eng.scanCallCount(repo),
		"pass 2: the shot was NOT consumed by the failed rebuild, so the gate authorizes another (page + terminator)")
}

// TestReconcileBM25ProbeLegCount pins the FIRST-TICK cost: the BM25 arm's L2 root is
// separate from the HNSW one, so the first pass pulls that arm's shipped corpus once.
// The second pass costs strictly fewer manifest reads — the load once-guard.
func TestReconcileBM25ProbeLegCount(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const (
		repo = "bm25LegCountRepo"
		// Measured on the implemented tree, not derived by reasoning.
		firstPassReadBound = 28
	)
	c, eng, backend := buildReconcileClientWithSeg(t, 64, repo)
	ctx := context.Background()

	seedCollapsedBM25(t, backend, repo)
	eng.scanItems[repo] = makeReconcileScanPage(repo, 128)

	resetSegReadCalls(backend)
	c.reconcileSegmentCoverage(ctx)
	pass1 := segReadCalls(backend)

	resetSegReadCalls(backend)
	c.reconcileSegmentCoverage(ctx)
	pass2 := segReadCalls(backend)

	t.Logf("manifest reads: pass1=%d pass2=%d", pass1, pass2)
	require.Less(t, pass2, pass1,
		"the second pass costs strictly fewer manifest reads — the load once-guard")
	require.LessOrEqual(t, pass1, firstPassReadBound,
		"the first-tick probe stays under its measured bound")
}

// TestReconcileBM25SteadyStateReads pins the RECURRING cost on the never-shipped
// shape — a graph with no BM25 manifest at all, which is the permanent state of most
// graphs. A pass2-under-pass1 check would admit a per-tick read that never shrinks;
// this asserts consecutive steady passes cost an EQUAL, bounded number.
//
// WHAT IT MEASURES: the ARM PROBE's recurring cost, not the gate's presence branch.
// The entry probe evaluates both arms and pays the BM25 arm's read regardless of what
// the gate later decides, and on this shape the arm would report healthy via the
// sub-floor disarm anyway — so the count would be identical without the presence
// branch. That branch is a CORRECTNESS guard (no shipped BM25 manifest means no
// rebuild, independent of where the disarm thresholds sit), not a read saving, and
// this bound will not fail if it is removed.
func TestReconcileBM25SteadyStateReads(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const (
		repo = "bm25SteadyRepo"
		// Measured on the implemented tree, not derived by reasoning.
		steadyReadBound = 9
	)
	c, eng, backend := buildReconcileClientWithSeg(t, 64, repo)
	ctx := context.Background()

	// NEVER-SHIPPED: an HNSW manifest only. The BM25 arm sits at resident 0 / shipped
	// 0, below the floor, on every tick with no once-guard to amortize it.
	shipHNSWFor(t, backend, kgtypes.GraphCode, repo, 64)
	eng.scanItems[repo] = makeReconcileScanPage(repo, 128)

	c.reconcileSegmentCoverage(ctx) // warm pass — not measured.

	resetSegReadCalls(backend)
	c.reconcileSegmentCoverage(ctx)
	pass2 := segReadCalls(backend)

	resetSegReadCalls(backend)
	c.reconcileSegmentCoverage(ctx)
	pass3 := segReadCalls(backend)

	require.Equal(t, pass2, pass3, "the per-tick manifest-read cost is steady, not growing")
	require.LessOrEqual(t, pass2, steadyReadBound, "the per-tick cost stays under its measured bound")
	require.Equal(t, 0, eng.scanCallCount(repo),
		"a graph with no shipped BM25 manifest is never rebuilt by this gate")
}
