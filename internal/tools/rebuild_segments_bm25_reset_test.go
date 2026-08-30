// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuild_segments_bm25_reset_test.go pins the hole a whole-layer BM25 swap opens in
// the decoupled keyword corpus, and the ordering that closes it.
//
// THE HOLE. The segment_rebuild scan is VECTOR-GATED for both formats, so the BM25
// layer a rebuild builds holds nothing for a node that is embed-eligible but not yet
// embedded — while the BM25 arm's feed cursor is already past that node. Swapping the
// layer in without clearing the cursor drops those documents PERMANENTLY: the node's
// later embed writeback does not move updated_at, and the embed axis no longer ships
// BM25 at all, so nothing re-emits it until its TEXT changes.
//
// WHY BOTH DIRECTIONS ARE ASSERTED. A reset that always fires is not the fix — it
// costs a full cold re-drain of the whole graph — so the second arm pins that a run
// staging no BM25 work does not pay it. That arm is also what kills the tempting
// wrong trigger: it scripts a finalize reporting Swapped=true, which a
// Swapped-gated implementation would reset on and this one must not.

// makeVectorOnlyScanPage builds a scan page whose items carry a vector but NO BM25
// fields — a rebuild that replaces the HNSW layer and stages no BM25 work at all.
//
// IT IS A REACHABLE SHAPE, not a contrivance: PipelineScanItem.Bm25Fields is nil
// whenever the server's composer declined the node (an empty field map composes to
// no message), and buildRebuildDocs turns a nil field map into no BM25 document.
func makeVectorOnlyScanPage(prefix string, n int) []*knowledgev1.PipelineScanItem {
	page := makeScanPage(prefix, 0, n)
	for _, it := range page {
		it.Bm25Fields = nil
	}
	return page
}

func TestBM25Arm_RebuildResetsCursorsBeforeSwap(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs

	t.Run("reset_precedes_swap", func(t *testing.T) {
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
			makeScanPage("a", 0, min),
		}}
		shipper := &fakeRebuildShipper{}

		res := handleClientRebuildSegments(context.Background(),
			rebuildClientDeps{scanner: scanner, shipper: shipper}, manageArgs{
				Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
			})
		require.False(t, res.IsError, "the rebuild must succeed: %v", res.Content)

		require.Equal(t, int64(1), shipper.finalizeCalls.Load(),
			"CONTROL: the run must actually have finalized, or the ordering below is read "+
				"off a rebuild that never reached the swap")
		require.Equal(t, 1, shipper.resetCount(),
			"a run that stages BM25 work must clear the arm's feed cursor exactly once")
		require.Equal(t, 1, shipper.bm25ResetsAtFinalize,
			"AND IT MUST HAVE HAPPENED BEFORE THE SWAP. The reset count observed AT the "+
				"finalize is what separates the two orderings — a reset issued afterwards "+
				"leaves this at 0 while the total still reads 1, and the window between the "+
				"swap and the reset is where the documents are lost for good")
	})

	t.Run("no_work_no_reset", func(t *testing.T) {
		// Vector-bearing items with NO composed BM25 fields: the HNSW layer is
		// replaced, no BM25 layer is, so there is nothing for the arm to re-establish.
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
			makeVectorOnlyScanPage("b", min),
		}}
		// noSwap stays false, so the fake's finalize reports Swapped=TRUE. That is the
		// point of this arm: a reset gated on Swapped would fire here.
		shipper := &fakeRebuildShipper{}

		res := handleClientRebuildSegments(context.Background(),
			rebuildClientDeps{scanner: scanner, shipper: shipper}, manageArgs{
				Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
			})
		require.False(t, res.IsError, "the rebuild must succeed: %v", res.Content)

		require.Positive(t, shipper.stageCalls.Load(),
			"CONTROL: partitions were still staged, so the zero below measures the BM25 "+
				"trigger rather than a rebuild that did nothing")
		require.Equal(t, int64(1), shipper.finalizeCalls.Load(),
			"CONTROL: and the finalize still ran, reporting Swapped=true — which is exactly "+
				"what a Swapped-gated reset would have fired on")
		require.Zero(t, shipper.resetCount(),
			"no BM25 layer changes hands, so the arm's cursor must NOT be cleared — a reset "+
				"here costs a full cold re-drain of the graph for no layer change at all")
	})

	t.Run("a_failed_reset_aborts_before_the_swap", func(t *testing.T) {
		// The reset exists to stop the swap landing over a stale cursor. If it cannot
		// be performed, swapping anyway is precisely the lossy state it prevents, and
		// nothing later re-derives the position — so the run must abort, leaving the
		// OLD layer serving and the OLD cursor consistent with it.
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
			makeScanPage("c", 0, min),
		}}
		shipper := &fakeRebuildShipper{}
		shipper.bm25ResetErr = errors.New("cursor file unwritable")

		res := handleClientRebuildSegments(context.Background(),
			rebuildClientDeps{scanner: scanner, shipper: shipper}, manageArgs{
				Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
			})
		require.True(t, res.IsError, "a failed cursor reset must surface, never be swallowed")
		require.Zero(t, shipper.finalizeCalls.Load(),
			"and it must abort BEFORE the finalize — a swap that lands with the stale cursor "+
				"still standing is the exact loss this reset exists to prevent")
	})
}
