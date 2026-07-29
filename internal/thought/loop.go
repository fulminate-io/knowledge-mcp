// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// PropagationInterval is the period at which the client-side
// PropagationLoop fires runBackgroundPropagation. OQ1 lock: this is
// HOURLY, NOT 30 seconds. The charge-driven trigger is gone — every
// tick runs cluster detection + propagation unconditionally.
const PropagationInterval = time.Hour

// PropagationLoop is the client-side "subconscious" goroutine that
// periodically propagates charge influence through the thought graph
// and re-detects clusters. Constructed with NewPropagationLoop and
// owned by the cmd/knowledge serve daemon bootstrap (wirePropagationRuntime).
//
// Carries an Execute-only thought.Caller — no Store-shaped wrapper. The
// serve daemon passes the login-aware Router, so every read/write is a wire
// call that routes cloud-when-logged-in / local otherwise.
type PropagationLoop struct {
	gc Caller

	// scanner and summarizer are OPTIONAL, nil-tolerant dependencies set at
	// construction via WithTopicDeps. The scanner (member-vector drain) is consumed
	// by BOTH the hourly runClusterDetection pass — for the post-Leiden
	// leaf-attachment fallback — AND the manual lever (RunSimilarityPass); the
	// summarizer is consumed ONLY by the lever. The production bootstrap passes the
	// real adapters and tests leave them nil (degraded mode = exactly the
	// pre-leaf-attachment behavior: detection skips leaf attachment with a loud WARN, the lever reports a
	// degraded run). nil scanner → no centroids → no leaf attachment + the lever
	// degrades; nil summarizer → centroids + cascade + links still run, only topic
	// summaries / drift skip (drift anchors to the stored topic_centroid, so it needs
	// no embedder).
	scanner    PipelineScanner
	summarizer TopicSummarizer

	// corpus is the resident thought-corpus cache. corpusScanner is the
	// CorpusDelta wire seam it drains through. Both nil-tolerant: a nil corpusScanner
	// leaves the loop in DEGRADED mode — the full drainThoughtBrowse path, behavior-
	// equivalent to pre-cache — so tests that leave it nil keep the old semantics.
	// The production bootstrap wires the routed CorpusDelta client via WithTopicDeps.
	corpus        *corpusCache
	corpusScanner CorpusDeltaScanner

	// vectorResident and coverageGate are the OPTIONAL in-process vector seam set
	// via WithVectorDeps: leaf attachment resolves member vectors from the client's
	// resident segment engines (ZERO RPC) when the gate reports the HNSW pool
	// trustworthy, and falls back to the server drain when it declines. Both
	// nil-tolerant — a nil pair is DEGRADED mode and takes the drain, which is
	// byte-identical to the pre-resident behavior every existing test exercises.
	vectorResident VectorResident
	coverageGate   SegmentCoverageGate

	// pendingLeafRetry holds leaves that were candidates but had no resident vector
	// yet. It is REQUIRED because a newly-embedded thought does not re-enter the
	// dirty seed when its vector arrives. The embed writeback DOES run db.Update —
	// it clears the embed-failure marker — but changedFields compares metadata
	// key-by-key against a map read, so setting a key to "" when it was ABSENT reads
	// as unchanged and yields no changed fields; AddNode then preserves UpdatedAt
	// (its content-identity check), so the node never re-dirties. If changedFields
	// ever starts reporting that write, this set becomes redundant — check there
	// before deleting it.
	//
	// Under resident resolution it matters MORE than under the drain: a vector
	// becomes resolvable only after the daemon's own embed drain produces it AND the
	// carrying segment is shipped and imported, so freshness lags by more than one
	// pass. Guarded by p.mu.
	pendingLeafRetry map[string]bool

	// interval is the per-tick cadence. Defaults to PropagationInterval
	// (one hour) in production. Tests override via newPropagationLoopForTest
	// to drive ticks deterministically without sleeping for an hour.
	interval time.Duration

	// backstopInterval is the cadence of the full-corpus reflection backstop:
	// once this much wall-clock has elapsed since the last completed full pass,
	// the next tick FORCES a full Leiden + DeGroot recompute (bypassing both the
	// quiet-skip and the incremental scoping) to reset accumulated
	// Dynamic-Frontier-Leiden approximation drift. Default 24h (nightly), set from
	// Config.ReflectBackstopInterval via NewPropagationLoop.
	backstopInterval time.Duration

	// clock is the time source for every cadence comparison (backstop forcing,
	// lastFullPass stamping). Defaults to time.Now in production; cadence tests
	// inject a fake clock so a forced/non-forced tick can be driven
	// deterministically without sleeping for a day.
	clock func() time.Time

	// onTick is the work performed on every ticker fire. Defaults to
	// p.runBackgroundPropagation in production. Tests inject a counter
	// closure so TestPropagationLoop_HourlyTick can assert tick semantics
	// without invoking real wire calls.
	onTick func()

	// baseCtx is the daemon-lifetime cancelable ctx every in-flight pass derives
	// from (the hourly tick's 6m compute ctx, the 5m cluster-detection ctx, the
	// async similarity pass, and a forced full pass). baseCancel cancels it in Stop
	// BEFORE the inFlight drain, so an in-flight pass observes ctx.Done() and
	// unwinds promptly rather than running to its multi-minute budget (the
	// cooperative daemon-stop drain). Both nil in a direct struct-literal test fake; baseContext()
	// degrades to context.Background() in that case.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	stopOnce sync.Once
	stopCh   chan struct{}
	inFlight sync.WaitGroup

	mu             sync.Mutex
	lastClusters   []ThoughtCluster
	lastProfile    *PersonalityProfile
	leidenState    *graph.LeidenState
	lastAdj        map[string][]string
	lastTensions   []TensionReport
	lastBlindSpots BlindSpotReport
	// lastComputed is the cold sentinel for the cluster/profile/tensions cache:
	// false until storeDetectionResults has published at least one completed tick,
	// true thereafter. The on-demand cache-serve handlers (personality/summary/
	// tensions) read it to distinguish a cold daemon (nothing computed yet) from a
	// genuinely empty result. Mirrors BlindSpotReport.Computed; flipped true only on
	// the warm path (storeDetectionResults), never on a failed/early-return tick.
	lastComputed bool

	// lastDirtySeed is the dirty seed derived by the most recent
	// runClusterDetection tick (UpdatedAt>watermark thoughts UNION edge-change
	// endpoints). runBackgroundPropagation reads it to scope RunPropagationScoped
	// to the closure on a warm tick. Nil on a cold-start full pass (scoping off).
	lastDirtySeed map[string]bool
	// lastWatermark is the max(Node.UpdatedAt) observed over the most recent full
	// per-tick thought browse — the watermark persisted after a completed warm pass
	// and read back as the dirty-seed cutoff next tick.
	lastWatermark int64

	// lastFullPass is the clock time of the most recent COMPLETED full-corpus
	// backstop pass. Seeded at boot from the persisted last_full_pass watermark
	// (zero Time when absent → the first tick forces a full pass, anchoring a fresh
	// daemon). A tick forces a full pass when clock()-lastFullPass >= backstopInterval.
	lastFullPass time.Time

	// forceFullNext, when set, makes the NEXT runClusterDetection SKIP the
	// cold-start rehydrate and leave prevLeidenState nil so runLeidenStep takes the
	// TRUE full branch (graph.NewLeidenState) rather than rehydrating from the
	// persisted cluster_id partition. Set by the backstop decision in
	// runBackgroundPropagation and cleared by runClusterDetection once consumed.
	// Nilling leidenState alone only triggers rehydrate (NOT a full pass), so this
	// flag is REQUIRED to bypass rehydrate and force an exact full recompute.
	forceFullNext bool
}

// NewPropagationLoop creates a PropagationLoop backed by the given
// GraphClient and the full-pass backstop cadence. wirePropagationRuntime in
// the serve daemon bootstrap owns construction and passes
// Config.ReflectBackstopInterval as backstopInterval. The clock defaults to
// time.Now; tests override it via newPropagationLoopForTest.
func NewPropagationLoop(gc Caller, backstopInterval time.Duration) *PropagationLoop {
	baseCtx, baseCancel := newPropagationBaseContext()
	p := &PropagationLoop{
		gc:               gc,
		interval:         PropagationInterval,
		backstopInterval: backstopInterval,
		clock:            time.Now,
		baseCtx:          baseCtx,
		baseCancel:       baseCancel,
		stopCh:           make(chan struct{}),
	}
	p.onTick = p.runBackgroundPropagation
	return p
}

// baseContext returns the loop's daemon-lifetime ctx, defaulting to
// context.Background() when baseCtx is unset. NewPropagationLoop and
// newPropagationLoopForTest always set it; this nil-guard keeps direct
// struct-literal construction (the gate/rehydrate test fakes that mirror the
// clockNow nil-guard pattern) from panicking on the per-pass ctx derivation.
func (p *PropagationLoop) baseContext() context.Context {
	if p.baseCtx == nil {
		return context.Background()
	}
	return p.baseCtx
}

// clockNow returns the loop's time source, defaulting to time.Now when clock is
// unset. NewPropagationLoop and newPropagationLoopForTest always set clock; this
// nil-guard keeps direct struct-literal construction (the gate/rehydrate test
// fakes) from panicking on the backstop cadence read.
func (p *PropagationLoop) clockNow() time.Time {
	if p.clock == nil {
		return time.Now()
	}
	return p.clock()
}

// The p.mu-guarded accessors over this loop's cached tick output (GetClusters,
// GetBlindSpots, GetClustersCached, GetTensions) and the synchronous cold-cache
// TriggerClusterDetection live in loop_accessors.go.

// runBackgroundPropagation is the hourly-tick entry. It brackets the pass for
// Stop()-drain (inFlight.Add, ORTHOGONAL to the single-flight coalescing below),
// claims the per-account reflection single-flight guard around the WHOLE pass so a
// manual propagate cannot interleave a second concurrent recompute + writeback over
// the same corpus (on a coalesce emit the loud absorbed-trigger log and return),
// then lets decideBackstopForce decide whether the backstop must force a full pass
// this tick and runs the shared runPass body. The manual ForceFullPass lever
// (backstop.go) shares that body with forceFull pinned true unconditionally.
func (p *PropagationLoop) runBackgroundPropagation() {
	if p == nil || p.gc == nil {
		return
	}
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	release, ok := AcquireReflectionPass(ReflectionPassKey)
	if !ok {
		slog.Info("thought: reflection tick absorbed by an in-flight pass — coalescing",
			"key", ReflectionPassKey)
		return
	}
	defer release()

	forceFull := p.decideBackstopForce()
	// The tick discards runPass's (result, err): the pass logs its own outcome
	// (propagation-complete / budget-exceeded WARN / forced-pass log) internally, and
	// a failed tick simply re-runs next hour. The manual ForceFullPass lever consumes
	// the return to render an operator summary.
	_, _ = p.runPass(p.baseContext(), forceFull)
}

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

	// Every pass triggers a cluster detection. No conditional guard — the trigger
	// semantics are deliberately simple per OQ1 lock (one tick = one detection +
	// one propagation; the manual lever is the same single detection + propagation).
	slog.Debug("thought: runPass — triggering cluster detection")
	p.runClusterDetection()

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
	result, err := RunPropagationScoped(ctx, p.gc, profile, p.fetchNodeMap(ctx, profile), dirtySeed, p) // resident cache.
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
// When the resident corpus cache is warm the node map is built DIRECTLY
// from the snapshot (the full Node payloads — with cluster_id + propagated_* — are
// already in hand), eliminating both the listAllThoughtIDs browse and the
// fetchNodesByIDs hydrate. A cold/degraded loop falls back to the two-round-trip
// browse+hydrate.
func (p *PropagationLoop) fetchNodeMap(ctx context.Context, profile *PersonalityProfile) map[string]*knowledgev1.Node {
	if profile == nil {
		return nil
	}
	if nodes, warm := p.CorpusSnapshot(); warm {
		m := make(map[string]*knowledgev1.Node, len(nodes))
		for _, n := range nodes {
			m[n.GetId()] = n
		}
		return m
	}
	ids, _ := listAllThoughtIDs(ctx, p.gc)
	return fetchNodesByIDs(ctx, p.gc, ids)
}
