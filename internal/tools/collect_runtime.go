// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// collect_runtime.go holds the standing, daemon-lifetime runtime that owns
// DETACHED collect goroutines and tracks per-target run status. It is the seam
// that lets the collect MCP handler cap its synchronous wait at 60s:
// the collector runs under this runtime from the start, the handler races the
// run against a 60s timer, and on timeout it returns an all-good "still running
// in the background" message while the goroutine finishes here.
//
// The lifecycle mirrors thought.PropagationLoop exactly (baseCtx cancel-before-
// drain + an inFlight WaitGroup drained by a bounded Stop), and the per-target
// single-flight guard mirrors the AcquireReflectionPass / RebuildSegments map
// shape. The GENUINELY-NEW piece is the per-target run registry (the running +
// last-outcome maps): no existing collect-run tracker exists, and the reflection
// single-flight guard is single-key and status-less, so it cannot carry per-
// target outcome state.

// collectDetachThreshold is the fixed cap on how long the collect MCP handler
// blocks the caller before returning the "still running in the background"
// message. Other MCP hosts default to a ~60s per-tool timeout, so the cap sits
// just under it. NewCollectRuntime seeds detachAfter from this const; tests
// override the field in-package with a small value so they never sleep 60s.
const collectDetachThreshold = 60 * time.Second

// CollectRunStatus is the exported, immutable snapshot of one target's collect
// run — either the in-flight state (State=="running", FinishedAt zero) or the
// last completed/failed outcome. manage(status) renders these.
type CollectRunStatus struct {
	Target     string // the single-flight key (collector type + normalized id)
	Label      string // human-facing "<type> <id>"
	State      string // running | completed | failed
	StartedAt  time.Time
	FinishedAt time.Time // zero while running
	Err        string    // non-empty only for State=="failed"
}

// collectRun is the unexported in-flight bookkeeping for a running target.
type collectRun struct {
	label     string
	startedAt time.Time
}

// CollectRunHandle is the completion handle Start returns for a freshly-launched
// run. Done() closes when the run finishes; Err() is safe to read lock-free ONLY
// after Done() closes — the store-before-close ordering in Start's goroutine is
// the happens-before edge that makes that read race-free.
type CollectRunHandle struct {
	done chan struct{}
	err  error
}

// Done returns a channel closed when the run completes (success or failure).
func (h *CollectRunHandle) Done() <-chan struct{} { return h.done }

// Err returns the run's terminal error (nil on success). Read it ONLY after
// <-Done() has observed the channel closed; Start stores err BEFORE close(done),
// so a reader gated on Done() observes the fully-written value with no lock.
func (h *CollectRunHandle) Err() error { return h.err }

// CollectRuntime owns detached collect goroutines for the daemon's lifetime and
// tracks per-target run status. Constructed once at boot (NewCollectRuntime),
// drained once at shutdown (Stop).
type CollectRuntime struct {
	// baseCtx/baseCancel are the daemon-lifetime ctx every detached run's ctx
	// ultimately derives from (via BaseContext); Stop cancels baseCtx before the
	// inFlight drain so an in-flight collect unwinds at its next RPC boundary.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	inFlight sync.WaitGroup // drained by Stop; each Start adds one bracket
	stopOnce sync.Once      // guards baseCancel against a double Stop

	mu      sync.Mutex
	running map[string]*collectRun      // per-target in-flight state
	last    map[string]CollectRunStatus // per-target last completed/failed outcome

	clock       func() time.Time // default time.Now; overridden in-package by tests
	detachAfter time.Duration    // default collectDetachThreshold; overridden by tests
}

// NewCollectRuntime constructs a ready-to-use runtime with a daemon-lifetime
// baseCtx, the real clock, and the fixed 60s detach threshold. Mirrors
// thought.NewPropagationLoop's baseCtx/baseCancel mint.
func NewCollectRuntime() *CollectRuntime {
	baseCtx, baseCancel := context.WithCancel(context.Background()) //nolint:gosec // G118: baseCancel is stored on the runtime and invoked by Stop
	return &CollectRuntime{
		baseCtx:     baseCtx,
		baseCancel:  baseCancel,
		running:     map[string]*collectRun{},
		last:        map[string]CollectRunStatus{},
		clock:       time.Now,
		detachAfter: collectDetachThreshold,
	}
}

// BaseContext returns the runtime's daemon-lifetime ctx (nil-guarded to
// context.Background() for direct struct-literal test fakes). The collect
// intercept builds its ENRICHED collect ctx as a child of this, so a daemon Stop
// (baseCancel) propagates cancellation into a detached run.
func (r *CollectRuntime) BaseContext() context.Context {
	if r == nil || r.baseCtx == nil {
		return context.Background()
	}
	return r.baseCtx
}

// DetachAfter returns the synchronous-wait cap the handler races against.
func (r *CollectRuntime) DetachAfter() time.Duration { return r.detachAfter }

// clockNow returns the runtime's time source, nil-guarded to time.Now.
func (r *CollectRuntime) clockNow() time.Time {
	if r.clock == nil {
		return time.Now()
	}
	return r.clock()
}

// Start launches work on a daemon-lifetime goroutine under the per-target single-
// flight guard. work is a NO-ARG closure: the caller closes over the already-
// enriched collect ctx, so Start injects no ctx (there is no bare-baseCtx param a
// caller could accidentally substitute for the cascade/resolution/web-enriched
// ctx).
//
//   - If key is already running: returns (nil, false, in-flight elapsed) and
//     spawns nothing — the caller renders the "already running" coalesce message.
//   - Otherwise: records the run, spawns the goroutine via inFlight.Go (so Stop
//     drains it), and returns (handle, true, 0).
//
// The goroutine runs work under a panic-recover (degrade-not-die), fires ONE
// loud slog.Error for any failure (normal error OR recovered panic), then in the
// completion block updates the registry outcome, stores h.err, and closes h.done
// LAST — that ordering is the happens-before edge for a lock-free Err()-after-
// Done() read and is a plain data race if inverted.
func (r *CollectRuntime) Start(key, label string, work func() error) (h *CollectRunHandle, started bool, elapsed time.Duration) {
	r.mu.Lock()
	if run, busy := r.running[key]; busy {
		el := r.clockNow().Sub(run.startedAt)
		r.mu.Unlock()
		return nil, false, el
	}
	startedAt := r.clockNow()
	r.running[key] = &collectRun{label: label, startedAt: startedAt}
	r.mu.Unlock()

	h = &CollectRunHandle{done: make(chan struct{})}
	r.inFlight.Go(func() {
		// Run work under a panic-recover so a panicking collect degrades to a
		// failed run rather than taking down the daemon (mirrors
		// similarity_async.go). A recovered panic becomes err.
		var err error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("collect run panicked: %v", rec)
					slog.Error("collect: detached run panicked, recovered", "target", key, "panic", rec)
				}
			}()
			err = work()
		}()

		// One loud slog.Error for EVERY failing run — normal collect error or
		// recovered panic (In-scope item 5). Never silently swallowed.
		if err != nil {
			slog.Error("collect: detached run failed", "target", key, "label", label, "error", err)
		}

		// Completion block — ORDERING IS LOAD-BEARING: update the registry
		// outcome, then store h.err, then close(h.done) LAST. The close is the
		// happens-before edge so a reader doing <-h.Done() then lock-free h.Err()
		// observes the fully-written err.
		state := "completed"
		errStr := ""
		if err != nil {
			state = "failed"
			errStr = err.Error()
		}
		r.mu.Lock()
		delete(r.running, key)
		r.last[key] = CollectRunStatus{
			Target:     key,
			Label:      label,
			State:      state,
			StartedAt:  startedAt,
			FinishedAt: r.clockNow(),
			Err:        errStr,
		}
		r.mu.Unlock()

		h.err = err
		close(h.done)
	})
	return h, true, 0
}

// Snapshot returns one CollectRunStatus per running target (State=="running",
// caller derives elapsed from StartedAt) unioned with each target's last
// completed/failed outcome, sorted by Label for a stable render.
func (r *CollectRuntime) Snapshot() []CollectRunStatus {
	r.mu.Lock()
	out := make([]CollectRunStatus, 0, len(r.running)+len(r.last))
	for key, run := range r.running {
		out = append(out, CollectRunStatus{
			Target:    key,
			Label:     run.label,
			State:     "running",
			StartedAt: run.startedAt,
		})
	}
	for _, st := range r.last {
		out = append(out, st)
	}
	r.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Stop cancels baseCtx (so an in-flight detached collect unwinds at its next RPC
// boundary) then waits up to deadline for the inFlight drain. Nil-safe; the
// stopOnce guard makes repeated Stop calls safe. Mirrors PropagationLoop.Stop.
func (r *CollectRuntime) Stop(deadline time.Duration) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.baseCancel != nil {
			r.baseCancel()
		}
	})
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		slog.Warn("CollectRuntime.Stop: deadline elapsed, abandoning in-flight collect", "deadline", deadline)
	}
}
