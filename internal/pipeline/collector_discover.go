// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collector_discover.go holds the per-axis pipeline_scan dispatch + the gap-id
// debug logging, split out of collector.go to keep that file under the 500-line
// context cap as the auto-heal latch accreted.

// debugLogGapItems logs the exact gap node ids + their source graph name for a
// discovery batch. Logged as "<graphName>::<nodeId>" tokens so a recurring
// re-summarize/re-embed (the same ids surfacing every collect) is directly
// visible and the gap's source layer can be compared with the writeback target
// layer. Debug level — off unless debug logging is enabled.
func debugLogGapItems(axis string, items []*knowledgev1.PipelineScanItem) {
	toks := make([]string, 0, len(items))
	for _, it := range items {
		toks = append(toks, it.GetGraphName()+"::"+it.GetNodeId())
	}
	slog.Debug("pipeline.collector: gap node ids", "axis", axis, "count", len(toks), "ids", strings.Join(toks, " "))
}

// discover decides whether this collector's (graph_type, graph_name, axis) has new
// work and, when it does, issues the Phase-2 PipelineScan detail fetch for it. axis
// must be "summary" or "embed". Returns (nil, nil) — and issues ZERO RPCs — when
// the SHARED bulk gen-poll snapshot shows the per-axis dirty-gen has not advanced
// past the per-axis watermark (last).
//
// Two-phase protocol: the per-tick dirty-gen is sampled ONCE for every
// loaded graph by the central RunGenPollLoop (genpoll.go), which writes the shared
// genSnapshot and pokes this collector's wake when its gen advances. discover reads
// that snapshot rather than self-issuing a PipelineScan just to learn the gen, so a
// no-change tick costs ZERO collector RPCs (the only per-tick RPC is the one bulk
// gen-poll). When the snapshot gen HAS advanced (or the snapshot is not yet known —
// first scan — or a backlog is still draining, in which case the watermark sits
// below the snapshot gen), discover issues the EXISTING scanGaps PipelineScan to
// pull the detail items + the authoritative gen.
//
// The watermark (last) is intentionally pinned to the floor while a backlog drains
// (items > 0): advancing it sooner would let the next tick's cheap-tick
// short-circuit while the queue still has work, starving the workers. `cached_gen`
// in the log line below stays at its floor value for the whole drain window — that
// is by design, NOT a stuck pipeline. The items count is the real progress signal.
//
// IT RETURNS THE SERVER'S gap_set_complete ALONGSIDE THE ITEMS, and it is NOT
// stored on the collector here — the axis-drained state and its concurrency belong
// to runLoop, which owns the cross-axis quiescence question this feeds.
//
// The 0-RPC no-change tick returns TRUE for it. That path fires only when the
// shared snapshot's gen equals this axis's watermark, and the watermark advances
// only on a COMPLETE EMPTY PAGE — so "nothing has changed since a scan that found
// nothing AND reported it had seen the whole set" is exactly the claim, and
// returning false there would make a quiet pipeline permanently indistinguishable
// from a busy one.
//
// THE WORD "COMPLETE" IS LOAD-BEARING, not emphasis. While the advance fired on any
// empty page, this line synthesized a completeness the server had explicitly denied:
// an arm whose statement failed returns no rows and no error, the watermark moved,
// and the next tick reported the axis drained without anyone measuring it. The
// advance site enforces the condition this sentence asserts; the two must be read
// together.
func (c *collector) discover(ctx context.Context, axis string, last *atomic.Uint64) ([]*knowledgev1.PipelineScanItem, bool, error) {
	// The ask is one LEASE for every worker on the axis — the unit a worker
	// actually takes, so a full scan page fills the pool exactly once.
	//
	// NO CLIENT-SIDE CLAMP IS APPLIED. The server clamps every scan at its own
	// per-request ceiling and reports the truncation, so clamping here would make
	// this a second authority on the same bound. At the shipped defaults the embed
	// ask is 500 x 20 = 10,000, which lands exactly ON that ceiling, so a fully
	// backlogged graph WILL come back truncated. That is correct and already
	// handled: the drain gate and idle backoff re-scan as leases complete, and the
	// watermark is deliberately pinned to its floor for the whole drain window.
	limit := c.cfg.SummaryLeaseSizeOrDefault() * c.cfg.SummaryWorkersOrDefault()
	if axis == "embed" {
		limit = c.cfg.EmbedLeaseSizeOrDefault() * c.cfg.EmbedWorkersOrDefault()
	}
	cachedGen := last.Load()

	// Phase-1 cheap-tick: consult the shared bulk-poll snapshot. When the central
	// loop has sampled this graph AND the snapshot gen matches the watermark, there
	// is no new work — return WITHOUT any PipelineScan (the 0-RPC no-change tick).
	// While a backlog drains, the watermark sits below the snapshot gen (last.Store
	// fires only on an empty fetch), so this guard does NOT fire mid-drain and the
	// detail fetch below keeps paging. nil genSnapshot (test fakes without the
	// central loop) falls through to always scan — the pre-two-phase behavior.
	if c.genSnapshot != nil {
		summaryGen, embedGen, ok := c.genSnapshot(graphKey{GraphType: c.gt, GraphName: c.name})
		snapGen := summaryGen
		if axis == "embed" {
			snapGen = embedGen
		}
		if ok && snapGen == cachedGen {
			return nil, true, nil
		}
	}

	// Phase-2 detail fetch: the gen advanced (or is unknown / mid-drain) — pull the
	// gap items via the EXISTING PipelineScan. last_seen_gen is still passed so the
	// server's own cheap-tick short-circuit stays a backstop.
	items, gen, gapSetComplete, err := scanGaps(ctx, c.client, c.gt, c.name, axis, limit, cachedGen, c.cfg.EmbedIdentity)
	if err != nil {
		// Surface the error to the caller so the loop can apply scan-error
		// backoff (#3) rather than re-firing at the base cadence. NOT complete: a
		// failed scan measured nothing.
		return nil, false, err
	}
	if gen != 0 && gen == cachedGen && len(items) == 0 {
		// The server's own cheap-tick backstop fired and served its empty page with
		// gap_set_complete already set; pass it through rather than re-deriving it.
		return nil, gapSetComplete, nil
	}
	// THE WATERMARK ADVANCES ONLY ON A COMPLETE EMPTY PAGE, and the second conjunct
	// is what makes every downstream claim about this watermark true.
	//
	// AN EMPTY PAGE IS NOT EVIDENCE OF A DRAINED AXIS. It is also what a scan
	// returns when an arm's statement FAILED — the summary leaf arm logs and returns
	// no rows with NO error reaching this client, so the failure is invisible here —
	// and what a window filled entirely with rows the server's Go gate refused
	// returns. Advancing on either latches this axis to a generation nothing has
	// actually drained.
	//
	// AND THE LATCH IS THEN SELF-CONFIRMING ON BOTH SIDES OF THE WIRE. The zero-RPC
	// cheap tick above returns complete when the snapshot gen equals this watermark,
	// and the server's own short-circuit returns complete when last_seen_gen equals
	// its dirty gen. Neither re-measures anything; both are reading back the claim
	// this line made. So an advance on an incomplete page does not merely delay a
	// scan — it manufactures a durable "this axis has no work left" out of a page
	// that measured nothing, which is exactly the false quiescence the flag exists
	// to prevent.
	if len(items) == 0 && gapSetComplete {
		last.Store(gen)
	}
	if len(items) > 0 {
		slog.Debug("pipeline.collector: discovered items",
			"graph_type", c.gt, "name", c.name, "axis", axis, "items", len(items), "server_gen", gen, "cached_gen", cachedGen)
		// Log the EXACT gap node ids + their source graph name so a recurring
		// re-summarize/re-embed (same ids every collect) is visible and the gap's
		// source layer can be compared against the writeback target.
		debugLogGapItems(axis, items)
	}
	return items, gapSetComplete, nil
}
