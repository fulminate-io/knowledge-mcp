// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// Runner is the dream-foundation control surface. It owns:
//   - the Registry of Workers (graph-resident catalog)
//   - the EventBus that fans tool events out to subscribed workers
//   - the GraphStorage path used to write per-worker logs
//
// Worker MCP tool calls route through the injected Dispatch func (the
// client intercept chain → engine.Dispatch), NOT a Runner-held graph
// client. One process holds at most one Runner; bootstrap wires it. Test
// harnesses construct Runners directly via NewRunner.
type Runner struct {
	// Registry loads graph-resident NodeWorker entries via the wire-
	// loopback worker:list tool call.
	Registry *Registry

	// Bus fans Events out to subscribers. The Phase 2 chokepoint Emits
	// tool-* events; Runner Emits worker-* events.
	Bus *EventBus

	// GraphStorage is the absolute path to the substrate's graph storage
	// directory (typically ~/.knowledge/). Per-worker logs live under
	// <GraphStorage>/workers/<name>.log.
	GraphStorage string

	// Dispatch is the standard client tool-call path BuildAllowedTools routes
	// every worker tool call through (intercept chain → engine.Dispatch) — the
	// SAME sequence upstream MCP traffic uses, no worker-specific plumbing.
	Dispatch DispatchFunc

	// Catalog is the client-owned MCP tool catalog (tools.AllToolSchemas),
	// wired at bootstrap; BuildAllowedTools filters this value by the
	// worker's allowlist. Carried on the Runner rather than imported in
	// package dream because tools imports dream (cycle).
	Catalog []kgtools.MCPTool

	// inFlight tracks running invocations so Stop can drain them up to a
	// deadline. The map keys are worker names; multiple concurrent
	// invocations of the same worker are tracked under sequential
	// counters to avoid silent collision.
	mu       sync.Mutex
	inFlight sync.WaitGroup

	// inFlightByID is the cancel registry for active invocations keyed
	// by per-run UUID. Cancel(name|id) walks this map under mu and calls
	// each entry's CancelFunc; runWorker registers itself on entry and
	// unregisters under defer on exit. Map identity is stable for the
	// life of the Runner; entries come and go.
	inFlightByID map[string]*inflightInvocation

	// subs holds per-worker subscription channels installed by
	// InstallWorker. Stop unsubscribes each channel cleanly. Map key is the
	// worker name + trigger index, both stable for the run of one Runner.
	subs []runnerSubscription

	// stopOnce ensures Stop is idempotent.
	stopOnce sync.Once

	// stopCh is closed by Stop to signal the per-trigger loops to exit.
	stopCh chan struct{}
}

// runnerSubscription pairs an EventBus channel with the worker name it
// was opened for, so Stop can unsubscribe and so the dispatch loop knows
// which worker to spawn on each event.
type runnerSubscription struct {
	worker string
	ch     <-chan Event
}

// inflightInvocation is one entry in the cancel registry. Cancel calls
// CancelFunc; the runWorker goroutine returns when its derived context
// notices the cancellation. Started is exposed via Runner.Running so
// operators can see "what's been running for 14 minutes" before deciding
// what to kill.
type inflightInvocation struct {
	WorkerName string
	Cancel     context.CancelFunc
	Started    time.Time
}

// RunningInvocation is the public view of one in-flight entry returned
// by Runner.Running. Mirrors inflightInvocation minus the CancelFunc.
type RunningInvocation struct {
	InvocationID string    `json:"invocation_id"`
	WorkerName   string    `json:"worker"`
	Started      time.Time `json:"started"`
}

// NewRunner returns a Runner ready for Start. The Registry reads
// graph-resident Worker rows via the wire-loopback worker:list tool
// call at use-time — constructing a Runner before the server has
// finished standing up the listener is fine, the call resolves lazily
// when triggered.
func NewRunner(reg *Registry, bus *EventBus, graphStorage string, dispatch DispatchFunc, catalog []kgtools.MCPTool) *Runner {
	return &Runner{
		Registry:     reg,
		Bus:          bus,
		GraphStorage: graphStorage,
		Dispatch:     dispatch,
		Catalog:      catalog,
		inFlightByID: make(map[string]*inflightInvocation),
		stopCh:       make(chan struct{}),
	}
}

// newInvocationID returns a 32-char hex UUID. Used as the per-run key in
// the cancel registry and emitted on InvocationRecord.InvocationID so
// operators can target a specific run via worker(operation:"cancel",
// invocation:"<id>"). 16 bytes of crypto/rand entropy — collisions across
// the lifetime of a single Runner are not a real concern.
func newInvocationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on a healthy system; if it does, fall
		// back to the wall-clock so the run still gets a unique-ish ID
		// and we don't crash an in-progress trigger.
		return fmt.Sprintf("clk-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// registerInvocation adds an entry to inFlightByID and returns the new
// invocation_id together with a cancellable context derived from ctx.
// Callers MUST defer unregisterInvocation(id) on the returned id; missing
// the unregister leaks the cancel func (a no-op if the run already
// exited, but the map grows unbounded).
func (r *Runner) registerInvocation(ctx context.Context, workerName string) (string, context.Context, context.CancelFunc) {
	id := newInvocationID()
	derived, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.inFlightByID[id] = &inflightInvocation{
		WorkerName: workerName,
		Cancel:     cancel,
		Started:    time.Now().UTC(),
	}
	r.mu.Unlock()
	return id, derived, cancel
}

// unregisterInvocation removes id from the in-flight map. Idempotent —
// calling twice or on an unknown id is a no-op. Does NOT call the cancel
// func; that's the caller's responsibility (typically already-deferred
// from registerInvocation).
func (r *Runner) unregisterInvocation(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	delete(r.inFlightByID, id)
	r.mu.Unlock()
}

// Running returns a snapshot of every in-flight invocation. Used by
// worker(operation:"running") to surface targets for operators choosing
// what to cancel. Sorted by Started ascending so the longest-running
// entries appear first.
func (r *Runner) Running() []RunningInvocation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := make([]RunningInvocation, 0, len(r.inFlightByID))
	for id, inv := range r.inFlightByID {
		out = append(out, RunningInvocation{
			InvocationID: id,
			WorkerName:   inv.WorkerName,
			Started:      inv.Started,
		})
	}
	r.mu.Unlock()
	// Stable order: oldest first. Caller renders as text or json.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Started.Before(out[j-1].Started); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Cancel stops in-flight invocations matching id (specific run) or name
// (every running invocation of that worker). Exactly one of id/name must
// be non-empty; passing both prefers id. Returns the count of CancelFuncs
// invoked. Canceling an unknown id or name with no matches returns 0
// without error. Cancel does NOT remove the entry from the registry —
// the runWorker goroutine unregisters when it observes ctx.Err() and
// exits. So a back-to-back Cancel(id) before the goroutine gets a turn
// will return 1 both times (the second context.cancel call is a no-op
// per stdlib semantics); after the goroutine has cleaned up, subsequent
// calls return 0.
//
// Cancellation propagates through the per-invocation context the runWorker
// goroutine holds; eino's react.Agent honors ctx, and the connect-go
// transport in mcpTool.InvokableRun propagates cancellation to in-flight
// tool calls as well. Sub-second turnaround in practice; the goroutine
// itself returns at the next ctx.Err() check or tool-result receipt.
func (r *Runner) Cancel(id, name string) (int, error) {
	if r == nil {
		return 0, errors.New("dream: Runner.Cancel: nil receiver")
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" && name == "" {
		return 0, errors.New("dream: Runner.Cancel: id or name required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != "" {
		inv, ok := r.inFlightByID[id]
		if !ok {
			return 0, nil
		}
		inv.Cancel()
		return 1, nil
	}
	count := 0
	for _, inv := range r.inFlightByID {
		if inv.WorkerName == name {
			inv.Cancel()
			count++
		}
	}
	return count, nil
}

// InstallWorker subscribes w's triggers to the EventBus and spawns one
// dispatch goroutine per subscription. Returns immediately; the dispatch
// loops run until Stop is called or ctx is cancelled.
//
// It is called when a worker is CREATED in this process, and that is the
// only way a worker's triggers are ever installed. A running trigger
// registration is per-process state: worker rows persist, their running
// registration does not, so a worker earns its subscriptions by being
// created here rather than by having existed before a restart. There is
// deliberately no entry point that walks the registry and installs
// triggers for workers this process did not create.
//
// Self-trigger guard: events whose Origin starts with "worker:" are
// dropped before the user's Trigger.Filter even runs. This prevents an
// agent worker subscribed to tool-completed from re-firing on its own
// MCP tool calls (an infinite loop in v1).
//
// A disabled worker installs nothing. nil-safe.
func (r *Runner) InstallWorker(ctx context.Context, w Worker) {
	if r == nil || !w.Enabled {
		return
	}
	for _, t := range w.Triggers {
		r.installTrigger(ctx, w, t)
	}
	slog.Info("dream: worker triggers installed", "worker", w.Name, "triggers", len(w.Triggers))
}

// installTrigger registers one EventBus subscription for w + t and
// spawns the dispatch loop that fans matching events into runWorker.
//
// Cron triggers parse-validated at Worker.Validate time but never
// dispatch in v1 — installTrigger silently skips them so cron entries
// stay parseable but inert until a follow-up scheduler ticket lands.
// Manual triggers similarly never receive events from Emit (no producer
// in v1 emits "manual"); they are reachable only through OnManualTrigger.
func (r *Runner) installTrigger(ctx context.Context, w Worker, t Trigger) {
	if t.Event == EventCron || t.Event == EventManual {
		return
	}
	ch := r.Bus.Subscribe(t)
	r.mu.Lock()
	r.subs = append(r.subs, runnerSubscription{worker: w.Name, ch: ch})
	r.mu.Unlock()

	go r.dispatchLoop(ctx, w, ch)
}

// dispatchLoop drains ch, applies the self-trigger guard, and spawns
// runWorker on every accepted event. Returns when ch closes (Stop
// unsubscribed) or ctx cancels.
func (r *Runner) dispatchLoop(ctx context.Context, w Worker, ch <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if OriginIsDreamWorker(ev) {
				// Self-trigger guard: never re-fire on a worker's
				// own tool calls.
				continue
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				slog.Warn("dream: Runner.dispatchLoop: marshal event", "worker", w.Name, "error", err)
				continue
			}
			r.inFlight.Add(1)
			go func(payload json.RawMessage) {
				defer r.inFlight.Done()
				r.runWorker(ctx, w, payload)
			}(payload)
		}
	}
}

// Stop unsubscribes every installed trigger and waits up to the deadline
// for in-flight invocations to drain. Idempotent — calling Stop after
// the first invocation is a no-op.
//
// Stop does NOT cancel ctx for in-flight invocations; per-worker context
// (with the wallclock cap from Worker.MaxWallclockSeconds) is the
// authoritative deadline.
func (r *Runner) Stop(deadline time.Duration) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
		r.mu.Lock()
		subs := r.subs
		r.subs = nil
		r.mu.Unlock()
		for _, s := range subs {
			r.Bus.Unsubscribe(s.ch)
		}
	})
	if deadline <= 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		slog.Warn("dream: Runner.Stop: drain timeout exceeded; abandoning in-flight workers", "deadline", deadline)
	}
}

// runWorker executes one invocation of w with the given payload. Lifecycle:
//  1. Open the per-worker WorkerLog under <GraphStorage>/workers/<name>.log.
//  2. Emit worker-started Event on the bus.
//  3. Run runReAct with the SOLE runtime path.
//  4. Emit worker-completed Event with status (ok/error) + duration.
//  5. Close the log.
//
// Errors at any step are logged and surfaced through the worker-completed
// Event's Status field. The per-worker log captures the structured
// error breakdown.
func (r *Runner) runWorker(ctx context.Context, w Worker, payload json.RawMessage) {
	if r.GraphStorage == "" {
		slog.Warn("dream: Runner.runWorker: empty GraphStorage; skipping", "worker", w.Name)
		return
	}
	log, err := OpenWorkerLog(r.GraphStorage, w.Name)
	if err != nil {
		slog.Warn("dream: Runner.runWorker: OpenWorkerLog", "worker", w.Name, "error", err)
		return
	}
	defer func() { _ = log.Close() }()

	invID, runCtx, cancel := r.registerInvocation(ctx, w.Name)
	defer cancel()
	defer r.unregisterInvocation(invID)

	startedAt := time.Now().UTC()
	r.Bus.Emit(Event{
		Type:   EventWorkerStarted,
		Worker: w.Name,
		Origin: "worker:" + w.Name,
		At:     startedAt,
	})

	runErr := r.runReAct(runCtx, w, string(payload), log, invID)
	dur := time.Since(startedAt).Milliseconds()
	status := "ok"
	if runErr != nil {
		status = "error"
		slog.Warn("dream: Runner.runWorker: runReAct failed", "worker", w.Name, "error", runErr)
	}

	r.Bus.Emit(Event{
		Type:       EventWorkerCompleted,
		Worker:     w.Name,
		Origin:     "worker:" + w.Name,
		Status:     status,
		DurationMs: dur,
		At:         time.Now().UTC(),
	})
}
