// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// collector_heal.go holds the auto-heal embed-drain edge-latch. It lives in its
// own file to keep collector.go under the 500-line context cap, and mirrors the
// maybeQuiescenceFlush shape in collector.go.

// ErrHealDisarmed is the sentinel the bootstrap-supplied heal closure returns when
// the per-graph heal breaker has latched this graph disarmed. It lives HERE (not in
// bootstrap) because maybeHealCheck consumes it and the import direction is
// bootstrap→pipeline — pipeline cannot import bootstrap. maybeHealCheck errors.Is-
// matches it to set the collector's healDisarmed flag and stop re-arming the per-wake
// heal check, WITHOUT WARNing: a breaker disarm is a deliberate terminal state, not a
// heal failure.
var ErrHealDisarmed = errors.New("pipeline: auto-heal disarmed for this graph (heal breaker latched)")

// noteWakeRearm is the ARMING half of the runLoop-local healArmed latch, and the
// counterpart to maybeHealCheck below, which consumes it. It returns the next
// healArmed value: a collect-wake on the embed axis arms the check, and every
// other cycle leaves the caller's latch UNCHANGED.
//
// A breaker-disarmed graph (healDisarmed latched) stops re-arming so the closure
// is no longer invoked per wake — ending the self-sustaining heal re-fire.
//
// Re-fire observability (kept): every embed-wake re-arm is one turn of the
// auto-heal cadence. Logging it makes the re-fire source (organic gen-advance
// wake vs the heal/flush's own writes waking the loop) visible per cycle.
func (c *collector) noteWakeRearm(ax loopAxis, byWake, armed bool) bool {
	if !byWake || ax.axis != "embed" || c.healDisarmed.Load() {
		return armed
	}
	slog.Debug("pipeline.collector: embed-wake re-armed auto-heal latch",
		"graph_type", c.gt, "name", c.name)
	return true
}

// maybeHealCheck implements the auto-heal embed-drain edge-latch. It is
// the consumption half of the runLoop-local healArmed latch (armed on a
// collect-wake by the embed loop) and returns the next healArmed value:
//   - while the gap has items or in-flight work it returns true (STAY armed — the
//     drain has not completed yet, so the heal must not fire mid-backlog).
//   - if the latch is NOT armed, the axis is not embed, or no heal closure is
//     wired (test fakes / no segment manager) it returns `armed` UNCHANGED — a
//     no-op that neither fires nor disturbs the latch.
//   - on the armed embed drain-complete edge (empty scan AND nothing in flight AND
//     the latch set AND a heal closure wired) it fires c.healIfSegmentless ONCE
//     and returns false (DISARM) so the post-drain idle scans do not re-fire it.
//     Fires once per collect (the collect armed the latch; this drain consumes it).
//
// A heal error only WARNs (best-effort, mirroring maybeQuiescenceFlush and the
// embed-writeback ship path): the heal closure is itself a cheap probe + a
// single-flight rebuild, and the next collect-armed drain retries. It still
// DISARMS on error — the arm is per-collect, not a retry-until-success latch; a
// transient failure self-heals on the next collect's drain.
//
// Independent of pendingSinceFlush: that latch is driven by embed WORK, but the
// already-embedded new-user case (the heal's whole reason to exist) has ZERO
// embed work, so only healArmed (collect-driven) fires for it. That is the
// load-bearing distinction the auto-heal trigger exists for.
func (c *collector) maybeHealCheck(ctx context.Context, ax loopAxis, items, inFlight int, armed bool) bool {
	if items > 0 || inFlight > 0 {
		return true
	}
	if !armed || ax.axis != "embed" || c.healIfSegmentless == nil {
		return armed
	}
	slog.Info("pipeline.collector: embed gap drained while heal-armed — auto-heal check (cheap zero-segments + coverage-ratio probe; rebuild on zero OR degraded coverage)",
		"graph_type", c.gt, "name", c.name)
	if err := c.healIfSegmentless(ctx); err != nil {
		if errors.Is(err, ErrHealDisarmed) {
			// The heal breaker latched this graph disarmed after repeated no-progress
			// rebuilds. Latch the collector's own flag so the embed-wake arm site stops
			// re-arming this closure per wake — the loop is broken until a manual
			// rebuild_segments or a restart. This is NOT a failure, so do NOT WARN.
			c.healDisarmed.Store(true)
			slog.Info("pipeline.collector: auto-heal disarmed by heal breaker — stopping per-wake heal re-arm until a manual rebuild_segments or restart",
				"graph_type", c.gt, "name", c.name)
			return false
		}
		slog.Warn("pipeline.collector: auto-heal check failed (best-effort; next collect-armed drain retries)",
			"graph_type", c.gt, "name", c.name, "error", err)
	}
	return false
}

// maybeBalanceAtQuiescence runs the exact per-arm balance verdict ONCE PER COLLECT, on
// the edge where BOTH pipeline axes have drained at the current epoch.
//
// IT SEALS BEFORE IT READS, and that is the WORK-GATED FLUSH ASYMMETRY handled
// explicitly rather than hoped away. The quiescence flush fires only when embed WORK
// happened since the last one, so a collect carrying ZERO embed work — the
// already-embedded case, which is exactly the state this verdict most often observes —
// leaves the flush unfired. The resident count reads the SEALED set only, so evaluating
// there would read short by the whole unsealed sub-threshold tail and report a deficit
// against a corpus that is merely still buffered. Forcing the seal first makes the
// operand describe the same corpus the verdict claims to be judging. A flush over a
// nothing-pending engine is a no-op, and it is paid once per collect rather than per
// tick because of the epoch gate below.
//
// A FLUSH FAILURE DECLINES THE VERDICT RATHER THAN EVALUATING ANYWAY. An unsealed tail
// is precisely the state that manufactures a false deficit, so proceeding on a failed
// seal would produce the wrong answer with confidence.
//
// EVERY GATE DECLINES BY DEFAULT: no closure wired, no epoch source, an axis not
// drained at the current epoch, or the verdict already run for this epoch — each is a
// reason not to assert, never a reason to assume health.
func (c *collector) maybeBalanceAtQuiescence(ctx context.Context) {
	if c.balanceAtQuiescence == nil || c.collectEpoch == nil {
		return
	}
	if !c.quiescentBothAxes() {
		return
	}
	want := c.collectEpoch() + 1
	if c.balanceEvaluatedAtEpoch.Load() == want {
		return // already evaluated for this collect
	}

	if c.flush != nil {
		if err := c.flush(ctx); err != nil {
			slog.Warn("pipeline.collector: balance verdict declined — the pre-verdict force-seal failed, "+
				"and an unsealed tail reads as a deficit that is not there",
				"graph_type", c.gt, "name", c.name, "error", err)
			return
		}
	}

	// STAMPED BEFORE THE CALL, not after. The verdict issues RPCs and can take a while;
	// stamping afterwards would let a second tick observe an unstamped epoch and run a
	// duplicate evaluation concurrently with the first.
	c.balanceEvaluatedAtEpoch.Store(want)

	if err := c.balanceAtQuiescence(ctx); err != nil {
		slog.Warn("pipeline.collector: balance verdict failed (best-effort; the next collect re-arms it)",
			"graph_type", c.gt, "name", c.name, "error", err)
	}
}

// noEpochSourceReported records the graphs for which quiescentBothAxes has already
// announced that it cannot evaluate. The refusal is a standing property of the
// deployment rather than an event, so it is said ONCE per graph instead of on
// every tick.
var noEpochSourceReported sync.Map

// quiescentBothAxes reports that BOTH pipeline axes drained to a COMPLETE, EMPTY
// gap set AT THE CURRENT COLLECT EPOCH.
//
// EPOCH EQUALITY, NOT A PAIR OF BOOLS, is what makes staleness structurally
// impossible rather than defended against. Each axis stamps one-plus the epoch it
// drained at; a collect landing new rows moves the epoch, so a stamp from before it
// no longer equals `want` and the axis correctly reads as not-drained — even though
// that axis's loop may not have run since, which is the common case because a
// drained axis sits on the longest idle sleep.
//
// AN AXIS THIS COLLECTOR DOES NOT RUN IS VACUOUSLY DRAINED. run() launches no loop
// for a disabled axis, so its stamp would stay 0 forever and the predicate could
// never fire on a graph with, say, no summarizer configured. That is a real
// deployment, not a corner case.
//
// WITH NO EPOCH SOURCE IT RETURNS FALSE, AND NEVER TREATS ABSENT STAMPS AS
// AGREEMENT. A nil source is the router-less client, where attachCollectGate
// installs no factory at all; reading it as epoch zero would pin `want` at 1
// forever, so no stamp would ever expire and the staleness hole this function
// exists to close would be fully open on exactly the deployment with nobody
// watching. Declining is a refusal to assert, not a degraded answer — and it is
// announced once per graph so the deployment is greppable rather than silently
// unmonitored.
//
// CONCURRENCY: advisory and re-evaluated every tick. Every disagreement between the
// two loads resolves conservatively to not-quiescent, so no lock and no cross-field
// ordering guarantee is needed. Do not add one.
func (c *collector) quiescentBothAxes() bool {
	if c.collectEpoch == nil {
		if _, seen := noEpochSourceReported.LoadOrStore(string(c.gt)+"\x00"+c.name, struct{}{}); !seen {
			slog.Info("pipeline.collector: cross-axis quiescence cannot be evaluated for this graph — "+
				"no collect-epoch source is wired (router-less or degraded client), so a drain "+
				"observation could never expire and the exact balance verdict does not run here",
				"graph_type", c.gt, "name", c.name)
		}
		return false
	}
	want := c.collectEpoch() + 1
	if c.summaryEnabled && c.summaryDrainedAtEpoch.Load() != want {
		return false
	}
	if c.embedEnabled && c.embedDrainedAtEpoch.Load() != want {
		return false
	}
	return true
}
