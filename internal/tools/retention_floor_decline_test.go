// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// retention_floor_decline_test.go — the delta arm refuses to pull a window it
// cannot report a floor for.
//
// THE DEFECT IT CLOSES IS PERMANENT DELETION LOSS. retentionFloorFor returns zero
// when a durable position is UNREADABLE. Sending that zero makes the server classify
// the pass as a full rebuild rather than a delta, which emits NO erase rows at all;
// the client merges the live corpus, learns no deletion, and advances its horizon
// past the window. Every deletion inside it is then unlearnable for that graph
// forever. Declining costs one skipped pass; sending the zero costs the deletions.
//
// THE ZERO-REQUEST ASSERTION IS THE ONE THAT CARRIES THE PROPERTY. An assertion that
// only checks for an error passes against an implementation that errors AFTER
// putting the zero on the wire, which is the whole defect.

// errUnreadablePosition is what a corrupt or unreadable state record surfaces as.
// It is a TEST-LOCAL error deliberately: the production sentinel is about the
// consequence of a zero floor, not about how the read failed.
var errUnreadablePosition = errors.New("state record unreadable")

// declineFixture builds a delta-arm fixture whose two durable positions are both
// readable, so a subtest breaks exactly one of them and nothing else differs.
func declineFixture() (*fakeRepairScanner, *fakeRebuildShipper) {
	sc := &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{floorScanPage()}}
	sh := &fakeRebuildShipper{}
	sh.watermark, sh.mergeWatermark = floorLagging, floorAhead
	return sc, sh
}

func TestSegmentDeltaDeclinesWhenTheRetentionFloorIsUnreadable(t *testing.T) {
	ctx := context.Background()

	t.Run("unreadable_rebuild_state_declines", func(t *testing.T) {
		sc, sh := declineFixture()
		sh.loadErr = errUnreadablePosition

		_, err := MergeSegmentDelta(ctx, sc, sh, &fakeRepairShipper{}, nil,
			kgtypes.GraphCode, "myrepo", floorAhead)
		require.Error(t, err, "an unreadable rebuild position must DECLINE the pass")
		require.ErrorIs(t, err, ErrRetentionFloorUnreadable,
			"and it declines under the sentinel its caller classifies on, not under an anonymous error")
		require.Empty(t, sc.afterStamped,
			"and it must decline BEFORE issuing a scan — an arm that errors after sending the zero has "+
				"already asked the server for a window it will be served as a full rebuild, carrying no erases")
	})

	t.Run("unreadable_merge_watermark_declines", func(t *testing.T) {
		sc, sh := declineFixture()
		sh.mergeWatermarkErr = errUnreadablePosition

		_, err := MergeSegmentDelta(ctx, sc, sh, &fakeRepairShipper{}, nil,
			kgtypes.GraphCode, "myrepo", floorAhead)
		require.Error(t, err, "an unreadable merge position must DECLINE the pass")
		require.ErrorIs(t, err, ErrRetentionFloorUnreadable)
		require.Empty(t, sc.afterStamped, "and must decline before issuing a scan")
	})

	t.Run("readable_floor_still_scans", func(t *testing.T) {
		// THE KNOWN POSITIVE. Without it the two zero-request assertions above are
		// satisfied by a fixture in which the scan never happens for some unrelated
		// reason — a mis-wired shipper, a nil scanner, a guard higher up.
		sc, sh := declineFixture()

		_, err := MergeSegmentDelta(ctx, sc, sh, &fakeRepairShipper{}, nil,
			kgtypes.GraphCode, "myrepo", floorAhead)
		require.NoError(t, err, "both positions are readable, so this pass must proceed")
		require.Len(t, sc.afterStamped, 2,
			"exactly ONE drain — the fixture's single page plus the terminating empty page the id-cursor "+
				"scan needs to know it is exhausted. This is the control proving the two declines above "+
				"suppressed a scan that would otherwise have happened")
	})
}
