// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collector_axes.go holds the PER-AXIS wiring for the shared discovery loop: the
// loopAxis descriptor and the two thin entry points that fill it in. It was split
// out of collector.go when that file crossed the repo's 500-line cap, on the same
// reasoning collector_heal.go was: the axis descriptor grows by a field whenever
// the loop gains per-axis state, while runLoop itself does not.

// loopAxis bundles the per-axis wiring the shared discovery loop needs so the
// summary and embed loops share ONE implementation of the drain-gate (#2),
// idle-backoff (#1), and scan-error backoff (#3). Duplicating that control
// flow per axis is exactly how the two loops drift apart, so they don't.
type loopAxis struct {
	axis    string          // "summary" | "embed" — the pipeline_scan axis
	lastGen *atomic.Uint64  // per-axis dirty-gen cache (c.lastSummaryGen / lastEmbedGen)
	relSize int             // release-channel buffer
	backoff *errBackoff     // #3 per-axis scan-error gate (separate from the worker LLM gate)
	wake    <-chan struct{} // collect-fired wake (c.summaryWake / embedWake) — cuts the idle sleep short
	// drainedAtEpoch is THIS axis's quiescence stamp (c.summaryDrainedAtEpoch /
	// embedDrainedAtEpoch). Carried on the axis descriptor so runLoop stays
	// axis-agnostic, exactly as lastGen and wake are.
	drainedAtEpoch *atomic.Uint64
	// push sends one item's Work onto the axis channel, returning false on
	// ctx cancel (caller exits the loop). The axis-specific Work struct +
	// target channel live here so runLoop stays axis-agnostic.
	push func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool
}

// runSummaryLoop is the summary-axis discovery cycle (shared runLoop with the
// summary wiring). No client-side graph-type gate: the server returns
// empty items for non-summarizable graph types, so a redundant client gate
// would only duplicate that decision.
func (c *collector) runSummaryLoop(ctx context.Context) {
	c.runLoop(ctx, loopAxis{
		axis:           "summary",
		lastGen:        &c.lastSummaryGen,
		drainedAtEpoch: &c.summaryDrainedAtEpoch,
		relSize:        c.cfg.SummaryChannelSizeOrDefault(),
		backoff:        newErrBackoff(c.cfg.ErrBackoffBaseOrDefault(), c.cfg.ErrBackoffMaxOrDefault()),
		wake:           c.summaryWake,
		push: func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool {
			select {
			case <-ctx.Done():
				return false
			case c.summaryCh <- SummaryWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				SummarizeText: item.GetSummarizeText(), Release: release, Backend: c.client,
			}:
				return true
			}
		},
	})
}

// runEmbedLoop is the embed-axis discovery cycle (shared runLoop with the embed
// wiring). The graph-type note above applies identically (NodeIDsByEmbedGap
// short-circuits server-side).
func (c *collector) runEmbedLoop(ctx context.Context) {
	c.runLoop(ctx, loopAxis{
		axis:           "embed",
		lastGen:        &c.lastEmbedGen,
		drainedAtEpoch: &c.embedDrainedAtEpoch,
		relSize:        c.cfg.EmbedChannelSizeOrDefault(),
		backoff:        newErrBackoff(c.cfg.ErrBackoffBaseOrDefault(), c.cfg.ErrBackoffMaxOrDefault()),
		wake:           c.embedWake,
		push: func(ctx context.Context, item *knowledgev1.PipelineScanItem, release chan<- string) bool {
			select {
			case <-ctx.Done():
				return false
			case c.embedCh <- EmbedWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				EmbedText: item.GetEmbedText(),
				Release:   release, Backend: c.client,
			}:
				return true
			}
		},
	})
}
