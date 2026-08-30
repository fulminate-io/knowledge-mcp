// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// retention_floor_scan_test.go — what this client REPORTS as its retention
// position, asserted on the request rather than on a downstream effect.
//
// THE DEFECT IT CLOSES: two consumers of the same erase feed hold independent
// durable positions and both scan the same axis, so the server records whichever
// arrives. With an erasure between the two positions, the AHEAD consumer's scan
// raised the watermark past it and the reap destroyed it — a deletion the LAGGING
// consumer can now never learn about.
//
// ASSERT THE VALUE ON THE WIRE. An effect-only assertion passes for any
// implementation that merely happens not to reap in the fixture, which is most of
// them.

const (
	floorLagging = int64(1_000_000_000)
	floorAhead   = int64(2_000_000_000)
)

// floorScanPage is one live item, so a scan has something to return and the
// drains under test run their ordinary path rather than an empty-page shortcut.
func floorScanPage() []*knowledgev1.PipelineScanItem {
	return makeScanPage("floor", 0, 1)
}

// TestSegmentScan_RetentionFloorTakesTheSlowestConsumer drives the real entry
// points and reads the position each one put on the wire.
func TestSegmentScan_RetentionFloorTakesTheSlowestConsumer(t *testing.T) {
	ctx := context.Background()

	t.Run("ahead_consumer_sends_the_lagging_position", func(t *testing.T) {
		// The rebuild drain is AHEAD at T2; the delta consumer lags at T1. The drain
		// must report T1 — its own T2 would license reaping an erasure between them.
		sc := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
		sh := &fakeRebuildShipper{}
		sh.watermark, sh.mergeWatermark = floorAhead, floorLagging

		_, err := RebuildSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo", false)
		require.NoError(t, err)
		require.NotEmpty(t, sc.afters)
		require.Equal(t, floorLagging, sc.afters[0],
			"the ahead consumer must report the LAGGING position — reporting its own would raise the server's "+
				"retention watermark past an erasure the other consumer has not read")
	})

	t.Run("a_peer_with_no_position_imposes_no_floor", func(t *testing.T) {
		// A peer that has never run pulls nothing at all, so it has ingested nothing a
		// reaped erasure could strand. Treating its zero as a floor would drag this
		// scan down to a whole-corpus read — the retired min-including-zero form, and
		// this subtest is what keeps it retired.
		sc := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
		sh := &fakeRebuildShipper{}
		sh.watermark, sh.mergeWatermark = floorLagging, 0

		_, err := RebuildSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo", false)
		require.NoError(t, err)
		require.NotEmpty(t, sc.afters)
		require.Equal(t, floorLagging, sc.afters[0],
			"a peer with NO position imposes no floor — the scan keeps its own bound rather than collapsing to zero")
	})

	t.Run("both_origin_sites_take_the_floor", func(t *testing.T) {
		// PER NAMED REGION. An aggregate "some scan sent the minimum" is satisfied by
		// whichever arm was already lower, so each of the two non-zero senders is
		// driven separately and asserted on its own request.
		t.Run("rebuild_drain", func(t *testing.T) {
			sc := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			sh := &fakeRebuildShipper{}
			sh.watermark, sh.mergeWatermark = floorAhead, floorLagging
			_, err := RebuildSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo", false)
			require.NoError(t, err)
			require.Equal(t, floorLagging, sc.afters[0], "the rebuild drain reports the floor")
		})

		t.Run("delta_arm", func(t *testing.T) {
			// The delta arm is the AHEAD one here, so its own horizon is not the floor.
			sc := &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			sh := &fakeRebuildShipper{}
			sh.watermark, sh.mergeWatermark = floorLagging, floorAhead
			_, err := MergeSegmentDelta(ctx, sc, sh, &fakeRepairShipper{}, nil,
				kgtypes.GraphCode, "myrepo", floorAhead)
			require.NoError(t, err)
			require.NotEmpty(t, sc.afterStamped)
			require.Equal(t, floorLagging, sc.afterStamped[0], "the delta arm reports the floor")
		})
	})

	t.Run("a_zero_scan_never_raises_the_floor", func(t *testing.T) {
		// The two zero-sending arms hold no durable position, so they impose no floor
		// and must not acquire one. A change routing them through the helper and
		// letting it substitute a persisted value would put a non-zero bound on a scan
		// whose whole contract is to read from the beginning.
		t.Run("reset_rebuild", func(t *testing.T) {
			sc := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			sh := &fakeRebuildShipper{}
			sh.watermark, sh.mergeWatermark = floorAhead, floorLagging
			_, err := RebuildSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo", true)
			require.NoError(t, err)
			require.NotEmpty(t, sc.afters)
			require.Zero(t, sc.afters[0],
				"a reset reads from the beginning even with positions on disk — substituting one would silently "+
					"narrow the scan its contract widens")
		})

		t.Run("segment_repair", func(t *testing.T) {
			sc := &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			_, err := RepairUncoveredSegments(ctx, sc, &fakeRepairShipper{}, kgtypes.GraphCode, "myrepo")
			require.NoError(t, err)
			require.NotEmpty(t, sc.afterStamped)
			require.Zero(t, sc.afterStamped[0], "the repair arm reads the whole corpus and reports no position")
		})
	})

	t.Run("delta_arm_sends_its_own_horizon_on_the_scan_field", func(t *testing.T) {
		// THE TWO MEANINGS TRAVEL ON TWO FIELDS. The floor is what the erasure
		// refusal is measured against and stays the minimum across consumers; the
		// scan bound is this consumer's own position, and sending it is what stops
		// the window widening back down to a pinned rebuild watermark every pass.
		//
		// VACUITY GUARD: floorLagging and floorAhead are distinct package constants
		// (1e9 and 2e9), so no single value can satisfy both assertions below — one
		// field carrying both meanings fails one of them.
		require.NotEqual(t, floorLagging, floorAhead,
			"vacuity guard: the two positions must differ, or one value would satisfy both assertions")
		sc := &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
		sh := &fakeRebuildShipper{}
		sh.watermark, sh.mergeWatermark = floorLagging, floorAhead
		_, err := MergeSegmentDelta(ctx, sc, sh, &fakeRepairShipper{}, nil,
			kgtypes.GraphCode, "myrepo", floorAhead)
		require.NoError(t, err)
		require.NotEmpty(t, sc.afterStamped)
		require.Equal(t, floorLagging, sc.afterStamped[0],
			"control, unchanged from before the split: field 8 still carries the FLOOR across consumers")
		require.NotEmpty(t, sc.scanFroms)
		require.Equal(t, floorAhead, sc.scanFroms[0],
			"and field 10 carries THIS consumer's own horizon — the bound the scan actually reads from")
	})

	t.Run("rebuild_and_repair_arms_leave_the_scan_field_unset", func(t *testing.T) {
		// A SCOPE FENCE, labeled honestly: it is green from the moment the field
		// exists and stays green after the client edit. Its job is to fail if a later
		// edit widens the scan field beyond the delta arm, which would narrow a scan
		// whose contract is to read the whole corpus.
		t.Run("rebuild_drain", func(t *testing.T) {
			sc := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			sh := &fakeRebuildShipper{}
			sh.watermark, sh.mergeWatermark = floorAhead, floorLagging
			_, err := RebuildSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo", false)
			require.NoError(t, err)
			require.NotEmpty(t, sc.scanFroms)
			for i, got := range sc.scanFroms {
				require.Zero(t, got, "the rebuild drain must leave the scan field unset on request %d", i)
			}
		})

		t.Run("segment_repair", func(t *testing.T) {
			sc := &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
			_, err := RepairUncoveredSegments(ctx, sc, &fakeRepairShipper{}, kgtypes.GraphCode, "myrepo")
			require.NoError(t, err)
			require.NotEmpty(t, sc.scanFroms)
			for i, got := range sc.scanFroms {
				require.Zero(t, got, "the repair arm must leave the scan field unset on request %d", i)
			}
		})
	})
}
