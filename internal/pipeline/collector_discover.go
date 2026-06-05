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
func (c *collector) discover(ctx context.Context, axis string, last *atomic.Uint64) ([]*knowledgev1.PipelineScanItem, error) {
	limit := c.cfg.SummaryBatchSizeOrDefault() * c.cfg.SummaryWorkersOrDefault()
	if axis == "embed" {
		limit = c.cfg.EmbedBatchSizeOrDefault() * c.cfg.EmbedWorkersOrDefault()
	}
	cachedGen := last.Load()
	items, gen, err := scanGaps(ctx, c.client, c.gt, c.name, axis, limit, cachedGen)
	if err != nil {
		// Surface the error to the caller so the loop can apply scan-error
		// backoff (#3) rather than re-firing at the base cadence.
		return nil, err
	}
	if gen != 0 && gen == cachedGen && len(items) == 0 {
		return nil, nil
	}
	if len(items) == 0 {
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
	return items, nil
}
