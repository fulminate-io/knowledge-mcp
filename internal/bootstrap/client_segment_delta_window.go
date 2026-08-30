// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_delta_window.go owns the DELTA WINDOW: pulling one graph's
// bounded window, resolving where that window starts, and part two of the merge's
// two-part commit.
//
// SPLIT FROM client_segment_reconcile.go, which keeps the pass that DRIVES these —
// the periodic loop, the scoping, the shutdown drain. The two were one file until
// it crossed the size cap, and the seam between them is the natural one: the pass
// decides WHICH graphs to look at, and this decides what one window means.

// segmentGraphRef names one segment-bearing graph instance. It is the key the
// reconcile pass enumerates and the key the per-graph delta horizons are held under.
type segmentGraphRef struct {
	gt   kgtypes.GraphType
	name string
}

// mergePending is what one delta pull produced and what the caller must commit ONLY
// after the drain that made it durable.
type mergePending struct {
	// Horizon is the server-served horizon this window was pulled up to.
	Horizon int64
	// Merged is how many live items were handed to the local segments.
	Merged int
	// Pull reports whether a pull happened at all this pass. A graph with no horizon
	// of any kind pulls nothing, and a commit must not advance anything for it.
	Pull bool
	// RetentionFloorNanos and ScanFromNanos are the two watermark values the pass
	// SENT, carried back from tools.SegmentDelta rather than recomputed here: the
	// point of logging them is to show the caller's horizon beside the values that
	// actually went out, and a second reading of the same operands would show
	// neither.
	RetentionFloorNanos int64
	ScanFromNanos       int64
}

// consumeSegmentDelta pulls one graph's bounded delta window and lands BOTH halves —
// the deletes on the local pool and the live items into the local segments — unless
// deltaScope excludes it this pass (see reconcileSegmentCoverageScoped).
//
// WHERE THE HORIZON COMES FROM, in order:
//  1. the in-memory horizon carried by this process;
//  2. otherwise the DURABLE merge horizon;
//  3. otherwise the durable REBUILD watermark;
//  4. otherwise NO PULL AT ALL.
//
// CLAUSE 4 IS THE LOAD RULE AND IT IS DELIBERATE. A zero-watermark scan of this axis
// is the full vectored corpus, so seeding an unseeded graph from zero would make
// every process pay one full-corpus read per such graph, and merging that window
// would be the whole-corpus rebuild this path exists to replace. An unseeded graph
// therefore pulls nothing and waits for the coverage backstop's rotation, which seeds
// its horizon on both of that arm's writing exits — bounded at one rotation, once per
// machine, against a full-corpus read on EVERY boot today.
//
// THE CONSEQUENCE, stated rather than hidden: until a graph's horizon is seeded it
// learns no server-side deletes from this feed either. Hard deletes never rode this
// feed at all, so nothing about that story changes.
//
// Best-effort throughout, like every other arm of this pass: a failure WARNs and the
// pass moves on. A window that does not land this tick lands on the next one, because
// the caller only commits the horizon after a successful drain.
func (c *client) consumeSegmentDelta(
	ctx context.Context, g segmentGraphRef, deltaScope map[segmentGraphRef]struct{},
) mergePending {
	if deltaScope != nil {
		if _, signaled := deltaScope[g]; !signaled {
			return mergePending{}
		}
	}
	scanner := c.PipelineScanner()
	if scanner == nil {
		return mergePending{} // degraded client — no scan seam to read the feed through.
	}

	since, ok := c.mergeHorizonFor(g)
	if !ok {
		slog.Debug("bootstrap: segment delta has no horizon for this graph yet — pulling nothing until the backstop seeds one",
			"graph_type", g.gt, "name", g.name)
		return mergePending{}
	}

	out, err := tools.MergeSegmentDelta(
		ctx, scanner, c.SegmentShipper(), c.segmentMgr, c.segmentMgr, g.gt, g.name, since)
	if err != nil {
		// THE ARM SPLITS IN TWO, and until this split it was one. Re-reading the
		// window next pass is RIGHT for a transient failure and WRONG for a REFUSAL,
		// which would repeat forever — see rebuildBehindWindowGraph for why only a
		// full rebuild recovers. connect.CodeOf UNWRAPS, which is why it is used
		// rather than a type assertion: MergeSegmentDelta wraps with %w. The CODE is
		// the entire seam contract; the message is operator-only and unparsed.
		// THE DECLINE ARM COMES FIRST, and the order is load-bearing: this error is
		// raised locally and never reaches the wire, so it carries no connect code and
		// the refusal arm below would not classify it. It is the no-horizon-no-pull
		// rule one operand wider — no horizon OR no floor, no pull — and it returns
		// the same no-commit result, so the window is not advanced past.
		if errors.Is(err, tools.ErrRetentionFloorUnreadable) {
			c.rebuildUnreadableFloorGraph(ctx, g, since)
			return mergePending{}
		}
		if connect.CodeOf(err) == connect.CodeOutOfRange {
			c.rebuildBehindWindowGraph(ctx, g, since)
			return mergePending{}
		}
		slog.Warn("bootstrap: segment delta merge failed (continuing; the horizon is not advanced, so the window is re-read next pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return mergePending{}
	}
	if out.Learned > 0 {
		slog.Info("bootstrap: segment delta landed server-side deletes on the local pool",
			"graph_type", g.gt, "name", g.name, "learned", out.Learned, "carried", out.Carried)
	}
	if out.Merged > 0 {
		// local_slowest_consumer_floor_nanos is THIS CLIENT's slowest local consumer
		// position, produced by retentionFloorFor (cmd/knowledge/internal/tools/retention_floor.go:77).
		// It is NOT the server's reap floor, and reading it as one has already caused
		// one misdiagnosis.
		slog.Info("bootstrap: segment delta merged co-worker updates into the local segments",
			"graph_type", g.gt, "name", g.name, "merged", out.Merged, "since", since,
			"local_slowest_consumer_floor_nanos", out.RetentionFloorNanos, "scan_from_nanos", out.ScanFromNanos)
	} else {
		// THE CONVERGED STEADY STATE IS THE STATE AN OPERATOR HAS TO DIAGNOSE, and
		// until now it logged nothing at all — which is how a delta arm that re-served
		// the same rows every pass stayed invisible across two investigations. The
		// record is guarded on a pull having happened, not on an error, so a pass that
		// merged nothing still says what it asked for.
		//
		// local_slowest_consumer_floor_nanos is THIS CLIENT's slowest local consumer
		// position, produced by retentionFloorFor (cmd/knowledge/internal/tools/retention_floor.go:77).
		// It is NOT the server's reap floor.
		slog.Debug("bootstrap: segment delta pass merged nothing",
			"graph_type", g.gt, "name", g.name, "since", since,
			"local_slowest_consumer_floor_nanos", out.RetentionFloorNanos, "scan_from_nanos", out.ScanFromNanos)
	}
	return mergePending{
		Horizon: out.Horizon, Merged: out.Merged, Pull: true,
		RetentionFloorNanos: out.RetentionFloorNanos, ScanFromNanos: out.ScanFromNanos,
	}
}

// mergeHorizonFor resolves the window's start for one graph, walking the three seed
// sources in order. ok=false is clause 4 — no horizon of any kind, so no pull.
func (c *client) mergeHorizonFor(g segmentGraphRef) (int64, bool) {
	c.deltaHorizonMu.Lock()
	since, carried := c.deltaHorizon[g]
	c.deltaHorizonMu.Unlock()
	if carried {
		return since, true
	}

	// The durable merge horizon: what the last landed merge for this graph was
	// scanned up to, which survives a restart precisely so the next process re-merges
	// one bounded window rather than the corpus.
	if h, err := c.segmentMgr.LoadMergeWatermark(g.gt, g.name); err != nil {
		slog.Warn("bootstrap: segment delta could not read the merge horizon (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, false
	} else if h > 0 {
		return h, true
	}

	// The durable rebuild watermark: the last horizon a landed rebuild published up
	// to. A graph that has landed one has a genuine bound to read from.
	w, _, err := c.segmentMgr.LoadRebuildState(g.gt, g.name)
	if err != nil {
		slog.Warn("bootstrap: segment delta could not read the rebuild state to seed its horizon (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, false
	}
	if w > 0 {
		return w, true
	}
	return 0, false
}

// commitMergeWatermark is part TWO of the merge's two-part commit, and the caller
// runs it ONLY on the branch where the drain that shipped the merge succeeded. A
// skipped commit leaves the horizon where it was, so the same window is re-pulled next
// tick and the same items are re-merged — idempotent, because the add is keyed by id.
func (c *client) commitMergeWatermark(g segmentGraphRef, pending mergePending) {
	if !pending.Pull || pending.Horizon <= 0 {
		return
	}
	c.deltaHorizonMu.Lock()
	if c.deltaHorizon == nil {
		c.deltaHorizon = make(map[segmentGraphRef]int64)
	}
	advanced := pending.Horizon > c.deltaHorizon[g]
	if advanced {
		c.deltaHorizon[g] = pending.Horizon
	}
	c.deltaHorizonMu.Unlock()
	if !advanced {
		return
	}
	if err := c.segmentMgr.SaveMergeWatermark(g.gt, g.name, pending.Horizon); err != nil {
		slog.Warn("bootstrap: segment delta could not persist the merge horizon (continuing; the window is re-read next process)",
			"graph_type", g.gt, "name", g.name, "error", err)
	}
}
