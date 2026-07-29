// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
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
