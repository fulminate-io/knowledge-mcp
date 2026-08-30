// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestDiscover_IncompleteEmptyPageNeverLatchesADrain is the standing gate on the
// self-confirming latch: an axis must NEVER report a drain while every answer the
// server gave carried gap_set_complete=false.
//
// THE RULE, NOT THE REPRODUCTION. An empty page is not evidence of a drained axis.
// It is equally what a scan returns when an arm's statement FAILED — the summary
// leaf arm logs its error and returns no rows with NO error reaching the client, so
// the failure is invisible on this side — and what a window filled entirely with
// rows the server's Go gate refused returns. The server distinguishes those from a
// real drain with gap_set_complete; the client has to honor it.
//
// WHY IT IS SELF-CONFIRMING, which is what makes this worse than a delayed scan.
// Advancing the watermark on an incomplete page latches the axis to a generation
// nothing drained. From then on BOTH sides read that latch back as measurement: the
// zero-RPC cheap tick reports complete because the snapshot gen equals the
// watermark, and the server's own short-circuit reports complete because
// last_seen_gen equals its dirty gen. Neither re-measures. A page that measured
// nothing becomes a durable "this axis has no work left".
//
// THE PROBE IS THE WATERMARK AND THE FLAG TOGETHER. Asserting only the flag would
// pass against a client that latched the watermark but happened to answer false
// once; asserting only the watermark would miss the synthesis. Both are checked, on
// the same run, across repeated ticks.
func TestDiscover_IncompleteEmptyPageNeverLatchesADrain(t *testing.T) {
	ctx := context.Background()
	fake := newFakeWireClient()

	// The reachable shape: an EMPTY page, at a real generation, that the server
	// explicitly reports as NOT the whole gap set. GapSetComplete is left false on
	// purpose — this models the arm-error and saturated-window routes, both of which
	// reach the client as items=0 with no error.
	fake.summaryScanResp = &knowledgev1.PipelineScanResponse{DirtyGen: 3, GapSetComplete: false}
	p := genPollTestPipeline(t, fake)
	c := registerStubCollector(p, "repoIncomplete")

	fake.seedGenPoll(entry("repoIncomplete", "summary", 3), entry("repoIncomplete", "embed", 0))
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)

	items, complete, err := c.discover(ctx, "summary", &c.lastSummaryGen)
	require.NoError(t, err,
		"the arm-error route reaches the client as an empty page with NO error — that is "+
			"exactly why the flag, and not the error, is what discriminates here")
	require.Empty(t, items, "fixture: the page is empty")
	require.False(t, complete,
		"the server said this page is not the whole gap set; discover must pass that through")

	require.Zero(t, c.lastSummaryGen.Load(),
		"THE LATCH: an empty page the server reported INCOMPLETE must not advance the "+
			"watermark. Advancing here pins the axis to a generation nothing drained, and both "+
			"cheap ticks then read that pin back as a completeness nobody measured")

	// REPEATED TICKS AT THE SAME GENERATION must keep re-asking rather than settling
	// into the zero-RPC tick. This is the half that fails loudly on a client which
	// latched: once latched, the scan count stops rising and the flag flips to true.
	scansAfterFirst := fake.scansByAxis["summary"]
	require.Positive(t, scansAfterFirst, "CONTROL: the first tick must actually have scanned")

	for range 3 {
		fake.seedGenPoll(entry("repoIncomplete", "summary", 3), entry("repoIncomplete", "embed", 0))
		_, throttled := p.genPollOnce(ctx)
		require.False(t, throttled)

		items, complete, err := c.discover(ctx, "summary", &c.lastSummaryGen)
		require.NoError(t, err)
		require.Empty(t, items)
		require.False(t, complete,
			"no tick may report a drain while every server answer carried "+
				"gap_set_complete=false — a true here is synthesized, not measured")
		require.Zero(t, c.lastSummaryGen.Load(), "and the watermark must stay put")
	}
	assert.Greater(t, fake.scansByAxis["summary"], scansAfterFirst,
		"an un-drained axis must keep issuing detail fetches; a scan count that stopped "+
			"rising means the cheap tick took over on a watermark nothing earned")

	// THE KNOWN POSITIVE, on the same collector and the same generation: the ONLY
	// change is the server reporting the page complete. Without this arm every
	// assertion above is satisfied by a client that never advances its watermark at
	// all, which would be a different defect wearing the same green.
	fake.summaryScanResp = &knowledgev1.PipelineScanResponse{DirtyGen: 3, GapSetComplete: true}
	items, complete, err = c.discover(ctx, "summary", &c.lastSummaryGen)
	require.NoError(t, err)
	require.Empty(t, items)
	require.True(t, complete, "the server now reports the whole gap set was seen")
	require.Equal(t, uint64(3), c.lastSummaryGen.Load(),
		"KNOWN POSITIVE: a COMPLETE empty page is a real drain and MUST advance the "+
			"watermark — otherwise the axis never quiesces and the cheap tick never fires")
}
