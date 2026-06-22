// SPDX-License-Identifier: Apache-2.0

package bootstrap

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

// fastloadVecDocs builds n searchengine.Documents with deterministic 32-byte
// vectors — the bootstrap-package mirror of segmentdist.hnswVecDocs (which lives in
// package segmentdist and cannot be imported across the _test package boundary). The
// producer Manager seals these into a REAL decodable HNSW segment so a consumer
// load() imports them (NOT the []byte("seg") placeholder, which never decodes).
func fastloadVecDocs(prefix string, n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0x632F, 0xA571))
	docs := make([]searchengine.Document, n)
	for i := range docs {
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		docs[i] = searchengine.Document{ID: fmt.Sprintf("%s-n%d", prefix, i), Vector: v}
	}
	return docs
}

// TestHealClosure_IntactCorpusLoadsNoRebuild is the BEHAVIORAL red-green at
// the level where the RebuildSegments invocation is observable: the bootstrap
// embed-drain heal closure (buildHealFactory). It proves that an INTACT-but-not-
// resident shipped corpus is healed by a one-shot read-engine load — ZERO
// RebuildSegments paged (scanCallCount==0) — once Phase 1 reorders the closure to
// try ReconcileResidentDegenerate before the rebuild.
//
// RED-GREEN: on a tree WITHOUT the Phase 1 reorder the closure rebuilds on every
// segmentPoolDegenerate==true, so PipelineScan is paged (scanCallCount>=1) and the
// scanCallCount==0 assertion FAILS. With Phase 1 the load reaches coverage
// (resident 96 >= floor 64) so degenerate=false and the rebuild is skipped
// (scanCallCount==0) — GREEN. The ResidentDocCount>=64 assertion additionally pins
// that the heal was a real LOAD-REACHES-COVERAGE heal, not a sub-floor disarm
// masquerading as one.
//
// NUMERIC INVARIANT (must hold for a genuine red-green, not a disarm in disguise):
// for a real decodable corpus, covered == resident == N (ShippedSegmentDocCount and
// ResidentDocCount sum the same sealed segments). Both triggers line up on that N:
//   - segmentPoolDegenerate TRUE (closure ENTERS load-first): covered N < 0.5*embedded
//     AND embedded >= segmentCoverageFloor(64). With embedded=300, N=96: 96 < 150 ✓,
//     300 >= 64 ✓.
//   - ReconcileResidentDegenerate FALSE via the resident>=floor short-circuit (load
//     REACHES coverage, rebuild skipped): resident N >= residentBackstopFloor(64).
//     96 >= 64 ✓.
//
// Both hold ONLY in the band floor <= N < embedded/2 → N>=64 AND embedded>128. N<64
// would fall past the floor short-circuit to the sub-floor disarm (degenerate=false
// for the WRONG reason — a disarm, not a load-reaches-coverage), so resident would
// be 0<64 and the GREEN ResidentDocCount>=64 assertion would catch it.
func TestHealClosure_IntactCorpusLoadsNoRebuild(t *testing.T) {
	const (
		repo     = "intactRepo"
		corpusN  = 96  // floor(64) <= 96 < embedded/2(150)
		embedded = 300 // >128 so the band floor<=N<embedded/2 is non-empty
	)
	c, eng := buildReconcileClientWith(t, embedded, true /*realFetch*/, repo)
	ctx := context.Background()

	// Ship a REAL decodable HNSW corpus of N=96 docs through the SAME router the
	// consumer segmentMgr loads from. AddAndShip of a sub-MinSegmentDocs(1024) batch
	// just BUFFERS, so Flush force-seals + ships the 96-doc tail as one real segment.
	producer := segmentdist.NewManager(c.router, t.TempDir(), 0)
	require.NoError(t, producer.AddAndShip(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, corpusN)))
	require.NoError(t, producer.Flush(ctx, kgtypes.GraphCode, repo))

	// The shipped corpus is present (covered == N) but the consumer's live engine has
	// not loaded it yet (resident 0) — the intact-but-not-resident post-restart state.
	covered, _, err := c.segmentMgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.GreaterOrEqual(t, covered, 64, "the shipped corpus clears the floor")
	require.Less(t, covered, embedded/2, "covered < 0.5*embedded so segmentPoolDegenerate arms (closure enters load-first)")
	require.Equal(t, 0, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, repo),
		"the consumer has not loaded the corpus yet (resident 0)")

	// Seed a scan page so that, IF a rebuild wrongly fired, PipelineScan would be paged
	// (making the scanCallCount==0 assertion meaningful — not vacuously satisfied).
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	// ACT: invoke the per-graph embed-drain heal closure.
	heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
	require.NotNil(t, heal, "a code graph gets a non-nil heal closure")
	require.NoError(t, heal(ctx), "the heal closure succeeds")

	// GREEN (Phase 1): the read-engine load reached coverage (resident 96 >= floor 64)
	// so degenerate=false and the from-scratch rebuild was SKIPPED.
	require.Equal(t, 0, eng.scanCallCount(repo),
		"INTACT corpus healed via one-shot load — RebuildSegments NEVER paged (scanCallCount==0)")
	require.GreaterOrEqual(t, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, repo), 64,
		"the load made the corpus resident at/above floor — a real load-reaches-coverage heal, not a sub-floor disarm")
}

// TestHealClosure_UnloadableCorpusStillRebuilds is the over-suppression GUARD: it
// proves the Phase 1 load-first gate suppresses the rebuild ONLY when a load
// actually restores coverage, never unconditionally. A GENUINELY-unloadable corpus
// — the default empty-Fetch healSegmentService (load imports nothing) with shipped
// metas >= floor — under the SAME armed embedded knob STILL pages RebuildSegments:
// ReconcileResidentDegenerate's post-load resident stays 0 < floor while shipped
// (64,64) >= floor (no sub-floor disarm), so degenerate=true → rebuild fires
// (scanCallCount>=1).
//
// This case is reachable ONLY because of the same nonzero-embedded fixture knob: the
// closure must reach the load-first/rebuild branch (segmentPoolDegenerate TRUE),
// which needs embedded>=64. With embedded=0 the closure would exit at the healthy
// branch and this guard would be silently defeated.
func TestHealClosure_UnloadableCorpusStillRebuilds(t *testing.T) {
	const (
		repo     = "unloadableRepo"
		embedded = 300 // armed so segmentPoolDegenerate enters the load-first branch
	)
	// realFetch=false: the default empty Fetch — a load imports nothing.
	c, eng := buildReconcileClientWith(t, embedded, false /*realFetch*/, repo)
	ctx := context.Background()

	// Shipped metas summing to 128 (>> floor 64) via the []byte("seg") placeholder —
	// it never decodes, so load imports nothing → resident stays 0. covered=128 <
	// 0.5*300=150 so segmentPoolDegenerate arms.
	shipHNSW(t, c, repo, 64, 64)
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
	require.NotNil(t, heal, "a code graph gets a non-nil heal closure")
	require.NoError(t, heal(ctx), "the heal closure succeeds")

	require.GreaterOrEqual(t, eng.scanCallCount(repo), 1,
		"an unloadable corpus (load restores nothing) STILL rebuilds — the gate suppresses only when a load reaches coverage")
	require.Equal(t, 0, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, repo),
		"the load imported nothing — resident stays empty, so the rebuild was the correct heal")
}

// TestHealNeedsRebuild_ProbeErrorNeverRebuilds is the directive red-green: "a
// timeout must never rebuild." It drives healNeedsRebuild's THREE-way decision into
// the ReconcileResidentDegenerate probe-error arm and asserts that a probe error
// (server down / 524 timeout) returns (false, nil) — the existing resident is kept
// and NO from-scratch RebuildSegments fires. It also pins the companion: a GENUINE
// resDegen==true from a SUCCESSFUL probe STILL returns (true, nil) and rebuilds, so
// the fix suppresses ONLY the transient-failure storm, never a real collapse.
//
// RED-GREEN: with client_segment.go line 328 = `return false, nil` the probe-error
// arm keeps the resident and skips the rebuild — GREEN. Restoring it to
// `return true, nil` makes a probe error rebuild — the storm being eliminated —
// flipping the (false, nil) + scanCallCount==0 assertions RED.
//
// Both subtests share the armed-embedded + degenerate-shipped fixture so the
// closure reaches the ReconcileResidentDegenerate call (HasShippedSegments true,
// segmentPoolDegenerate true). They differ ONLY in whether the probe's load() errors
// (failListAfterN) or merely fails to restore coverage (empty Fetch).
func TestHealNeedsRebuild_ProbeErrorNeverRebuilds(t *testing.T) {
	const embedded = 300 // armed so segmentPoolDegenerate enters the load-first branch

	t.Run("probe error keeps resident, NO rebuild", func(t *testing.T) {
		const repo = "probeErrRepo"
		c, eng, seg := buildReconcileClientWithSeg(t, embedded, false /*realFetch*/, repo)
		ctx := context.Background()

		// Shipped metas summing to 128 (>> floor 64): covered=128 < 0.5*300=150 so
		// segmentPoolDegenerate arms and the closure reaches the probe.
		shipHNSW(t, c, repo, 64, 64)
		// Seed a scan page so a wrongly-fired rebuild WOULD page PipelineScan (the
		// scanCallCount==0 assertion is meaningful, not vacuous).
		eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

		// The server answers the ONE cheap manifest-snapshot probe (the consolidated
		// ShippedManifestSnapshot that now feeds BOTH presence and doc-count = 1
		// ListDelta call) then times out on the heal probe's cache-first load() (the
		// 2nd+ ListDelta) — the 524/down shape. (Pre-consolidation this was 2 presence
		// Lists then a 3rd; the snapshot collapse drops it to 1 then the load.)
		seg.mu.Lock()
		seg.failListAfterN = 1
		seg.mu.Unlock()

		// DIRECT assertion on the decision: a probe error returns (false, nil) — keep
		// the existing resident, do NOT rebuild against a down/timing-out server.
		needsRebuild, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err, "a probe error is best-effort — healNeedsRebuild never propagates it")
		require.False(t, needsRebuild,
			"a probe error (server down / timeout) must NOT rebuild — keep the existing resident")

		// BEHAVIORAL assertion: drive the full heal closure and prove ZERO rebuild
		// fires over the probe error. Re-arm the failure: the direct call above already
		// consumed ListDelta calls and the listCalls counter persists, so reset it so the
		// closure's ONE snapshot probe (call 1) succeeds again and its load() (call 2+)
		// errors.
		seg.mu.Lock()
		seg.listCalls = 0
		seg.mu.Unlock()
		heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
		require.NotNil(t, heal, "a code graph gets a non-nil heal closure")
		require.NoError(t, heal(ctx), "the heal closure succeeds (best-effort over the probe error)")
		require.Equal(t, 0, eng.scanCallCount(repo),
			"a probe error fires ZERO RebuildSegments — PipelineScan never paged (the timeout-must-never-rebuild directive)")
	})

	t.Run("genuine degenerate from a SUCCESSFUL probe STILL rebuilds", func(t *testing.T) {
		const repo = "genuineDegenRepo"
		// realFetch=false (empty Fetch) and NO failListAfterN: the probe's load()
		// SUCCEEDS but imports nothing, so resident stays 0 < floor while shipped
		// (64,64) >= floor → resDegen=true → rebuild.
		c, eng, _ := buildReconcileClientWithSeg(t, embedded, false /*realFetch*/, repo)
		ctx := context.Background()

		shipHNSW(t, c, repo, 64, 64)
		eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

		// DIRECT assertion: a SUCCESSFUL probe reporting a genuinely-collapsed pool
		// (resDegen=true) still returns (true, nil) — the real-collapse path is intact.
		needsRebuild, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.True(t, needsRebuild,
			"a genuine resDegen==true from a successful probe STILL rebuilds — the fix suppresses only transient failures")

		// BEHAVIORAL assertion: the full closure pages RebuildSegments for the real
		// collapse.
		heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
		require.NotNil(t, heal, "a code graph gets a non-nil heal closure")
		require.NoError(t, heal(ctx), "the heal closure succeeds")
		require.GreaterOrEqual(t, eng.scanCallCount(repo), 1,
			"a genuine collapse from a successful probe rebuilds — PipelineScan is paged")
	})
}
