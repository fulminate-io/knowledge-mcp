// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// multiwriter_coverage_floor_test.go is the corpus-wipe-recurrence guard at the
// multi-writer layer: a writer publishing from a DEGENERATE resident set (empty
// Export, or a partial Export far below residentBackstopRatio of the shipped
// corpus) must SKIP the publish — it must NOT swap its manifest and drive a
// refcount-GC, which would wipe the prior corpus. The single-writer gate is covered
// by publish_resident_test.go (degenerate_preload_publish_is_gated); this overlays a
// concurrent, legitimately-publishing writer B and adds the assertion that A's
// degenerate publish wipes neither the prior corpus NOR B's blobs.
//
// OUT OF SCOPE (in-test note): memory-pressure eviction — this suite asserts only the
// publish-gate (eviction-absent) invariant. The dormant unprotected eviction path this
// note used to name (unloadUnderPressure) was RETIRED on 2026-08-02, so production has
// no eviction path to be in or out of scope; should one ever be built it must carry the
// LiveMembersOutside coverage gate, and this suite still would not exercise it.

// TestMultiWriterCoverageFloorEmptyExport proves writer A's EMPTY resident Export
// publish is skipped and deletes ZERO blobs while writer B legitimately holds the
// graph — the prior corpus and B's blobs survive byte-for-byte.
func TestMultiWriterCoverageFloorEmptyExport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	a, b := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphCode, "covfloor-empty"
	target := graphSelector(gt, name)

	// B legitimately ships a multi-segment corpus + publishes (the prior corpus that
	// must survive A's degenerate publish). >= residentBackstopFloor docs so the
	// coverage ratio is ARMED (not disarmed as a tiny graph).
	const corpusSegs = 4
	for s := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("cf-b%d-%s", s, batch[i].ID)
		}
		require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
	}
	// One tick ships the whole seeded window. What lands is PARTITION-shaped rather
	// than one segment per batch, so the prior corpus is identified by its MEMBERSHIP
	// below rather than by a count of the batches that built it.
	require.NoError(t, b.ReEmitDirtyBuckets(ctx, gt, name))
	priorCorpus := shippedHNSWIDs(svc)
	require.NotEmpty(t, priorCorpus, "B ships the full prior corpus")

	// A publishes from an EMPTY resident engine (it never Added anything). The
	// coverage gate must SKIP it — zero deletes, manifest untouched.
	aDM := a.managerFor(gt, name)
	require.Empty(t, aDM.engine.Export(), "A's engine is empty")
	dropped, err := aDM.shipAndPublish(ctx, aDM.locallyShipped)
	require.NoError(t, err)
	require.Empty(t, dropped, "an empty Export publish must delete ZERO blobs (skipped, not a wipe)")

	// The prior corpus survives byte-for-byte; the server live-blob count never
	// dropped below B's set.
	now := shippedHNSWIDs(svc)
	require.Len(t, now, len(priorCorpus), "B's prior corpus survives A's empty publish")
	for id := range priorCorpus {
		require.Contains(t, now, id, "every prior-corpus blob survives the degenerate empty publish")
	}
	require.GreaterOrEqual(t, serverSegCount(t, svc, target), len(priorCorpus),
		"the server live-blob count never drops below B's set")
}

// TestMultiWriterCoverageFloorSubRatioExport proves a RESTARTED writer A that ships
// a single tail segment BEFORE loading the full corpus — a resident set far below
// residentBackstopRatio of the shipped corpus — has its publish gated, so neither
// the prior corpus nor B's blobs are wiped.
func TestMultiWriterCoverageFloorSubRatioExport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	_, b := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphCode, "covfloor-subratio"
	target := graphSelector(gt, name)

	// B ships the multi-segment prior corpus (ratio armed).
	const corpusSegs = 4
	for s := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("sr-b%d-%s", s, batch[i].ID)
		}
		require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
	}
	// One tick ships the seeded window; the prior corpus is identified by membership.
	require.NoError(t, b.ReEmitDirtyBuckets(ctx, gt, name))
	priorCorpus := shippedHNSWIDs(svc)
	require.NotEmpty(t, priorCorpus)

	// A "restarts" (fresh Manager, same writer_id) and ships ONE tail segment WITHOUT
	// loading the prior corpus — its resident set is 1 of corpusSegs+1 segments, far
	// below the 0.5 coverage ratio. The publish MUST be gated (skipped).
	aRestart := restartFleetMember(t, svc, 0, t.TempDir())
	tail := hnswVecDocs(searchCorpusN)
	for i := range tail {
		tail[i].ID = fmt.Sprintf("sr-tail-%s", tail[i].ID)
	}
	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, tail))
	require.NoError(t, aRestart.ReEmitDirtyBuckets(ctx, gt, name))

	// The tail blob landed (ship is unconditional) but the publish was gated: the
	// prior corpus survives. The server now holds B's set PLUS A's tail, and every
	// prior-corpus id is intact (no refcount-GC wiped it).
	now := shippedHNSWIDs(svc)
	require.Greater(t, len(now), len(priorCorpus),
		"the sub-ratio publish is GATED — the prior corpus + the new tail survive (no wipe)")
	for id := range priorCorpus {
		require.Contains(t, now, id, "every prior-corpus blob survives the gated sub-ratio publish")
	}
	require.GreaterOrEqual(t, serverSegCount(t, svc, target), len(priorCorpus),
		"the server live-blob count never drops below B's set")
}
