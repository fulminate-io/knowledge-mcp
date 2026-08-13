// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

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

	// corpusCachePath is the durable warm-start record for the resident corpus
	// cache. EMPTY DISABLES PERSISTENCE ENTIRELY (every existing test, and any
	// client wired without a data root): the loop then behaves exactly as it does
	// today — resident-only, cold on every restart.
	corpusCachePath string
	// corpusWarmLoadDone records that the once-per-process record read has been
	// attempted. Guarded by mu, mirroring watermarkRestored.
	corpusWarmLoadDone bool
	// corpusUnvalidated is true from the instant disk payloads are merged into the
	// live cache until they are validated by Reconcile or discarded by Reset. The
	// three snapshot accessors report COLD while it is set, so a caller can never
	// be served bytes that came off disk and have been checked against nothing.
	//
	// An atomic rather than a mu-guarded bool because the READERS are the three
	// snapshot accessors, called from foreign goroutines — the on-demand MCP
	// reflect handlers, which hold no reflection guard — while the writer is the
	// single refreshCorpusCache goroutine. It is one independent boolean with no
	// invariant tying it to any other mu-guarded field, so an atomic gives those
	// accessors the visibility they need without putting p.mu on a hot read path
	// or raising a lock-ordering question against the cache's own mutex.
	corpusUnvalidated atomic.Bool

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

	// admitted and wsWake are the working-set gate on the BACKGROUND entries of
	// this family — the tick and the boot cluster detection. Every read here
	// targets knowledge/default, so one predicate covers them all. Both nil in a
	// loop with no gate wired, which reads as NOT admitted: default-deny.
	// ForceFullPass is deliberately outside the gate — it is a user lever, not a
	// background process. See loop_workingset_gate.go.
	admitted func() bool
	wsWake   <-chan struct{}

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

	mu sync.Mutex
	// watermarkRestored records that the one-shot last-full-pass read has been
	// performed. The read moved off the boot path and onto the first ADMITTED
	// tick, so it cannot touch the knowledge graph before an interaction has
	// earned it. Guarded by mu.
	watermarkRestored bool
	lastClusters      []ThoughtCluster
	lastProfile       *PersonalityProfile
	leidenState       *graph.LeidenState
	lastAdj           map[string][]string
	lastTensions      []TensionReport
	lastBlindSpots    BlindSpotReport
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
//
// THE WORKING-SET GATE GOES HERE AND NOWHERE DEEPER. runPass and
// refreshCorpusCache are SHARED with ForceFullPass — the user lever behind
// thoughts(propagate, force_full:true) — so gating either of them would silently
// disable that lever. This entry is the background one; that one is a direct
// user interaction and stays ungated. A skipped tick costs nothing: the next
// admission wakes the loop.
func (p *PropagationLoop) runBackgroundPropagation() {
	if p == nil || p.gc == nil {
		return
	}
	if !p.knowledgeAdmitted() {
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

	// The last-full-pass watermark read used to happen at boot; it is a wire call,
	// so it now happens once, HERE, behind the gate and before the cadence check
	// that is its only reader.
	p.restoreWatermarkOnce(p.baseContext())

	forceFull := p.decideBackstopForce()
	// The tick discards runPass's (result, err): the pass logs its own outcome
	// (propagation-complete / budget-exceeded WARN / forced-pass log) internally, and
	// a failed tick simply re-runs next hour. The manual ForceFullPass lever consumes
	// the return to render an operator summary.
	_, _ = p.runPass(p.baseContext(), forceFull)
}
