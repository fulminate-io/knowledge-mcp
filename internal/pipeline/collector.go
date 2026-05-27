// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collector is the per-graph background worker that discovers nodes
// needing summary/embed and pushes them onto the global channels. One
// collector per (GraphType, GraphName), spawned by Pipeline.RegisterGraph.
//
// Lifecycle: run() launches two parallel goroutines — runSummaryLoop and
// runEmbedLoop. Each loop is a stoplight cycle: drain releases from
// completed workers, scan eligible IDs via the pipeline_scan MCP tool,
// push every new one onto the channel, sleep one tick, repeat. The
// in-flight set prevents duplicate queueing during the worker call
// latency window; without it, a 3-sec CLI failure would let 12+ ticks
// re-queue the same nodes (the bug that produced 25× counter inflation
// in Apr 2026).
//
// In-flight cleanup uses TWO mechanisms (defense in depth):
//  1. Worker writes NodeID to the per-collector release channel after
//     processing each item; collector drains releases at the start of
//     each cycle and removes those IDs from in-flight.
//  2. After scan, the collector intersects in-flight with the current
//     eligible set. Anything no longer eligible (success → Summary
//     populated, terminal → marker stamped) gets pruned.
//
// run() exits when ctx is canceled (by Pipeline.UnregisterGraph or
// Pipeline.Stop).
//
// Post-Phase-4: collector reads via the pipeline_scan MCP tool against
// the shared WireClient rather than calling store.NodeIDsBySummaryGap /
// store.NodeIDsByEmbedGap in-process. The dirty-gen cheap-tick skip
// remains — the scan response carries the server-side dirty_gen, so
// the collector still skips the next tick when the gen hasn't advanced.
type collector struct {
	gt        kgtypes.GraphType
	name      string
	cfg       Config
	summaryCh chan<- SummaryWork
	embedCh   chan<- EmbedWork
	metrics   *metricsState
	client    WireClient

	// lastSummaryGen / lastEmbedGen cache the server-side dirty-gen
	// value from the most recent scan that returned zero eligible IDs.
	// On the next tick, if the scan returns the same gen, we skip
	// pushing anything (the scan call itself was still cheap on the
	// server — base-graph dirty gen check + early-return).
	lastSummaryGen atomic.Uint64
	lastEmbedGen   atomic.Uint64
}

// newCollector constructs a collector. The actual goroutine launch is
// done by Pipeline.RegisterGraph so the WaitGroup accounting stays
// centralized.
func newCollector(gt kgtypes.GraphType, name string, cfg Config, summaryCh chan<- SummaryWork, embedCh chan<- EmbedWork, metrics *metricsState, client WireClient) *collector {
	return &collector{
		gt:        gt,
		name:      name,
		cfg:       cfg,
		summaryCh: summaryCh,
		embedCh:   embedCh,
		metrics:   metrics,
		client:    client,
	}
}

// run launches two parallel ticker loops — one for summary dispatch, one
// for embed dispatch. Splitting them prevents the embed loop from being
// starved when the summary channel is saturated (the per-graph collector
// blocks on summary push; without the split the same goroutine never
// reaches dispatchEmbeds).
//
// Exits on ctx.Done.
func (c *collector) run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.runSummaryLoop(ctx) }()
	go func() { defer wg.Done(); c.runEmbedLoop(ctx) }()
	wg.Wait()
}

// runSummaryLoop is the stoplight discovery cycle for summary work.
// Each iteration: drain releases from completed workers, scan eligible
// IDs via pipeline_scan, push every new one onto the channel (blocking
// on channel-full = backpressure), sleep one tick, repeat. The in-flight
// set prevents re-queueing items that workers haven't yet finished.
func (c *collector) runSummaryLoop(ctx context.Context) {
	inFlight := make(map[string]struct{})
	release := make(chan string, c.cfg.SummaryChannelSizeOrDefault())

	for {
		drainReleases(release, inFlight)

		// No client-side graph-type gate (FUL-305): the server returns empty
		// items for non-summarizable graph types (NodeIDsBySummaryGap short-
		// circuits on gt.Summarizable() server-side), so a redundant client
		// gate here would only duplicate that decision.
		eligible := c.discover(ctx, "summary", &c.lastSummaryGen)
		pruneInFlightItems(inFlight, eligible)

		for _, item := range eligible {
			if _, dup := inFlight[item.GetNodeId()]; dup {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case c.summaryCh <- SummaryWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				SummarizeText: item.GetSummarizeText(), Release: release,
			}:
				inFlight[item.GetNodeId()] = struct{}{}
			}
		}

		if !c.sleepTick(ctx) {
			return
		}
	}
}

// runEmbedLoop mirrors runSummaryLoop for the embed system.
func (c *collector) runEmbedLoop(ctx context.Context) {
	inFlight := make(map[string]struct{})
	release := make(chan string, c.cfg.EmbedChannelSizeOrDefault())

	for {
		drainReleases(release, inFlight)

		// No client-side graph-type gate (FUL-305): the server returns empty
		// items for non-embeddable graph types (NodeIDsByEmbedGap short-circuits
		// on gt.Embeddable() server-side), so a redundant client gate here would
		// only duplicate that decision.
		eligible := c.discover(ctx, "embed", &c.lastEmbedGen)
		pruneInFlightItems(inFlight, eligible)

		for _, item := range eligible {
			if _, dup := inFlight[item.GetNodeId()]; dup {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case c.embedCh <- EmbedWork{
				GraphType: c.gt, GraphName: item.GetGraphName(), NodeID: item.GetNodeId(),
				EmbedText: item.GetEmbedText(), Release: release,
			}:
				inFlight[item.GetNodeId()] = struct{}{}
			}
		}

		if !c.sleepTick(ctx) {
			return
		}
	}
}

// drainReleases empties the release channel non-blocking, removing each
// released ID from the in-flight set. Called at the start of every cycle
// so the next discovery query sees an accurate in-flight picture.
func drainReleases(release <-chan string, inFlight map[string]struct{}) {
	for {
		select {
		case id := <-release:
			delete(inFlight, id)
		default:
			return
		}
	}
}

// pruneInFlightItems intersects inFlight with the just-discovered eligible
// set: any ID no longer eligible is removed from in-flight. Defense in
// depth against missed releases (e.g., worker panic before release write):
// once Summary is populated or a marker lands, the scan no longer returns
// the ID, so it falls out of in-flight here even if the release was
// dropped.
func pruneInFlightItems(inFlight map[string]struct{}, eligible []*knowledgev1.PipelineScanItem) {
	if len(inFlight) == 0 {
		return
	}
	eligibleSet := make(map[string]struct{}, len(eligible))
	for _, it := range eligible {
		eligibleSet[it.GetNodeId()] = struct{}{}
	}
	for id := range inFlight {
		if _, still := eligibleSet[id]; !still {
			delete(inFlight, id)
		}
	}
}

// sleepTick waits one configured tick or returns false on ctx cancel.
func (c *collector) sleepTick(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(c.cfg.TickOrDefault()):
		return true
	}
}

// discover issues one pipeline_scan call for this collector's
// (graph_type, graph_name, axis) and updates the per-axis dirty-gen
// cache. axis must be "summary" or "embed". Returns empty + skip when
// the server-reported dirty_gen has not advanced since the last
// empty-result scan.
//
// Single-axis dispatch lives here rather than duplicated in the two
// loops so the dirty-gen cache update happens in exactly one place per
// axis (the test fixture's call counter can assert exact RPC counts).
//
// The cache is intentionally pinned to the floor while a backlog drains
// (items > 0): advancing it sooner would let the next tick's cheap-tick
// short-circuit while the queue still has work, starving the workers.
// `cached_gen` in the log line below stays at its floor value for the
// whole drain window — that is by design, NOT a stuck pipeline. The
// items count is the real progress signal.
func (c *collector) discover(ctx context.Context, axis string, last *atomic.Uint64) []*knowledgev1.PipelineScanItem {
	limit := c.cfg.SummaryBatchSizeOrDefault() * c.cfg.SummaryWorkersOrDefault()
	if axis == "embed" {
		limit = c.cfg.EmbedBatchSizeOrDefault() * c.cfg.EmbedWorkersOrDefault()
	}
	cachedGen := last.Load()
	items, gen, err := scanGaps(ctx, c.client, c.gt, c.name, axis, limit, cachedGen)
	if err != nil {
		slog.Debug("pipeline.collector: scan failed; will retry next tick",
			"graph_type", c.gt, "name", c.name, "axis", axis, "error", err)
		return nil
	}
	if gen != 0 && gen == cachedGen && len(items) == 0 {
		return nil
	}
	if len(items) == 0 {
		last.Store(gen)
	}
	if len(items) > 0 {
		slog.Debug("pipeline.collector: discovered items",
			"graph_type", c.gt, "name", c.name, "axis", axis, "items", len(items), "server_gen", gen, "cached_gen", cachedGen)
	}
	return items
}
