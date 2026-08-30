// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
)

// worker_embed_lease.go turns ONE embed lease into N provider calls and ONE
// writeback.
//
// WHY THE STRIDES ARE SEQUENTIAL, and why that is not a missed optimisation.
// Total provider concurrency is EmbedWorkersOrDefault() workers with one
// in-flight call each — exactly what it was before the lease existed. Running a
// lease's strides in parallel would multiply provider concurrency by the stride
// count and invalidate both the RPM pacer and the breaker's window, which is
// what the lease was careful NOT to change.
//
// WHY THE LEASE IS NOT HANDED TO THE EMBEDDER AS ONE CALL. Passing 500 items in
// one call routes through the provider client's own packing loop, which returns
// on the first failing pack and DISCARDS every already-billed pack before it. A
// stride per provider call means a transient failure on stride 4 of 5 loses
// stride 4 and nothing else.

// processEmbedLeaseGroup spends one lease: it walks items in strides of the
// provider-call cap, calls embedGroupOnce per stride, merges what succeeded, and
// issues exactly ONE writeback for the whole lease.
//
// STRIDE FAILURE PARTITIONING. A stride that fails contributes NOTHING to the
// merge and the loop CONTINUES. The only early exit is ctx cancellation. That is
// deliberate on all three counts: the single writeback carries every SUCCEEDED
// stride, so one bad stride costs one stride's work rather than the lease's;
// handleEmbedderError has already marked or backed off that stride's ids by its
// own unchanged rules, so the failure model does not move with the lease size;
// and a failed stride's ids are simply absent from the writeback, which leaves
// them embed-eligible for the next scan exactly as a dropped batch is today.
// Do NOT turn a stride failure into a lease abort — that would make the lease
// size a failure-amplification factor, which is the one thing the lease must not
// become.
//
// The marker writes are NOT folded in here. handleEmbedderError and
// markStuckEmbedItems keep issuing their own writebacks: that is the failure
// path, rare in steady state, and coupling it to the lease size would couple the
// failure model to the batching decision.
func processEmbedLeaseGroup(ctx context.Context, p *Pipeline, key groupKey, items []EmbedWork) {
	gk := key.Key
	be := backendOr(p, key.Backend)
	stride := max(p.cfg.EmbedBatchSizeOrDefault(), 1)
	mergedVectors := make(map[string][]byte, len(items))
	mergedIDs := make([]string, 0, len(items))
	strides := 0
	for start := 0; start < len(items); start += stride {
		if ctx.Err() != nil {
			slog.Debug("pipeline.embed: lease abandoned mid-stride on context cancellation",
				"graph_type", gk.GraphType, "graph_name", gk.GraphName, "written_so_far", len(mergedIDs))
			break
		}
		end := min(start+stride, len(items))
		vectors, ids := embedGroupOnce(ctx, p, key, items[start:end])
		strides++
		for _, id := range ids {
			if v, ok := vectors[id]; ok {
				mergedVectors[id] = v
			}
		}
		mergedIDs = append(mergedIDs, ids...)
	}
	if len(mergedIDs) == 0 {
		return
	}
	slog.Debug("pipeline.embed: lease complete — one writeback",
		"graph_type", gk.GraphType, "graph_name", gk.GraphName,
		"lease_items", len(items), "strides", strides, "written_ids", len(mergedIDs))
	writeEmbedResults(ctx, p, be, gk, mergedVectors, mergedIDs)
}
