// SPDX-License-Identifier: Apache-2.0

// loop_pass.go holds the SHARED reflection-pass body and its per-pass helpers:
// runPass (cluster detection + scoped-or-full DeGroot + watermark persistence),
// the quiet-tick gate, the scoped-pass accounting log, and the node-map fetch.
// Both callers live elsewhere — the hourly tick in loop.go
// (runBackgroundPropagation) and the manual user lever in backstop.go
// (ForceFullPass) — and neither owns the single-flight guard or the backstop
// decision, only this body does the work. Split out of loop.go, which was at the
// file-length cap; nothing moved changed shape.

package thought

import (
	"context"
	"errors"
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// runPass is the shared reflection-pass body: cluster detection + scoped (or, when
// forceFull, full-corpus) DeGroot propagation + watermark persistence. Both the
// hourly tick (runBackgroundPropagation, forceFull from decideBackstopForce) and the
// manual operator lever (ForceFullPass, forceFull pinned true) call it with the
// single-flight guard ALREADY claimed and, on the forced path, forceFullNext ALREADY
// set — runPass owns neither the guard nor the backstop decision, only the work. A
// forced pass bypasses all three scoping gates: the quiet-skip (forceFull →
// quietTickShouldSkip never skips), the Leiden incremental scope (the caller's
// forceFullNext → runClusterDetection skips rehydrate, full branch), and the DeGroot
// closure scope (dirtySeed=nil below). A completed forced pass advances + persists
// lastFullPass via recordForcedFullPass. Returns the result so the manual lever can
// render a summary; the tick ignores it.
func (p *PropagationLoop) runPass(ctxProbe context.Context, forceFull bool) (PropagationResult, error) {
	currentGen, probeOK, skip := p.quietTickShouldSkip(ctxProbe, forceFull)
	if skip {
		return PropagationResult{}, nil
	}

	// Per-tick budget sized for the REAL ~6k-thought corpus, not the old 10-row
	// cap. Each tick drains the full corpus via runClusterDetection AND
	// RunPropagationScoped, both calling fetchAdjacency("all"). The per-thought
	// session-sibling traversal that once dominated this cost is GONE — session
	// adjacency now comes from ONE bulk EdgeKGContains read regardless of N
	// (deriveSessionSiblings), so the per-tick wire cost is a handful of bulk reads
	// plus the paged node browse, not a per-thought RPC fan-out. This OUTER budget
	// brackets runClusterDetection (its own ctx below) AND RunPropagationScoped, so
	// it MUST be >= the inner budget or the inner is capped by whatever outer time
	// remains; 6m outer >= 5m inner. The loop is HOURLY (PropagationInterval), so a
	// multi-minute tick is well within cadence — this is a background goroutine, no
	// user-facing latency. The loud WARN below remains a safety rail on a
	// pathologically large corpus, not the expected steady state.
	//
	// The compute ctx derives from p.baseCtx (the cooperative daemon-stop drain), so a
	// daemon Stop (baseCancel) aborts an in-flight pass — INCLUDING a manual
	// force_full — at the next RPC boundary plus the compute-stage ctx.Err() gate
	// below. This is intentional: do NOT change this back to context.Background()
	// to "protect" a manual pass; the manual lever coalesces/retries and must not
	// outlive the loop.
	ctx, cancel := context.WithTimeout(p.baseContext(), 6*time.Minute)
	defer cancel()
	start := time.Now()

	// Refresh the resident thought-corpus cache BEFORE detection so every rewired
	// consumer reads a fresh Snapshot() this pass. Reached only on a non-quiet tick
	// (the quiet gate returned above), so a quiet tick issues ZERO CorpusDelta calls.
	// Nil-tolerant: a degraded loop (no cache/scanner) is a no-op and consumers stay
	// on the full-drain path.
	p.refreshCorpusCache(ctx)

	// ONE read memo for the WHOLE pass, built AFTER the refresh so it delegates to an
	// already-warm cache. Its LIFETIME is exactly this function call — a local, never
	// stored on the loop — so a pass can never serve another pass's snapshot and
	// there is nothing to invalidate. Detection and propagation both take it, which
	// is the entire point: the full-corpus reads happen once per pass instead of
	// three-to-five times.
	pr := newPassReads(p)

	// Every pass triggers a cluster detection. No conditional guard — the trigger
	// semantics are deliberately simple per OQ1 lock (one tick = one detection +
	// one propagation; the manual lever is the same single detection + propagation).
	slog.Debug("thought: runPass — triggering cluster detection")
	p.runClusterDetectionWith(pr)

	// Compute-stage cancellation gate (bind-first startup): Leiden (above) and DeGroot
	// (RunPropagationScoped below) are CPU-bound and run uninterrupted between
	// RPCs. A baseCancel observed after the Leiden stage short-circuits HERE, before
	// the multi-minute DeGroot stage starts, so a daemon Stop bounds the post-cancel
	// compute tail to one in-progress stage rather than both.
	if err := ctx.Err(); err != nil {
		return PropagationResult{}, err
	}

	// Read the per-tick state runClusterDetection just produced: the dirty seed
	// (nil on a cold-start full pass → full propagation) and the max-UpdatedAt
	// watermark over the full fresh browse.
	p.mu.Lock()
	profile := p.lastProfile
	dirtySeed := p.lastDirtySeed
	tickWatermark := p.lastWatermark
	p.mu.Unlock()

	// (c) DEGROOT FORCE — THE CRITICAL FIX: on a forced backstop tick, pass
	// dirtySeed=nil so RunPropagationScoped recomputes EVERY component. On a clean
	// corpus the per-tick seed runClusterDetection derives is EMPTY but non-nil
	// (map[string]bool{}); dirtyComponentClosure(emptySeed, …) returns ZERO
	// components, so a forced tick would run a full Leiden pass yet a NO-OP DeGroot
	// recompute — violating "exact full-corpus path regardless of dirty state". Nil
	// (not the empty seed) is what makes DeGroot rerun the whole corpus.
	if forceFull {
		dirtySeed = nil
	}

	// RunPropagationScoped needs nodeByID for cluster_id resolution under
	// personality scalars AND for the carry-forward/diff of untouched components.
	// Skip the bulk hydrate when profile is nil (no personality adjustment; the
	// diff then keeps every row, preserving prior behavior). dirtySeed scopes the
	// DeGroot recompute to the closure on a warm tick; nil ⇒ full pass.
	result, err := RunPropagationScoped(ctx, p.gc, profile, p.fetchNodeMap(ctx, pr, profile), dirtySeed, pr) // per-pass memo.
	if err != nil {
		// LOUD degradation: a per-tick deadline means the corpus is larger than
		// the budget — report how many thoughts were fetched before the cap so a
		// truncated pass is never mistaken for a complete one. result carries
		// ThoughtsProcessed (the corpus size fetched) even on the cancelled path.
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("thought: propagation tick budget exceeded — reflected fewer than the full corpus; "+
				"writeback skipped this tick (per-tick budget exceeded)",
				"thoughts_fetched", result.ThoughtsProcessed,
				"budget", (6 * time.Minute).String(),
				"elapsed", time.Since(start).Round(time.Millisecond))
			return result, err
		}
		slog.Warn("background propagation failed", "error", err)
		return result, err
	}
	if result.ThoughtsProcessed > 0 {
		// Log convergence PER COMPONENT — components_converged + non_converged count,
		// never a bare global converged flag (one slow clique must not mask the
		// converged majority).
		slog.Info("propagation complete",
			"thoughts", result.ThoughtsProcessed,
			"components", result.Components,
			"iterations", result.Iterations,
			"components_converged", result.ComponentsConverged,
			"non_converged", len(result.NonConverged)+result.NonConvergedOmitted,
			"duration", time.Since(start).Round(time.Millisecond))
	}

	// LOUD ACCOUNTING (ticket mandate): report the scoped pass's actuals against
	// the full-pass equivalent. The avoided cost is HONEST: (a) the retired 2N
	// per-thought session-sibling traversal — now ONE bulk EdgeKGContains read
	// regardless of N; (b) the skipped DeGroot/Leiden recompute over untouched
	// components (carry-forward); (c) the O(N)→O(changed) writeback rows via
	// diffMetadataUpdates. It is NOT a claim of avoided EDGE reads — the full edge
	// read still runs every tick. full_pass_equivalent is what an unscoped pass
	// would have spent on those terms (2*N sibling traverses + recompute over all
	// components + 2*N writeback rows).
	p.logScopedPassAccounting(result, dirtySeed)

	// COMPLETED pass → persist the start-of-pass reflect gen as the new gen
	// watermark so the NEXT quiet tick can skip. Reached only on the non-error,
	// non-budget-exceeded path (the DeadlineExceeded/error arms return early WITHOUT
	// persisting, so a truncated pass never advances the watermark and the next
	// tick re-runs). Persist only when the probe yielded a real gen — a failed
	// probe (currentGen==0) writes nothing, so the next tick still runs.
	if probeOK && currentGen != 0 {
		if err := writeLastReflectedGen(ctxProbe, p.gc, currentGen); err != nil {
			slog.Warn("thought: failed to persist last-reflected gen watermark", "err", err, "gen", currentGen)
		}
	}

	// COMPLETED pass → persist the max-UpdatedAt watermark (over the FULL fresh
	// per-tick browse, NOT the seed/closure) so next tick's dirty-seed cutoff
	// reflects every node observed this pass — including externally-changed
	// untouched nodes the loop did not write. The store's AddNode preserves
	// UpdatedAt on equal-value writes, and diffMetadataUpdates drops unchanged rows
	// client-side, so the loop's OWN writeback does not re-seed the next tick.
	if tickWatermark != 0 {
		if err := writeLastReflectedWatermark(ctxProbe, p.gc, tickWatermark); err != nil {
			slog.Warn("thought: failed to persist last-reflected UpdatedAt watermark", "err", err, "watermark", tickWatermark)
		}
	}

	// COMPLETED FORCED PASS → advance lastFullPass and persist it so the backstop
	// cadence restarts from now. Reached ONLY on the non-error, non-budget-exceeded
	// path (the DeadlineExceeded/error arms return early WITHOUT advancing), so a
	// TRUNCATED forced pass leaves lastFullPass unchanged and the NEXT tick re-forces
	// — cheap post-456 and observable via the budget-exceeded WARN + this forced log.
	// This mirrors the writeLastReflectedGen placement above (persist only on
	// completion). The all-or-nothing "persist only on completion, else re-force"
	// semantics are deliberate — no partial-progress watermark for the forced pass.
	if forceFull {
		p.recordForcedFullPass(ctxProbe)
	}
	return result, nil
}

// quietTickShouldSkip probes the reflect dirty-gen before any drain and reports
// whether the tick may be skipped because no reflection-relevant write landed since
// the last completed pass (re-running would only reproduce the prior result). The
// probe is ONE PipelineScan; the watermark is one by-id read. Returns the
// start-of-pass gen + probeOK for the caller's completion-time watermark write.
//
// Safety rails (each a verified criterion):
//   - a forced backstop tick (forceFull) → DO NOT skip (the backstop must run the
//     full pass even on a quiet/unchanged corpus — that is its whole point).
//   - probe failure (probeOK==false) → DO NOT skip (degrade to running; never skip
//     on an unreachable probe).
//   - first run / no persisted watermark (lastReflectedGen==0) → DO NOT skip (cold
//     start always reflects once).
//   - the persisted gen is captured at pass START, so a write landing mid-pass
//     makes the NEXT tick run (the ticket's run-on-bump invariant).
func (p *PropagationLoop) quietTickShouldSkip(ctx context.Context, forceFull bool) (currentGen uint64, probeOK, skip bool) {
	probe, _ := p.gc.(reflectProbe)
	currentGen, probeOK = probeReflectGen(ctx, probe)
	lastReflectedGen := readLastReflectedGen(ctx, p.gc)
	if !forceFull && probeOK && lastReflectedGen != 0 && currentGen == lastReflectedGen {
		slog.Info("thought: reflection tick SKIPPED — reflect gen unchanged since last pass (quiet tick)",
			"gen", currentGen)
		return currentGen, probeOK, true
	}
	return currentGen, probeOK, false
}

// logScopedPassAccounting emits the loud per-warm-pass accounting line: the dirty
// seed size, the closure size (the nodes actually recomputed), the components
// touched, and the full-pass-equivalent cost of the terms the scoping avoided.
// On a cold-start full pass (nil seed) closure_size == the full corpus and the
// line records that scoping was off this tick.
func (p *PropagationLoop) logScopedPassAccounting(result PropagationResult, dirtySeed map[string]bool) {
	closureSize := len(result.ValenceChanges) // nodes whose propagated_* was recomputed this pass.
	// full_pass_equivalent: what an UNSCOPED pass would have spent on the avoided
	// terms — 2N sibling traverses (retired) + recompute over all N nodes + 2N
	// writeback rows. N is the processed corpus size.
	n := result.ThoughtsProcessed
	slog.Info("thought: scoped reflection pass accounting",
		"dirty_seed_size", len(dirtySeed),
		"closure_size", closureSize,
		"total_components", result.Components,
		"rpcs_issued", "node-browse + adjacency-edges + bulk-kgcontains + charges + 1 diffed writeback (O(1) in N)",
		"full_pass_equivalent", "retired 2N session-sibling traversals + recompute over all components + O(N) writeback rows",
		"scoped", dirtySeed != nil,
		"corpus_size", n)
}

// fetchNodeMap pulls the full thought node map only when profile is
// non-nil (RunPropagationScoped needs cluster_id for personality scalars and the
// persisted propagated_* for the untouched-component carry-forward/diff).
// Skipping the hydrate in the nil-profile case avoids an unnecessary
// gc.Call on a no-personality path (the diff then keeps every row — cold case).
//
// When the resident corpus cache is warm the node map comes off the snapshot (the
// full Node payloads — with cluster_id + propagated_* — are already in hand),
// eliminating both the listAllThoughtIDs browse and the wire hydrate: memoCorpusNodes
// serves resident ids from the cache and hydrates only the residual. A cold/degraded
// loop still pays the id browse, and its hydrate is then the whole set.
//
// src is the per-pass read memo, so on a warm pass this IS the map the detection
// stage already built rather than a second projection of the same snapshot. The map
// may be WIDER than the thought set (see memoCorpusNodes); every consumer —
// currentPropagatedAccessor and buildComponentMatrix — indexes it by id.
func (p *PropagationLoop) fetchNodeMap(ctx context.Context, src CorpusSource, profile *PersonalityProfile) map[string]*knowledgev1.Node {
	if profile == nil {
		return nil
	}
	if nodes, warm := p.CorpusSnapshot(); warm {
		ids := make([]string, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.GetId())
		}
		return memoCorpusNodes(ctx, p.gc, ids, src)
	}
	ids, _ := listAllThoughtIDs(ctx, p.gc)
	return memoCorpusNodes(ctx, p.gc, ids, src)
}
