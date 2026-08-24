// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestReconcileResidentDegenerate_IntactCorpusLoadsInOnePass pins the load-first
// CONTRACT the Phase 1 heal-closure gate depends on: a fresh Manager facing
// an INTACT multi-segment persisted corpus reaches coverage-passing resident
// (resident >= residentBackstopFloor AND resident >= residentBackstopRatio*shipped)
// in a SINGLE ReconcileResidentDegenerate pass, with degenerate=false — so the
// bootstrap gate can skip the from-scratch RebuildSegments and heal via the load
// alone.
//
// This is a GREEN-GREEN contract pin, NOT a behavioral red-green — and it is
// labeled so deliberately. The read engine's load() already fast-loads an intact
// corpus (proven by TestReconcileResidentDegenerate_ColdHeals); the segmentdist
// read primitive was NEVER the bug, so this test passes both BEFORE and
// AFTER the Phase 1 reorder. Its role is purely to PIN the single-pass load-reaches-
// coverage contract the gate relies on, so a future regression that breaks the
// read-engine fast-load is caught here. The BEHAVIORAL red-green (RebuildSegments
// fires on a Phase-1-reverted tree, ZERO after) lives in the Phase 3 bootstrap
// heal-closure test, where the rebuild invocation is observable via scanCallCount.
// Do not misread this as a mislabeled red-green.
//
// It EXTENDS the ColdHeals shape (single-segment) to a multi-segment corpus and
// adds the explicit ratio-coverage assertion — reusing the existing in-package
// helpers (newSegmentHarness, NewManager, the embed write path, hnswVecDocs, serverHNSWDocCount,
// residentBackstopFloor/residentBackstopRatio); no new harness or producer
// scaffolding is invented.
func TestReconcileResidentDegenerate_IntactCorpusLoadsInOnePass(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "restartFastloadRepo"}

	// Process 1 (producer): ship a MULTI-segment INTACT HNSW corpus. Each batch of
	// searchCorpusN (== MinSegmentDocs) vectors with a distinct per-batch id prefix
	// is force-sealed, and the tick re-emits and ships the accumulated corpus.
	const corpusSegs = 3
	producer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("fastload-b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, producer.AddAndMarkDirty(ctx, kgtypes.GraphCode, "restartFastloadRepo", batch))
	}
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "restartFastloadRepo"))
	require.NotEmpty(t, shippedHNSWIDs(svc),
		"process 1 ships a multi-segment intact corpus to the server")
	shipped := serverHNSWDocCount(t, gc, target)
	require.GreaterOrEqual(t, shipped, residentBackstopFloor,
		"the shipped corpus clears the floor (the denominator the ratio probe reads)")

	// Process 2 (restart): a FRESH consumer Manager starts cold (resident 0) — it has
	// loaded nothing yet this run.
	consumer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	require.Equal(t, 0, consumer.ResidentDocCount(kgtypes.GraphCode, "restartFastloadRepo"),
		"the fresh consumer has not loaded the corpus yet")

	// ACT — exactly ONE recovery pass.
	degenerate, err := consumer.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "restartFastloadRepo")
	require.NoError(t, err)
	require.False(t, degenerate,
		"an intact shipped corpus is restored by a single load() — NOT flagged for rebuild")

	// ASSERT single-pass coverage: the one load() reached coverage-passing resident —
	// both the absolute floor AND the ratio-of-shipped condition the Phase 1 gate
	// relies on to skip the rebuild.
	resident := consumer.ResidentDocCount(kgtypes.GraphCode, "restartFastloadRepo")
	require.GreaterOrEqual(t, resident, residentBackstopFloor,
		"one load() made the intact corpus resident above the floor")
	require.GreaterOrEqual(t, float64(resident), residentBackstopRatio*float64(shipped),
		"one load() reached the ratio-of-shipped coverage — the GREEN condition the gate skips the rebuild on")
}
