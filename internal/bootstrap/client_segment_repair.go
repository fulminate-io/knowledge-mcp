// SPDX-License-Identifier: Apache-2.0

// client_segment_repair.go — the reconcile cascade's COVERAGE-REPAIR detector.
//
// It owns the band the other two arms leave open. Below the band a graph's live
// pool has collapsed and healNeedsRebuild rebuilds it; above the band a graph is
// converged. In between sits the quiet failure this arm exists for: a graph that is
// fully embedded, perfectly healthy by every existing probe, and simply missing some
// of its documents from the searchable corpus because the ship that would have
// carried them was swallowed or lost with the process.
//
// THE TRIGGER CANNOT BE `covered < embedded`. The embedded denominator counts
// vectored nodes the rebuild scan deliberately excludes — tombstoned ones (a soft
// delete never clears the vector) and embed-failure-marked ones — so that difference
// is bounded below by a permanent structural residue that survives even a perfect
// from-scratch rebuild, and grows as tombstones are retained. An arm triggering on it
// would re-fire every tick forever. So the trigger is SELF-CALIBRATING: after a pass
// it re-reads both operands and remembers the gap it settled at, and only a gap
// LARGER than that remembered residue fires again.
//
// THE ROUND-ROBIN SCHEDULING LIVES IN client_segment_repair_rotation.go — the per-pass
// reset, the offer and the grant this cascade calls at STEP 1 and STEP 7b. It is a
// separate concern: that file decides whose turn it is, this one decides whether the
// graph whose turn it is actually needs a repair.

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// repairFailureDisarm is how many CONSECUTIVE failed repair passes disarm the arm
// for a graph. It is ARM-LOCAL on purpose: the shared heal breaker latches a graph's
// whole auto-heal, and a repair that cannot run is no reason to stop healing a pool
// that collapses later.
const repairFailureDisarm = 3

// repairUncoveredGraph is the detector: it decides whether ONE graph sits in the
// coverage-repair band and, if so, runs a pass and recalibrates from what it
// observed. Best-effort like every arm of the cascade — one graph's failure never
// stops the sweep.
func (c *client) repairUncoveredGraph(ctx context.Context, g segmentGraphRef, periodic bool) {
	c.repairUncoveredGraphWith(ctx, g, clientRepairDeps{c: c}, periodic)
}

// repairCoverageReads is STEPS 2 and 3 of the detector: the band's denominator
// (embedded) and its load-first numerator (live resident). They are extracted
// together because they share ONE convention — either read failing SKIPS this graph
// for this pass rather than letting the band arithmetic run on a number the arm does
// not have — and because a decider must never conclude "uncovered" from a read it
// could not make.
//
// ok=false means "not read", never "read as zero": every caller must decline on it.
func repairCoverageReads(
	ctx context.Context, g segmentGraphRef, deps repairArmDeps,
) (embedded, covered int, ok bool) {
	embedded, err := deps.EmbeddedCount(ctx, g)
	if err != nil {
		slog.Warn("bootstrap: segment repair could not read the embedded count (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, 0, false
	}
	covered, err = deps.LiveResidentCount(ctx, g)
	if err != nil {
		slog.Warn("bootstrap: segment repair could not read the live resident count (skipping this graph this pass)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return 0, 0, false
	}
	return embedded, covered, true
}

// repairBandAdmits reports whether a graph's coverage counts sit in the band the
// BOUNDED repair arm serves. Its three clauses are the arm's admission rule and each
// is load-bearing: below the floor the ratio is noise and the zero-presence heal owns
// the graph; at or above the denominator this is the over-coverage residue class, not
// this arm's; below the ratio the existing heal already fires.
//
// ITS OPERANDS ARE COVERAGE COUNTS, and a caller supplying a different pair is making
// a claim rather than reusing a helper: `embedded` is the denominator of nodes the
// local index is expected to hold, and `covered` is the numerator of distinct
// live-searchable ids it actually holds.
//
// TWO CALLERS: the periodic backstop detector's STEP 4 below, and the quiescence
// balance verdict's routing to this same arm. It is EXTRACTED rather than copied for
// the reason degenerateAgainstEmbedded records at its own declaration — a second copy
// of a band predicate beside the first is precisely how the two come to disagree about
// which graphs need rebuilding.
func repairBandAdmits(embedded, covered int) bool {
	return embedded >= tools.SegmentCoverageFloor &&
		covered < embedded &&
		float64(covered) >= tools.CoverageRatioThreshold*float64(embedded)
}

// repairUncoveredGraphWith is the body, parameterized on its dependencies.
func (c *client) repairUncoveredGraphWith(
	ctx context.Context, g segmentGraphRef, deps repairArmDeps, periodic bool,
) {
	// STEP 0 — THE BACKSTOP GATE. This arm is no longer the freshness path; the delta
	// merge is. What is left here is a corruption/loss backstop, so it runs only on a
	// PERIODIC sweep and only for a graph whose persisted record does not already
	// answer the question.
	//
	// IT PRECEDES THE ROUND-ROBIN OFFER ON PURPOSE. The offer advances the rotation's
	// OFFER COUNT for every graph it sees, so a gated graph placed after it would take a
	// turn it then declines. It also precedes the two coverage reads, which is the RPC
	// cost it exists to avoid paying — and after the offer/grant split that matters
	// MORE, not less: every graph offered before the tick grants now pays those reads,
	// so the gate that keeps converged graphs from being offered at all is what bounds
	// the fleet-wide cost.
	//
	// An unreadable record falls through to a scan, which is the safe direction: an
	// un-run backstop costs one pass, a wrongly-skipped one leaves corruption unfound.
	// IT READS Converged AND VerifiedAtNanos ONLY — never RepairState.Scanned. A
	// seeded record answers THIS gate's question correctly (the arm has nothing to do
	// for that graph); Scanned exists for the coverage column, which asks a different
	// question, and consulting it here would put every declined graph back on the
	// rotation for no gain.
	if !periodic {
		return
	}
	st, err := deps.LoadRepairState(g)
	if err == nil && st.Converged && deps.NowNanos()-st.VerifiedAtNanos < int64(tools.SegmentRepairBackstopInterval) {
		return
	}

	// STEP 0b — THE RESIDENCY GATE (see repairArmDeps.Evicted for why a zero from an
	// evicted pool must not reach STEP 4's band, and why this writes NO STEP 4a
	// record). Ahead of the offer below for STEP 0's reason, and so an evicted pool is
	// never loaded to answer a question this arm has already declined.
	if deps.Evicted(g) {
		return
	}

	// STEP 1 — the round-robin OFFER. It does not spend the tick's grant; STEP 7b does,
	// once every gate that can decline has passed. A graph the offer turns away costs
	// nothing at all — not a scan, not even the two reads — and once the tick has
	// granted, every later graph is turned away here.
	//
	// AN OFFERED GRAPH THAT LATER DECLINES DOES pay the two coverage reads, which is the
	// price of the fix: the alternative is a decliner consuming the tick's only grant and
	// every graph behind it waiting a whole tick. Three things bound the set that pays —
	// STEP 0 excludes every graph with a converged record fresher than the backstop
	// interval, STEP 0b excludes evicted pools, and a STEP 4 decliner seeds a record that
	// makes STEP 0 suppress it from the next tick onward.
	if !c.offerRepairSlot() {
		return
	}

	// STEPS 2 and 3 — the denominator and the load-first numerator.
	embedded, covered, read := repairCoverageReads(ctx, g, deps)
	if !read {
		return
	}

	// STEP 4 — the band, through the one predicate the quiescence verdict's routing
	// shares. Every clause is load-bearing; repairBandAdmits states which and why.
	if !repairBandAdmits(embedded, covered) {
		// STEP 4a — THE DECLINED-GRAPH SEED. A graph this arm declines never reaches
		// STEP 8, so it would never earn a repair record and never earn a merge
		// horizon: STEP 0 would re-read its two coverage counts on every rotation
		// forever, and the delta merge's no-horizon rule would leave it pulling
		// nothing for the life of the machine. Both holes close here, at most once
		// per graph per process.
		c.seedBackstopRecord(ctx, g, deps)
		return
	}

	// STEP 5 — calibration. A gap no larger than the residue the last pass settled at
	// is the structural difference, not a repairable hole.
	//
	// It prefers the PERSISTED residue when the record's converged bit is set, so the
	// convergence a previous process reached survives a restart. ONLY WHEN CONVERGED:
	// an unconverged record is one whose pass did not settle, and seeding the gate
	// from it would let one bad pass mask a real gap forever, where the in-memory
	// version bounded that mistake to a single process. Scanned is deliberately not
	// consulted either — a seeded record carries Residue 0, which this comparison
	// ignores on its own.
	gap := embedded - covered
	residueBefore := c.repairResidue(g)
	if st.Converged && st.Residue > residueBefore {
		residueBefore = st.Residue
	}
	if gap <= residueBefore {
		return
	}

	// STEP 6 — the ARM-LOCAL disarm. Without consulting the counter here it would be
	// decorative and the third consecutive failure would change nothing.
	failures := c.repairFailures(g)
	if failures >= repairFailureDisarm {
		slog.Debug("bootstrap: segment repair disarmed for graph after consecutive failures",
			"graph_type", g.gt, "name", g.name, "failures", failures)
		return
	}

	// STEP 7 — the shared breaker, READ ONLY. A graph whose auto-heal has latched
	// does not also get repair traffic.
	if !deps.BreakerAllows(g) {
		slog.Debug("bootstrap: segment repair skipped — the auto-heal breaker is latched for this graph",
			"graph_type", g.gt, "name", g.name)
		return
	}

	// STEP 7b — SPEND THE TICK'S GRANT. Placed after every gate that can decline, which
	// is the whole point of splitting the offer from the grant: the slot is spent on a
	// graph that is about to be repaired, never on one that is about to say no.
	c.grantRepairSlot()

	// STEP 8 — the pass.
	out, err := deps.Repair(ctx, g)
	if err != nil {
		// The residue is deliberately NOT touched and NOTHING is recorded against the
		// shared breaker: it latches after two no-progress passes and would disarm this
		// graph's WHOLE auto-heal until a manual rebuild.
		failures = c.noteRepairFailure(g)
		slog.Warn("bootstrap: segment repair pass failed (continuing; the arm retries next tick)",
			"graph_type", g.gt, "name", g.name, "failures", failures, "error", err)
		return
	}

	// STEP 9 — recalibrate, and ONLY on a pass that actually RAN. Guarding on err
	// alone is not enough: the single-flight coalesce returns the zero-value outcome
	// with a NIL error, so falling through would record the current UN-REPAIRED gap as
	// settled residue and kill the arm for the process lifetime.
	if !out.Ran {
		slog.Debug("bootstrap: segment repair coalesced into a pass already running (nothing recorded)",
			"graph_type", g.gt, "name", g.name)
		return
	}
	residueAfter, ok := c.calibrateRepairResidue(ctx, g, deps, out)
	if !ok {
		return
	}

	// STEP 10 — report.
	logRepairOutcome(g, out, repairPassOperands{
		embedded: embedded, covered: covered, gap: gap,
		residueBefore: residueBefore, residueAfter: residueAfter, failures: failures,
	})
}

// repairPassOperands carries what the pass observed around its scan, purely so the
// reporting line below can be lifted out of the cascade without threading six bare
// ints through it.
type repairPassOperands struct {
	embedded, covered, gap      int
	residueBefore, residueAfter int
	failures                    int
}

// logRepairOutcome emits the pass's attribution record. INFO when the pass shipped,
// DEBUG when it converged on nothing, because a converged pass is the steady state
// and should not be noise.
func logRepairOutcome(g segmentGraphRef, out tools.RepairOutcome, op repairPassOperands) {
	attrs := []any{
		"graph_type", g.gt, "name", g.name,
		"embedded_before", op.embedded, "covered_before", op.covered, "gap", op.gap,
		"residue_before", op.residueBefore, "residue_after", op.residueAfter,
		"failures", op.failures,
		"scanned_eligible", out.ScannedEligible,
		"missing_hnsw", out.MissingHNSW, "missing_bm25", out.MissingBM25,
		"shipped_hnsw", out.ShippedHNSW, "shipped_bm25", out.ShippedBM25,
	}
	if out.ShippedHNSW+out.ShippedBM25 > 0 {
		slog.Info("bootstrap: segment repair shipped the uncovered difference for a graph in the coverage band", attrs...)
		return
	}
	slog.Debug("bootstrap: segment repair pass found nothing uncovered", attrs...)
}

// calibrateRepairResidue re-reads BOTH operands after a pass that RAN and records
// the gap it settled at, returning that residue and whether it was recorded.
//
// RE-READING IS WHAT MAKES CONVERGENCE ROBUST. Calibrating from the operands sampled
// BEFORE the scan lets deletes inside the pass window inflate the residue
// permanently, and it makes convergence depend on the trigger and the diff agreeing
// about the corpus. A pass that found nothing missing records exactly the gap it
// observed, whatever the two measures disagree about.
//
// A re-read that fails leaves the residue UNCHANGED rather than guessing: an
// un-calibrated arm costs one extra pass, a wrongly-calibrated one is dead.
//
// IT ALSO PERSISTS, and the durable record is what turns the in-process convergence
// into a cross-restart one.
//
// THE CONVERGED BIT IS A SNAPSHOT CLAIM, and its scope is the whole point: it says
// everything embedded AS OF SCAN START is covered, not that the corpus has stopped
// moving. A write landing after that instant is the CURRENCY PATH's business — the
// local write lifecycle for our own writes, the periodic delta merge for a
// co-worker's — so this arm neither chases it nor lets it withhold convergence.
// Requiring the denominator to hold still across the pass would mean a graph that is
// written WHILE it is scanned could never record convergence, and would therefore
// re-scan its entire corpus on every boot — the exact cost this backstop exists to
// remove, reappearing on the busiest graph in the system.
//
// So the bit reduces to the two per-format counts, and they are NOT defensive
// symmetry: the formats diverge in production, because an id with no vector builds no
// HNSW document and an id with no indexable fields builds no BM25 one, so one format
// can be clean while the other is not. Both are measured against the scan-start
// eligible set, which is what makes the claim a snapshot claim rather than a race.
//
// SCANNED IS TRUE HERE AND ONLY HERE, unconditionally: this is the one path on which
// the corpus was actually examined, and "a pass looked and did not settle" is still a
// pass that looked. The seed path leaves it false; the coverage column ANDs the two,
// so an unconverged-but-scanned record still reads as unverified there.
func (c *client) calibrateRepairResidue(
	ctx context.Context, g segmentGraphRef, deps repairArmDeps, out tools.RepairOutcome,
) (int, bool) {
	embeddedAfter, aerr := deps.EmbeddedCount(ctx, g)
	coveredAfter, cerr := deps.LiveResidentCount(ctx, g)
	if aerr != nil || cerr != nil {
		slog.Warn("bootstrap: segment repair ran but could not re-read its operands to calibrate (residue unchanged)",
			"graph_type", g.gt, "name", g.name, "embedded_err", aerr, "covered_err", cerr)
		return 0, false
	}
	// Clamped: deletes inside the pass window can leave covered above embedded, and a
	// negative residue would make the gate meaningless.
	residueAfter := max(0, embeddedAfter-coveredAfter)
	c.setRepairResidue(g, residueAfter)
	c.clearRepairFailures(g)

	converged := out.MissingHNSW == 0 && out.MissingBM25 == 0
	if serr := deps.SaveRepairState(g, segmentdist.RepairState{
		Residue: residueAfter, Converged: converged, Scanned: true, VerifiedAtNanos: deps.NowNanos(),
	}); serr != nil {
		slog.Warn("bootstrap: segment repair could not persist its backstop record (continuing; the graph is re-offered next rotation)",
			"graph_type", g.gt, "name", g.name, "error", serr)
		return residueAfter, true
	}
	// THE SCANNED-GRAPH HALF OF THE COLD-START BRIDGE. The backstop's scan is
	// unwatermarked, so the horizon the server served it IS the correct starting point
	// for this graph's first delta window: everything at or before it was just
	// examined by a full scan. Written AFTER the record so a crash between them leaves
	// a graph that is converged-but-unseeded — one extra rotation, no lost data —
	// rather than seeded-but-unverified, which would start merging windows for a graph
	// whose corpus was never checked.
	//
	// The `> 0` guard is a CLIENT-SIDE rule: a zero means only "this scan was served
	// no horizon", and persisting it would re-arm the whole-corpus pull the merge
	// refuses.
	if out.ServedHorizonNanos > 0 {
		if merr := deps.SaveMergeWatermark(g, out.ServedHorizonNanos); merr != nil {
			slog.Warn("bootstrap: segment repair could not seed the graph's merge horizon (continuing; the next rotation retries)",
				"graph_type", g.gt, "name", g.name, "error", merr)
		}
	}
	return residueAfter, true
}

// seedBackstopRecord records what a DECLINED graph is worth recording: that this arm
// looked and had nothing to do, and the server horizon its delta merge should start
// from. It is NOT a claim that the graph was scanned — nothing scanned it — and that
// distinction is carried by RepairState.Scanned, which this path leaves FALSE.
//
// The record's three consumers each read the fields they need:
//   - STEP 0, which wants "this arm has nothing to do here" and gets it from Converged
//     plus the stamp;
//   - STEP 5's residue seeding, which reads Residue and is handed a ZERO here, so it
//     can never raise a residue and can never mask a gap;
//   - the coverage column's disposition, which reads SCANNED — not Converged — so a
//     seeded graph that later grows into the gap-repairing band reads "cache-aged"
//     rather than claiming a verification that never happened.
//
// ALL THREE BAND CLAUSES SEED, because all three describe a graph this arm declines:
// below the floor the zero-presence heal owns it, at or above the denominator it is
// the residue class, and below the ratio the existing auto-heal owns it — and that
// heal runs on every pass, unscoped by the round-robin, so a seeded horizon hides
// nothing from it.
//
// THE ONCE-PER-PROCESS GUARD IS NOT AN OPTIMIZATION: without it a graph whose record
// write kept failing would re-issue the horizon probe on every rotation forever.
func (c *client) seedBackstopRecord(ctx context.Context, g segmentGraphRef, deps repairArmDeps) {
	if !c.claimBackstopSeed(g) {
		return
	}
	h, err := deps.ServedHorizon(ctx, g)
	if err != nil {
		slog.Debug("bootstrap: segment backstop could not read the served horizon to seed a declined graph (continuing)",
			"graph_type", g.gt, "name", g.name, "error", err)
		return
	}
	// Record FIRST, horizon second — the same crash-window argument the calibration
	// makes: converged-but-unseeded costs one extra rotation, seeded-but-unverified
	// would merge windows for a graph nothing checked.
	if serr := deps.SaveRepairState(g, segmentdist.RepairState{
		Converged: true, Scanned: false, VerifiedAtNanos: deps.NowNanos(),
	}); serr != nil {
		slog.Debug("bootstrap: segment backstop could not persist a declined graph's record (continuing)",
			"graph_type", g.gt, "name", g.name, "error", serr)
		return
	}
	if h > 0 {
		if merr := deps.SaveMergeWatermark(g, h); merr != nil {
			slog.Debug("bootstrap: segment backstop could not seed a declined graph's merge horizon (continuing)",
				"graph_type", g.gt, "name", g.name, "error", merr)
		}
	}
}

// claimBackstopSeed reports whether this process has yet to attempt the declined-graph
// seed for g, marking it attempted. The map is created lazily on first write exactly
// as segmentRepairResidue is, so a *client built directly by a test harness needs no
// extra wiring.
func (c *client) claimBackstopSeed(g segmentGraphRef) bool {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	if c.segmentBackstopSeeded == nil {
		c.segmentBackstopSeeded = make(map[segmentGraphRef]struct{})
	}
	if _, done := c.segmentBackstopSeeded[g]; done {
		return false
	}
	c.segmentBackstopSeeded[g] = struct{}{}
	return true
}

// claimFloorRecovery reports whether this process has yet to attempt the
// unreadable-retention-floor recovery rebuild for g, marking it attempted. Same
// shape and same mutex as claimBackstopSeed, and lazily created for the same reason:
// a *client built directly by a test harness needs no extra wiring.
func (c *client) claimFloorRecovery(g segmentGraphRef) bool {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	if c.segmentFloorRecovered == nil {
		c.segmentFloorRecovered = make(map[segmentGraphRef]struct{})
	}
	if _, done := c.segmentFloorRecovered[g]; done {
		return false
	}
	c.segmentFloorRecovered[g] = struct{}{}
	return true
}

// repairResidue reads the graph's calibrated residue (absent = 0).
func (c *client) repairResidue(g segmentGraphRef) int {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	return c.segmentRepairResidue[g]
}

// setRepairResidue records the gap the finished pass settled at. The map is created
// lazily on first write so a *client built directly by a test harness — which is how
// most of this package's tests construct one — needs no extra wiring.
func (c *client) setRepairResidue(g segmentGraphRef, residue int) {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	if c.segmentRepairResidue == nil {
		c.segmentRepairResidue = make(map[segmentGraphRef]int)
	}
	c.segmentRepairResidue[g] = residue
}

// repairFailures reads the graph's consecutive-failure count (absent = 0).
func (c *client) repairFailures(g segmentGraphRef) int {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	return c.segmentRepairFailures[g]
}

// noteRepairFailure increments and returns the graph's consecutive-failure count.
func (c *client) noteRepairFailure(g segmentGraphRef) int {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	if c.segmentRepairFailures == nil {
		c.segmentRepairFailures = make(map[segmentGraphRef]int)
	}
	c.segmentRepairFailures[g]++
	return c.segmentRepairFailures[g]
}

// clearRepairFailures resets the streak after a pass that ran.
func (c *client) clearRepairFailures(g segmentGraphRef) {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	delete(c.segmentRepairFailures, g)
}
