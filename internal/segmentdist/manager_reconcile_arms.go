// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// coverageArm is the GENERAL PER-FORMAT READ SEAM: the non-generic view every
// per-format consumer walks the two engines through. It exists because distManager
// is generic over [Q, S] and the two live instantiations carry DIFFERENT type
// arguments — *distManager[[]byte, struct{}] for the HNSW arm (managerFor) and
// *distManager[bm25.Query, *bm25.CorpusStats] for the BM25 arm (bm25ManagerFor) —
// so Go cannot hold both in one slice. An interface whose method set mentions
// neither Q nor S can.
//
// IT HAS TWO CONSUMERS AND NEITHER OWNS IT: the per-format degeneracy probe
// (ReconcileResidentDegenerateByFormat) and the re-bucket detector (ReBucketNeeded).
// The method set below is therefore the UNION of what per-format consumers read,
// not one consumer's argument list — a type described by its first caller stops
// being extendable without a lie.
//
// Four of the eight methods are EXISTING distManager methods used verbatim (load,
// recoverIfDegenerate, recoverIfDegenerateWithShipped, shippedDocCountForRatio).
// The other four — residentDocCount, armFormat, distinctResidentDocCount and
// residentSegmentCount — are new FOR THE SEAM, and all four exist solely because
// the underlying state is a FIELD on a generic struct rather than a method.
//
// recoverIfDegenerate and recoverIfDegenerateWithShipped are BOTH pinned here: the
// arm probe calls the WithShipped variant (it has already read the denominator, so
// re-reading it would cost a second List), while recoverIfDegenerate remains the
// live read-side entry the WithShipped variant was factored out of. Declaring both
// makes a signature drift on either a build failure at the assertions below.
type coverageArm interface {
	load(context.Context) error
	// loadIfResident is load's DECLINING twin, for the background arms: an evicted
	// pool is skipped rather than re-materialized. load stays declared beside it —
	// the consumer arms still reach it, and dropping it would remove the drift
	// tripwire the assertions below exist for.
	loadIfResident(context.Context) (bool, error)
	recoverIfDegenerate(context.Context) error
	recoverIfDegenerateWithShipped(context.Context, int, bool) error
	residentDocCount() int
	distinctResidentDocCount() int
	residentSegmentCount() int
	shippedDocCountForRatio(context.Context) (int, bool, error)
	armFormat() string
}

// Compile-time satisfaction assertions for BOTH live instantiations, so a future
// signature drift on any of the methods is a build failure here rather than a
// silently dropped arm at the probe.
var (
	_ coverageArm = (*distManager[[]byte, struct{}])(nil)
	_ coverageArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// residentDocCount exposes the engine's resident doc count through the coverageArm
// seam. engine is a generic FIELD, so the interface cannot reach it directly.
func (m *distManager[Q, S]) residentDocCount() int { return m.engine.ResidentDocCount() }

// armFormat exposes this engine's segment format name (each format supplies its own) through the
// coverageArm seam, for per-arm verdict attribution and the per-arm debug record.
// format is a plain FIELD, hence the wrapper.
func (m *distManager[Q, S]) armFormat() string { return m.format }

// ArmVerdict is ONE format arm's degeneracy verdict: the resident counts either side
// of the server re-import, the shipped denominator the ratio was taken against, and
// the disarm/degenerate outcome. Err carries THIS arm's probe error and is nil when
// the arm was evaluated cleanly; an errored arm always reports Degenerate false,
// because an arm that could not be measured must never drive a rebuild.
//
// Shipped and Disarm are meaningful only when the arm went past the entry floor
// gate. An arm whose post-load resident already clears the floor is healthy without
// reading a denominator, so it reports Shipped 0 / Disarm false — that is a
// short-circuit artifact, not a measurement of an empty corpus.
//
// Evicted follows exactly the same reasoning one step earlier: the residency budget
// unloaded this pool to reclaim memory, the background probe declines to resurrect
// it, and so every count below is a short-circuit artifact of a pool NOBODY LOADED
// rather than a measurement — ResidentAfterLoad/ResidentAfterRecover 0, Shipped 0,
// Disarm false, Degenerate false, Err nil.
//
// CONSUMER WARNING, and it is the reason Evicted is a field rather than an inference:
// {Evicted:true, Degenerate:false, Err:nil} is INDISTINGUISHABLE FROM
// MEASURED-AND-HEALTHY to any consumer that does not read Evicted. Every consumer of
// this type must branch on it. The four that exist:
//   - client_segment_reconcile_graph.go's rebuild decision — declines on
//     Degenerate=false, so it is already correct, and ReconcileResidentDegenerate's
//     doc now says so explicitly rather than leaving it an accident of the OR;
//   - propagation_vector_deps.go HNSWCoverageTrustworthy — must NOT report the arm
//     trustworthy, or the propagation loop materializes the pool hourly;
//   - client_segment_bm25_gate.go hnswArmProbe — must take its documented
//     could-not-be-read path; an evicted arm was not read;
//   - client_segment_bm25_gate.go healNeedsRebuildBM25 — must decline WITHOUT
//     clearing the no-progress bound, which an unbranched !Degenerate would clear.
type ArmVerdict struct {
	Format               string
	ResidentAfterLoad    int
	ResidentAfterRecover int
	Shipped              int
	Disarm               bool
	Degenerate           bool
	Evicted              bool
	Err                  error
}

// ReconcileResidentDegenerateByFormat evaluates EVERY format arm of one graph
// independently and returns a verdict per arm. Each arm is measured against its OWN
// format's shipped denominator: a distManager's segment source is constructed
// format-scoped and shippedDocCountForRatioFromSnapshot filters by keepFormat, so an
// arm's List already yields only that format's shipped corpus under the identical
// floor/ratio/disarm rules.
//
// PER-ARM ERROR ISOLATION. A load/recover/List failure on one arm records that arm's
// Err, leaves its Degenerate false, and the probe CONTINUES to the next arm. A
// top-level error is returned ONLY when EVERY arm errored. This matters because each
// format's L2 cache is rooted separately (graphCacheDirFor), so one arm can be cold
// in exactly the processes where the other is warm: propagating a cold arm's error
// would destroy the other arm's server-independent L2-first verdict, which is a
// documented contract of the reconcile probe.
func (m *Manager) ReconcileResidentDegenerateByFormat(
	ctx context.Context, gt kgtypes.GraphType, name string,
) ([]ArmVerdict, error) {
	arms := []coverageArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)}

	verdicts := make([]ArmVerdict, 0, len(arms))
	var firstErr error
	errored := 0
	for _, arm := range arms {
		v := armCoverageVerdict(ctx, gt, name, arm)
		if v.Err != nil {
			errored++
			if firstErr == nil {
				firstErr = v.Err
			}
		}
		verdicts = append(verdicts, v)
	}
	if errored == len(arms) {
		// Nothing was measurable — surface the failure rather than reporting a
		// healthy-looking all-false verdict set.
		return verdicts, firstErr
	}
	return verdicts, nil
}

// armCoverageVerdict runs ONE arm's detect sequence and returns its verdict. It is
// the existing single-format sequence (cache-first load, cheap server re-import,
// resident-vs-shipped read) restructured to spend ONE List instead of two, and it
// never returns an error: a failure is recorded in the verdict's Err so the caller
// can keep evaluating the other arm.
//
// The ordering is load-bearing at two points:
//
//   - The ENTRY FLOOR GATE (step 2) preserves the zero-RPC property for a healthy
//     arm: resident >= floor short-circuits before any List or Fetch. No read may
//     move above it.
//   - Step 5 RE-APPLIES the floor BEFORE the ratio, matching the verdict switch this
//     replaces. A recovery that lands at or above the floor but below the ratio is
//     HEALTHY. Collapsing step 5 to a bare belowCoverageRatio call would flag such a
//     partial re-import as degenerate and drive a rebuild that cannot raise the read
//     pool it is measured against.
//
// THE MANIFEST-COMPLETENESS PASS RUNS EARLIER IN THE SAME TICK, and it does NOT
// weaken the invariant above. ReconcileManifestCompleteness
// (manager_completeness.go) converges each arm's L2 cache toward its published
// manifest before this probe runs, because the two answer different questions: this
// one asks whether the resident pool is degenerate against a doc-count ratio, that
// one asks whether the cache is SHORT of the id set actually published — a shortfall
// that is invisible here, since a cache holding a quarter of the corpus can still
// clear the floor and read as perfectly healthy.
//
// ITS READ IS GATED THE SAME WAY THIS ONE IS. It compares a persisted per-format
// manifest fingerprint against len(cache.Keys()) — two LOCAL numbers, no network —
// and pays source.List(0) only inside the resulting mismatch branch. So the
// no-read-above-the-floor property still describes this function exactly, and the
// healthy arm still reaches step 2 having paid nothing.
func armCoverageVerdict(
	ctx context.Context, gt kgtypes.GraphType, name string, arm coverageArm,
) (v ArmVerdict) {
	v.Format = arm.armFormat()

	// Per-arm reconcile diagnostic (kept per keep-debug-logging): one record per arm
	// at its decision point, carrying the format so the verdict is attributable. The
	// deferred emit covers every exit path, including the error and short-circuit
	// ones. Off the bind path (boot-delay one-shot + periodic loop only), so it never
	// touches first-search readiness.
	defer func() {
		slog.Debug("segmentdist: resident degeneracy reconcile probe",
			"graph_type", gt, "name", name, "format", v.Format,
			"resident_after_load", v.ResidentAfterLoad,
			"resident_after_recover", v.ResidentAfterRecover,
			"shipped", v.Shipped,
			"floor", residentBackstopFloor,
			"disarm", v.Disarm,
			"degenerate", v.Degenerate,
			"evicted", v.Evicted,
			// ANNOUNCE THE SEMANTICS ON THE LINE ITSELF. Every resident count above is
			// a sum of DISTINCT member ids per segment. It did not always mean that: a
			// segment carrying the same id more than once used to contribute one per
			// COPY, so on a corpus that accumulated duplicate layers these numbers were
			// inflated and now read roughly half what they did. THE DROP IS THE COUNT
			// BECOMING CORRECT, NOT DOCUMENTS DISAPPEARING — without this note the fall
			// is indistinguishable from the index loss this change repairs.
			"count_semantics", "resident counts are distinct member ids; a one-time drop here is the count being corrected, not data lost",
			"err", v.Err)
	}()

	// 1. Cache-first load: the L2-first primary path imports the warm L2 resident set
	// server-independently, so an arm whose lazy load would heal is not flagged.
	//
	// AN EVICTED POOL IS DECLINED HERE, BEFORE THE ENTRY FLOOR GATE, and the order is
	// load-bearing. An evicted pool reads a resident doc count of 0, which is below
	// residentBackstopFloor, so letting it fall through would read the shipped
	// denominator and then call recoverIfDegenerateWithShipped — which resets the load
	// floor and re-imports the FULL corpus from the server. Every eviction would be
	// undone on the next reconcile tick, at full network cost.
	skipped, err := arm.loadIfResident(ctx)
	if err != nil {
		v.Err = err
		return v
	}
	if skipped {
		v.Evicted = true
		return v
	}
	v.ResidentAfterLoad = arm.residentDocCount()

	// 2. ENTRY FLOOR GATE — a healthy arm stops here having paid zero List and zero
	// Fetch. ResidentAfterRecover mirrors the post-load count: no recovery ran.
	if v.ResidentAfterLoad >= residentBackstopFloor {
		v.ResidentAfterRecover = v.ResidentAfterLoad
		return v
	}

	// 3. Below the floor: read this arm's shipped denominator ONCE. It serves BOTH
	// the recovery decision and the verdict below, so the two provably agree on one
	// corpus snapshot instead of racing two reads of it.
	shipped, disarm, err := arm.shippedDocCountForRatio(ctx)
	if err != nil {
		v.Err = err
		return v
	}
	v.Shipped, v.Disarm = shipped, disarm

	// 4. Cheap server re-import with the denominator already in hand: resets the load
	// floor and re-imports from the server, bypassing the load once-guard that makes
	// a plain load() a no-op for an arm a prior load already partially filled.
	if err := arm.recoverIfDegenerateWithShipped(ctx, shipped, disarm); err != nil {
		v.Err = err
		return v
	}

	// 5. Verdict, FLOOR FIRST (see the doc comment): a recovery that cleared the
	// floor is healthy without consulting the ratio at all.
	v.ResidentAfterRecover = arm.residentDocCount()
	switch {
	case v.ResidentAfterRecover >= residentBackstopFloor:
		v.Degenerate = false
	default:
		v.Degenerate = belowCoverageRatio(v.ResidentAfterRecover, shipped, disarm)
	}
	return v
}

// recoverIfDegenerateWithShipped is recoverIfDegenerate's body MINUS its own shipped
// -count List: the caller has already read (shipped, disarm) and passes them in, so
// a caller that needs the denominator for its own verdict spends ONE List rather
// than two. recoverIfDegenerate itself is the thin live wrapper that reads the
// denominator and delegates here, so there is exactly one copy of this logic.
//
// It assumes the caller has ALREADY applied the entry floor gate — the gate lives in
// each caller because both of them need the resident count anyway.
//
// The ratio test is belowCoverageRatio, shared verbatim with the publish gate and
// with the arm verdict, so a degeneracy decision and the verdict taken over it
// cannot drift apart.
//
// Single-flight: load() takes no package lock (shipMu guards ship state only), so K
// concurrent degenerate searches would each reset the floor and re-import the
// corpus. The recovering atomic.Bool CAS elects ONE recovering goroutine; the rest
// skip (the winner's load() makes the corpus resident shortly). The flag is released
// on EVERY exit path (incl. the load() error path) via defer. Import dedup already
// makes a concurrent re-import merely wasteful (not corrupting), so single-flight is
// a cost bound, not a correctness requirement — kept belt-and-suspenders.
func (m *distManager[Q, S]) recoverIfDegenerateWithShipped(ctx context.Context, shipped int, disarm bool) error {
	// Not degenerate: a disarmed denominator (a pre-doc_count blob or a sub-floor
	// corpus) or a resident pool already covering the ratio.
	if !belowCoverageRatio(m.engine.ResidentDocCount(), shipped, disarm) {
		return nil
	}

	// SINGLE-FLIGHT the recovery: only the CAS winner resets the floor + re-imports.
	if !m.recovering.CompareAndSwap(false, true) {
		return nil // another goroutine is already recovering this engine — skip.
	}
	defer m.recovering.Store(false)

	// Reset the load floor to 0 so the forced load Lists(0) and re-imports the full
	// corpus (Import dedup drops any already-resident segment), then force that load.
	//
	// Call loadFromServer directly, NOT load(): the degeneracy this net heals is an
	// in-memory engine that covers far less than the SERVER's shipped corpus, and the
	// missing segments are on the server, not in L2 (the L2-first primary path is
	// exactly what produced/left the degenerate resident set). load() would either
	// short-circuit on the l2Loaded once-guard (already set this process) or re-import
	// only the L2 tail — neither recovers the server corpus. loadFromServer Lists(0)
	// and Fetches the misses, which is the recovery's whole purpose.
	m.importedGen.Store(0)
	return m.loadFromServer(ctx)
}
