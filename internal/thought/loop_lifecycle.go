// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// loop_lifecycle.go holds the PropagationLoop goroutine lifecycle: Start (boot +
// launch), runBootClusterDetection (guarded boot detection), Stop (cooperative
// drain), and loop (the ticker body). The pass bodies + state live in loop.go.

// Start launches the background propagation goroutine. Call once after
// construction. Restores the persisted last-full-pass watermark so the backstop
// cadence survives a daemon restart (absent → zero Time, which forces a full pass
// on the first tick, correctly anchoring a fresh daemon), then runs an initial
// cluster detection so cached tensions/blind spots are available immediately.
func (p *PropagationLoop) Start() {
	if p == nil {
		return
	}
	// Restore lastFullPass at boot so the backstop cadence is continuous across a
	// restart rather than re-anchored every launch. A zero Time (absent watermark)
	// makes the first tick force a full pass — the correct fresh-daemon anchor.
	if t, ok := readLastFullPass(context.Background(), p.gc); ok {
		p.mu.Lock()
		p.lastFullPass = t
		p.mu.Unlock()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("PropagationLoop: panic recovered",
					"site", "PropagationLoop.Start",
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		p.runBootClusterDetection()
		p.loop()
	}()
}

// runBootClusterDetection runs the boot-time cluster detection under the SAME
// per-account reflection single-flight guard the hourly tick uses. Start calls
// p.runClusterDetection() directly (not via runBackgroundPropagation), so this is
// the one boot entry into runClusterDetection — without the guard a boot detection
// still draining when the first hourly tick (or a manual propagate) fires would let
// the two run redundant concurrent full drains. On a coalesce (!ok — a manual
// propagate already in flight at startup) the boot detection is skipped: the
// in-flight pass already produces fresh clusters. The boot detection runs OUTSIDE
// the per-tick budget ctx (unchanged).
//
// Before the detection itself it WARMS the resident corpus cache, the same
// refreshCorpusCache runPass performs at the top of an hourly tick. Without it a
// daemon restart ran a fully cold pass: every rewired consumer inside the detection
// found a cold cache and re-drained the corpus itself. The cold cost is unchanged
// in kind — an empty cache makes the delta drain return the whole corpus — but it
// is paid ONCE through the CorpusDelta keyset path instead of once per consumer.
// Nil-tolerant: a degraded loop (no cache/scanner, and every test fake) is an exact
// no-op, keeping the pre-cache behavior.
func (p *PropagationLoop) runBootClusterDetection() {
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	if !ok {
		slog.Info("thought: boot cluster detection absorbed by an in-flight pass — coalescing",
			"key", ReflectionPassKey)
		return
	}
	defer release()
	// The 6-minute bracket mirrors runPass's outer budget so the boot drain cannot
	// outlive the same bound; baseContext (not Background) so a daemon Stop during
	// boot unwinds the drain at the next RPC boundary.
	ctx, cancel := context.WithTimeout(p.baseContext(), 6*time.Minute)
	defer cancel()
	p.refreshCorpusCache(ctx)
	p.runClusterDetection()
}

// Stop signals the loop to exit and waits up to deadline for the
// in-flight work to drain. Nil-safe (mirrors dream.Runner.Stop at
// cmd/knowledge/internal/dream/runner.go:335-338) — a nil receiver
// returns immediately. The stopOnce guard ensures repeated Stop()
// calls don't double-close the channel.
func (p *PropagationLoop) Stop(deadline time.Duration) {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		close(p.stopCh)
		// Cancel the daemon-lifetime base ctx BEFORE the inFlight drain (bind-first startup):
		// every in-flight pass derives its compute ctx from baseCtx, so this makes
		// the per-RPC wire calls unwind at the next RPC boundary and the step-1
		// compute-stage ctx.Err() gates short-circuit Leiden/DeGroot — so the
		// inFlight.Wait below completes promptly instead of running an in-flight pass
		// to its 5/6-minute budget. nil-safe for direct struct-literal test fakes.
		if p.baseCancel != nil {
			p.baseCancel()
		}
	})
	done := make(chan struct{})
	go func() {
		p.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		slog.Warn("PropagationLoop.Stop: deadline elapsed, abandoning in-flight work", "deadline", deadline)
	}
}

// loop runs as a background goroutine, ticking hourly and firing
// onTick on each tick. T3-1 fix: select{<-ticker.C; <-stopCh: return}
// pattern instead of `for range ticker.C` so Stop() can actually exit
// the loop body.
func (p *PropagationLoop) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.onTick()
		case <-p.stopCh:
			return
		}
	}
}
