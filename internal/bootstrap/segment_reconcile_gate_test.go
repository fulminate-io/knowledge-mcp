// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_reconcile_gate_test.go isolates the shipped-completeness shipped-completeness gate
// proof. It shares the reconcile fixtures (buildReconcileClientWithSeg, shipHNSW,
// makeReconcileScanPage) defined in segment_reconcile_test.go — same package — and is
// split out only to keep both test files under the 500-line file cap.

// TestReconcileSegmentCoverage_ShippedCompleteNoRebuild is the shipped-completeness-gate fix proof: a
// graph whose READ engine is degenerate (empty Fetch → resident 0, so
// ReconcileResidentDegenerate flags it) but whose SHIPPED corpus already COVERS the
// embedded node count must NOT be rebuilt. The read engine is merely lazily loaded,
// and a PG RebuildSegments writes the DETERMINISTIC engine (m.detManagers) — never
// raising the READ engine's (m.managers) resident count the probe re-reads — so
// rebuilding would re-flag on every 5-min tick: the ~85 rebuilds/wk loop. The
// healNeedsRebuild shipped-completeness gate skips it.
//
// TWO reconcile passes assert scanCallCount stays FLAT at 0 — the loop is CLOSED, not
// merely deferred one tick. Per the coverage-ratio advisory the fixture arms the REAL
// ratio branch of segmentPoolDegenerate, NOT the sub-floor small-graph disarm:
// embedded=64 (== segmentCoverageFloor, so the `embedded < floor` disarm does NOT
// fire) and shipped covered=64 (>= 0.5*64 ratio) → segmentPoolDegenerate=false via the
// coverage branch.
//
// RED without the Step-2.2 gate: reconcileSegmentCoverage rebuilds on
// degenerate==true on BOTH passes (scanCallCount >= 2). GREEN with the gate:
// healNeedsRebuild sees shipped-complete → skip → scanCallCount stays 0.
func TestReconcileSegmentCoverage_ShippedCompleteNoRebuild(t *testing.T) {
	const (
		repo = "shippedCompleteRepo"
		// == segmentCoverageFloor so segmentPoolDegenerate takes the coverage-RATIO
		// branch, not the sub-floor small-graph disarm (the advisory's point: exercise
		// the real coverage branch, so the GREEN is not a vacuous tiny-graph pass).
		embedded = 64
	)
	// realFetch=false: the default empty Fetch — load imports nothing, so the read
	// engine stays resident 0 and ReconcileResidentDegenerate keeps flagging it. This
	// is what makes the pre-fix code rebuild every pass (RED) and the post-fix gate
	// skip (GREEN, because the shipped corpus already covers the embedded set).
	c, eng, backend := buildReconcileClientWithSeg(t, embedded, repo)
	ctx := context.Background()

	// Shipped corpus of exactly 64 (>= floor so ReconcileResidentDegenerate arms, and
	// it COVERS the 64 embedded nodes: 64 >= 0.5*64 → segmentPoolDegenerate=false →
	// healNeedsRebuild==false). One digest of DocCount 64.
	shipHNSW(t, backend, repo, 64)
	// Seed a scan page so a WRONGLY-fired rebuild would page PipelineScan (making the
	// scanCallCount==0 assertions meaningful, not vacuous).
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	// PRE: the read engine IS genuinely degenerate (empty Fetch → resident 0 vs shipped
	// 64) — the gate's input condition. Without the gate this rebuilds.
	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.True(t, degenerate,
		"PRE: the read engine is degenerate (resident 0 vs shipped 64) — the gate's input, so a pre-fix tree WOULD rebuild")

	// TWO passes: the loop is CLOSED, not deferred. A lazily-loaded read engine over a
	// complete shipped corpus is skipped on EVERY tick.
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 0, eng.scanCallCount(repo),
		"pass 1: shipped covers embedded → healNeedsRebuild skips the PG rebuild (PipelineScan never paged)")
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 0, eng.scanCallCount(repo),
		"pass 2: STILL no rebuild — the re-flag loop is closed, not merely deferred one tick")
}
