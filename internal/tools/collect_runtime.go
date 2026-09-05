// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

// collectGateMaxHold bounds how long one in-flight entry may keep the gap-scan
// gate raised for its graph (CollectInFlightForGraph). It exists because a
// detached collect carries NO deadline of its own: its ctx descends from
// BaseContext, which is cancelled only by daemon Stop, so a collect blocked on an
// unreachable server never terminates and never reaches the completion block that
// releases the gate.
//
// The bound is at least 8x the longest collect-related span measurable in
// practice, so it does not expire during healthy work.
//
// THE FALSE-POSITIVE DIRECTION MATTERS AND MUST NOT BE "TUNED AWAY". If the bound
// does expire during a HEALTHY long collect, the gate drops and gap scanning
// resumes mid-collect — a return to the pre-gate behavior for the rest of that
// collect, nothing worse. The alternative failure is far worse and completely
// silent: a wedged gate stops enriching that graph forever, with no error
// anywhere, turning a visible stale status into invisible stale data. Bounded
// re-admission beats unbounded staleness. Do not raise this without re-reading
// that trade.
const collectGateMaxHold = 30 * time.Minute

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
	// Composition is the run's rendered node-type census — what the harvest
	// actually produced. It is the carrier that makes a DETACHED run's
	// composition readable, since past the 60s cap the tool has already returned
	// and its text can no longer say anything. Empty while running, by
	// construction: a run in flight has no composition yet.
	Composition string
}

// collectRun is the unexported in-flight bookkeeping for a running target.
type collectRun struct {
	label     string
	startedAt time.Time
	// gt is the FAMILY this run collects into, and it is half of the run's
	// identity. THE IDENTITY IS THE PAIR — the family the run collects into and
	// the bare graph name inside that family — because two families can carry the
	// same name and the pipeline registers one collector per (family, name).
	// Neither half identifies the run on its own.
	gt kgtypes.GraphType
	// graph is the BARE graph name this run collects into (empty for every
	// collector family the derivation does not name). It is what
	// CollectInFlightForGraph matches against a registered collector's name, and
	// it is deliberately NOT branch-qualified — see CollectInFlightForGraph.
	graph string
}

// collectGraphKey is the completed-collect map's key: the same (family, name)
// pair collectRun records, as a comparable struct. A struct rather than a
// separator-joined string because the key is read in exactly two places, both in
// this file, and a struct key cannot be built by accidental concatenation the way
// a joined string can.
type collectGraphKey struct {
	gt   kgtypes.GraphType
	name string
}

// CollectRunHandle is the completion handle Start returns for a freshly-launched
// run. Done() closes when the run finishes; Err(), Composition() and
// ProducedGraph() are safe to read lock-free ONLY after Done() closes — the
// store-before-close ordering in Start's goroutine is the happens-before edge
// that makes those reads race-free.
type CollectRunHandle struct {
	done          chan struct{}
	err           error
	composition   string
	producedGraph string
}

// Done returns a channel closed when the run completes (success or failure).
func (h *CollectRunHandle) Done() <-chan struct{} { return h.done }

// Err returns the run's terminal error (nil on success). Read it ONLY after
// <-Done() has observed the channel closed; Start stores err BEFORE close(done),
// so a reader gated on Done() observes the fully-written value with no lock.
func (h *CollectRunHandle) Err() error { return h.err }

// Composition returns the run's rendered node-type census (empty when the run
// reported none). It carries the SAME read-only-after-Done() contract as Err:
// Start stores it BEFORE close(done), so a reader gated on Done() observes the
// fully-written value with no lock.
func (h *CollectRunHandle) Composition() string { return h.composition }

// ProducedGraph returns the name of the graph this run actually collected into,
// as the COLLECTOR reported it — not the collect id it was asked for. The two
// differ wherever a collector derives its own name: a pdf collect's id is a
// filesystem path while its graph name is a basename-plus-content-hash slug, so
// a caller that wants to name the produced graph in a follow-up call must read
// this rather than the id. Empty for a collector that reports none.
//
// Same read-only-after-Done() contract as Err and Composition.
func (h *CollectRunHandle) ProducedGraph() string { return h.producedGraph }

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
	// completed counts ENDED collects per (FAMILY, BARE GRAPH NAME) pair — the same
	// identity CollectInFlightForGraph gates on, deliberately NOT the per-target key
	// the two maps above use. It is the monotonic "collect epoch" a consumer stamps
	// an observation against so the observation expires when a collect lands.
	completed map[collectGraphKey]uint64

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
		completed:   map[collectGraphKey]uint64{},
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
// work returns the run's rendered node-type composition and the name of the
// graph it actually produced, alongside its error. The composition is recorded
// on the registry outcome and on the handle, which is what makes it readable for
// a DETACHED run through manage(status); the produced graph name is recorded on
// the handle, where the messaging seam reads it to name the graph in a
// follow-up call.
//
// The goroutine runs work under a panic-recover (degrade-not-die), fires ONE
// loud slog.Error for any failure (normal error OR recovered panic), then in the
// completion block updates the registry outcome, stores h.err AND h.composition,
// and closes h.done LAST — that ordering is the happens-before edge for a
// lock-free Err()/Composition()-after-Done() read and is a plain data race if
// inverted.
// graph is the BARE graph name this run targets (empty for a collector family
// the derivation does not name); recording it on the same map entry is what lets
// CollectInFlightForGraph answer, and what makes the completion block's
// delete(r.running, key) release the gap-scan gate on success, on error and on a
// recovered panic alike — with no second lifetime to keep in step.
//
// gt is the FAMILY the run collects into, and it is recorded on that SAME map
// entry as the name, so the completion block's delete releases the whole identity
// with no second lifetime to keep in step.
func (r *CollectRuntime) Start(key, label string, gt kgtypes.GraphType, graph string, work func() (string, string, error)) (h *CollectRunHandle, started bool, elapsed time.Duration) {
	r.mu.Lock()
	if run, busy := r.running[key]; busy {
		el := r.clockNow().Sub(run.startedAt)
		r.mu.Unlock()
		return nil, false, el
	}
	startedAt := r.clockNow()
	r.running[key] = &collectRun{label: label, startedAt: startedAt, gt: gt, graph: graph}
	r.mu.Unlock()

	h = &CollectRunHandle{done: make(chan struct{})}
	r.inFlight.Go(func() {
		// Run work under a panic-recover so a panicking collect degrades to a
		// failed run rather than taking down the daemon (mirrors
		// similarity_async.go). A recovered panic becomes err.
		var err error
		var composition string
		var producedGraph string
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("collect run panicked: %v", rec)
					slog.Error("collect: detached run panicked, recovered", "target", key, "panic", rec)
				}
			}()
			composition, producedGraph, err = work()
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
			Target:      key,
			Label:       label,
			State:       state,
			StartedAt:   startedAt,
			FinishedAt:  r.clockNow(),
			Err:         errStr,
			Composition: composition,
		}
		// THE COUNTER MOVES INSIDE THE SAME CRITICAL SECTION that drops the running
		// entry and records r.last, so the completion counter and the collect gate
		// can never disagree about what a finished collect is. No interleaving can
		// expose a gate that is already down beside an epoch that has not moved —
		// which is the window a consumer stamping "drained at epoch N" would read as
		// still valid across a collect that has in fact landed new rows.
		//
		// KEYED ON THE (FAMILY, BARE GRAPH NAME) PAIR, NOT ON key, so it answers the
		// same question at the same granularity CollectInFlightForGraph does. key is
		// the collect TARGET, which is a different identity; counting by it would make
		// the epoch a consumer reads for a graph move for collects into other targets
		// and never move for its own. The family is part of the key because two
		// families can carry the same name.
		if graph != "" {
			r.completed[collectGraphKey{gt: gt, name: graph}]++
		}
		r.mu.Unlock()

		h.err = err
		h.composition = composition
		h.producedGraph = producedGraph
		close(h.done)
	})
	return h, true, 0
}

// CollectInFlightForGraph reports whether a collect into the graph (gt, name) is
// running right now. The LLM pipeline consults it (through an inert hook
// installed at boot) to hold its gap scan off a graph whose collect is still
// landing rows.
//
// IT ANSWERS FOR THE (FAMILY, NAME) PAIR A COLLECT RECORDED, for every family
// whose identity the collect dispatch can derive — code, pdf, web, the three
// cicd providers and gcp, azure and k8s. The pair is the identity because two
// families can carry the same name and the pipeline registers one collector per
// (family, name): a code collect must not gate a knowledge graph that happens to
// share its name, and matching on the name alone would do exactly that.
//
// AN EMPTY NAME NEVER GATES, and that arm is load-bearing rather than defensive:
// a collector family the derivation does not name — aws, whose graph name comes
// from an STS call made during the walk, and the logs collector, which produces
// no collector graph — records the empty name, and gating on it would hold the
// scan over everything or nothing at all.
//
// THE NAME IT MATCHES IS THE BARE GRAPH NAME, NEVER BRANCH-QUALIFIED, and for a
// code graph that is the whole correctness of the gate. The pipeline registers
// exactly ONE collector per BASE code-graph name — the catalog it enumerates
// lists base graphs only, and branch-overlay qualification happens per ITEM, not
// per collector. A recorded identity carrying a branch suffix could therefore
// never equal any registered collector's name, and the gate would be permanently
// inert while every one of its own tests stayed green, because those tests supply
// both names themselves. A bare name gates every overlay of that repo, which is
// the only granularity the registration model offers and exactly the one wanted
// here.
//
// Entries older than collectGateMaxHold are IGNORED, so a collect that never
// terminates cannot starve the pipeline forever.
func (r *CollectRuntime) CollectInFlightForGraph(gt kgtypes.GraphType, name string) bool {
	if r == nil || name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clockNow()
	for _, run := range r.running {
		if run.gt == gt && run.graph == name && now.Sub(run.startedAt) < collectGateMaxHold {
			return true
		}
	}
	return false
}

// CompletedCollectsForGraph returns how many collects into (gt, name) have ENDED,
// by any route — success, error or recovered panic — i.e. the same transition that
// records r.last and lowers the collect gate.
//
// IT IS AN EPOCH, NOT A STATISTIC. A consumer stamps an observation about a graph
// with the value it read, and compares later: an equal value means no collect has
// landed since, a larger one means the observation is stale. That is what makes a
// cross-loop observation expire on its own rather than needing every observer to
// notice a collect.
//
// THE COUNTER IS BUMPED IN THE SAME CRITICAL SECTION as the gate's own state
// transition (see Start), so a reader can never see the gate down beside a
// stale epoch.
//
// IT SHARES CollectInFlightForGraph's IDENTITY RULE, and must: the (family, bare
// name) PAIR, the name never branch-qualified. The pair, because two families can
// carry the same name and counting them into one bucket would make a code
// collect expire a knowledge graph's observations. The bare name, because the
// pipeline registers one collector per base code graph — a branch-suffixed key
// would count into a bucket nothing ever reads, leaving the epoch pinned at zero
// and every stamp permanently fresh, the staleness hole silently reopened with
// every test still green.
//
// AN EMPTY NAME READS 0 without consulting the map, for the same reason
// CollectInFlightForGraph never gates on one: a collector family the derivation
// does not name records no name, so there is no bucket to count into.
//
// It is 0 for a pair no collect has finished, which is a legal observation rather
// than a sentinel; callers that need to distinguish "no epoch source at all" must
// do so by the absence of the accessor, not by 0.
func (r *CollectRuntime) CompletedCollectsForGraph(gt kgtypes.GraphType, name string) uint64 {
	if r == nil || name == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completed[collectGraphKey{gt: gt, name: name}]
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
