// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuild_segments_reset_cures_test.go — the DOCUMENTED CURE for a quarantined
// segment has to be the command that actually rebuilds.
//
// WHY THIS ROW EXISTS. manage(status) tells an operator with a withdrawn segment to
// run rebuild_segments, and a quarantine touches neither the node set nor the stored
// watermark — it moves a .seg file aside. A default rebuild scans only what changed
// since the last rebuild that landed, so on a corpus whose nodes have not changed the
// prescribed command scans nothing, builds nothing and reports a clean run while the
// documents are still unreachable. The cure has to carry reset: true, and this row is
// what keeps the docs and the two operator strings honest about it.

// unchangedCorpusScanner serves its page ONLY to a scan that starts from ZERO, which
// is what an unchanged corpus looks like on the wire: every node was stamped at or
// before the last landed rebuild, so a watermark-scoped scan returns nothing while a
// from-scratch scan returns the whole corpus.
type unchangedCorpusScanner struct {
	mu         sync.Mutex
	page       []*knowledgev1.PipelineScanItem
	watermarks []int64
}

func (s *unchangedCorpusScanner) PipelineScan(
	_ context.Context, req *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watermarks = append(s.watermarks, req.GetAfterStampedAtNanos())
	resp := &knowledgev1.PipelineScanResponse{ServedHorizonNanos: 1_700_000_000_000_000_000}
	if req.GetAfterStampedAtNanos() == 0 && req.GetAfterId() == "" {
		resp.Items = s.page
	}
	return resp, nil
}

func (s *unchangedCorpusScanner) Execute(
	context.Context, *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func newUnchangedCorpusScanner() *unchangedCorpusScanner {
	return &unchangedCorpusScanner{page: makeScanPage("q-", 0, searchengine.DefaultMinSegmentDocs)}
}

// TestBareRebuildIsANoOpOnAnUnchangedCorpusAndResetIsNot is the doc claim, executed.
func TestBareRebuildIsANoOpOnAnUnchangedCorpusAndResetIsNot(t *testing.T) {
	ctx := context.Background()

	t.Run("the bare command scans nothing and rebuilds nothing", func(t *testing.T) {
		scanner := newUnchangedCorpusScanner()
		shipper := &fakeRebuildShipper{}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, scanner, shipper, kgtypes.GraphCode, "cure-doc", false)
		require.NoError(t, err)
		require.True(t, out.Ran, "it runs — which is exactly why it reads as success")
		require.Zero(t, out.Built, "and builds nothing: the documents of a quarantined segment are still unreachable")
		require.False(t, out.Published, "nothing was published, so nothing was restored")
		require.Zero(t, shipper.stageCalls.Load())
		require.Zero(t, shipper.finalizeCalls.Load())
		require.Zero(t, shipper.deltaCalls.Load())
		require.NotEmpty(t, scanner.watermarks)
		require.Equal(t, deltaPriorWatermark, scanner.watermarks[0],
			"the scan was scoped to the stored watermark, which a quarantine never moves")
	})

	t.Run("reset: true rescans the whole corpus and rebuilds it", func(t *testing.T) {
		scanner := newUnchangedCorpusScanner()
		shipper := &fakeRebuildShipper{}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, scanner, shipper, kgtypes.GraphCode, "cure-doc-reset", true)
		require.NoError(t, err)
		require.True(t, out.Ran)
		require.Positive(t, out.Built, "the reset scans from zero, so the corpus is there to rebuild")
		require.True(t, out.Published, "and the layer swap lands — which is what makes the documents searchable again")
		require.Positive(t, shipper.stageCalls.Load())
		require.Equal(t, int64(1), shipper.finalizeCalls.Load())
		require.Zero(t, scanner.watermarks[0], "a reset ignores the stored watermark by construction")
	})
}
