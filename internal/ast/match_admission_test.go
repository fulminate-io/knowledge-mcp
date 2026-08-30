// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// match_admission_test.go — the walkAdmission seam and the admission tests.
//
// It is a SEPARATE file from peak_concurrency_test.go because lefthook's
// file-length hook (lefthook.yml:104-125) is a hard error above 500 lines and
// covers test files, and four steps of this work write test code.
//
// WHY THE TWO GATES IN THIS PACKAGE ARE NOT ONE GATE COUNTED TWICE. The
// in-flight census here and the heap-peak comparison in peak_concurrency_test.go
// are DIFFERENT INSTRUMENT CLASSES with complementary blind spots. The census is
// cheap and, on its capped arm, exact by construction — but it counts the gate's
// own admissions, so it cannot notice a permit released before the walk actually
// runs. The heap-peak comparison is noisy and machine-dependent, but it can only
// go green if the permit is genuinely held across the memory's lifetime.
// Together they catch a mis-sized budget and a budget held over the wrong window.
//
// DO NOT MARK ANY TEST IN THIS FILE, OR IN peak_concurrency_test.go, AS PARALLEL.
// See withWalkAdmission below for the mechanism and the gate.

// withWalkAdmission swaps the process-wide gate for one of the given size for
// the duration of the calling test, restoring the previous gate via tb.Cleanup.
//
// NEVER CALL THIS FROM A TEST MARKED PARALLEL. It assigns a package var, and Go
// resumes paused parallel top-level tests only after the sequential wave
// completes — so a sequential caller is safe and a parallel one is a data race.
// Five files in this package do mark tests parallel: comment_census_test.go,
// corpus_identity_test.go, opaque_text_corpus_test.go, wrapper_acceptance_test.go
// and wrapper_census_test.go. A criterion on this step gates the rule
// mechanically rather than trusting this sentence: it lists every _test.go naming
// withWalkAdmission and fails if any of them carries the parallel marker. That
// scan reads raw text, so it cannot tell a call from a quotation — which is why
// this comment describes the marker instead of spelling it.
//
// PRECEDENT, AND IT IS PARTIAL: withGOMAXPROCS at
// cmd/knowledge/internal/searchengine/bucket_group_parallel_test.go:152 is the
// same save/assign/restore intent around a package-level knob, but its SHAPE
// differs — it wraps a callback and restores with defer, where this restores via
// tb.Cleanup. The idiom is borrowed; the mechanism is not copied, and a reader
// comparing the two should expect them to look different.
func withWalkAdmission(tb testing.TB, slots int) {
	tb.Helper()
	prev := walkAdmission
	walkAdmission = newWalkAdmission(slots)
	tb.Cleanup(func() { walkAdmission = prev })
}

// TestMatch_AdmissionSlotsTrackNumCPU pins the SHIPPED budget. It is what stops
// the gate being silently widened to a no-op by a later edit — a gate whose
// slots outnumber any achievable in-flight count admits everything and asserts
// nothing.
func TestMatch_AdmissionSlotsTrackNumCPU(t *testing.T) {
	require.Equal(t, runtime.NumCPU(), walkAdmission.slots(),
		"the shipped walk admission budget must track runtime.NumCPU()")
}

// TestMatch_ConcurrentInFlightDoesNotMultiply is the STRUCTURAL expression of
// this work's property, stated with no ratio in it: under astPeakCalls
// concurrent Match calls the process-wide in-flight file count is NumCPU rather
// than astPeakCalls x NumCPU.
//
// THE TWO ARMS DO NOT HAVE THE SAME EPISTEMIC STATUS.
//
//   - The CAPPED arm is EXACT BY CONSTRUCTION. The permit channel's capacity is
//     a hard ceiling, so runtime.NumCPU() is an invariant here rather than an
//     observation, and an equality is the right assertion.
//   - The UNCAPPED arm is NOT deterministic, and must not be read as if it were.
//     Pooled 11 calibration runs read 119-128 in-flight, with only 4 of 11
//     reaching the arithmetic 128 — a loaded machine schedules fewer workers
//     into overlap. The assertion is therefore >= 2*runtime.NumCPU() against a
//     worst observation of 119: a 3.7x margin, NOT a determinism claim. Do not
//     tighten it to an equality on the strength of a good run.
//
// THE UNCAPPED ARM IS THE NON-VACUITY HALF. Without it, a run in which the calls
// never actually overlapped would satisfy the capped assertion trivially.
//
// Each arm installs a FRESH gate, whose counters start at zero, so neither
// reading can inherit a high-water mark set by another test in this binary.
// That is also why walkAdmissionGate carries no reset method: there is nothing
// for one to do.
func TestMatch_ConcurrentInFlightDoesNotMultiply(t *testing.T) {
	dir := countRetentionCorpus(t, astPeakStmtsPerFile)
	want := int64(runtime.NumCPU())

	// CAPPED ARM — a fresh gate at the shipped budget.
	withWalkAdmission(t, runtime.NumCPU())
	matchArmPeak(t, dir, astPeakCalls, true)
	capped := walkAdmission.highWater()
	require.Equal(t, want, capped,
		"a %d-slot gate cannot admit more than %d files at once; %d in flight means the permit is not held across matchFile",
		want, want, capped)

	// UNCAPPED ARM — the same corpus through an effectively unbounded gate.
	withWalkAdmission(t, 1<<20)
	matchArmPeak(t, dir, astPeakCalls, true)
	uncapped := walkAdmission.highWater()
	require.GreaterOrEqual(t, uncapped, 2*want,
		"the uncapped seam drove only %d files in flight — under %d concurrent calls the walk did not overlap, so the capped arm proved nothing",
		uncapped, astPeakCalls)

	t.Logf("in-flight high-water: capped=%d (exact, slots=%d) uncapped=%d (floor %d)",
		capped, want, uncapped, 2*want)
}

// TestMatch_SingleCallIsNotThrottled pins the property that JUSTIFIES the budget
// being runtime.NumCPU() rather than something smaller: an UNCONTENDED caller is
// not throttled by the gate. match_admission.go states that reasoning in prose —
// "a single call already runs min(NumCPU, len(files)) workers, so at NumCPU slots
// an UNCONTENDED call never blocks" — and until this test existed that claim was
// argued rather than checked.
//
// IT IS ASSERTED STRUCTURALLY, NOT ON THE CLOCK. A wall-time assertion would be a
// machine-dependent gate, which testdata/perf_baseline.txt is explicit this
// package does not write. The in-flight census states the same property without
// the noise: a single call must still get several files in flight at once.
//
// THE TWO BOUNDS HAVE DIFFERENT EPISTEMIC STATUS, as elsewhere in this file. The
// upper bound is EXACT BY CONSTRUCTION — the permit channel cannot admit more
// than its capacity. The lower bound is a NO-THROTTLE FLOOR, not a determinism
// claim: 16 workers each take a permit before any of them finishes parsing a
// 61KB file, so several are always in flight, but exactly how many is a
// scheduling outcome. Two is asserted because one would mean the gate had
// serialized a caller that contends with nobody.
func TestMatch_SingleCallIsNotThrottled(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("a 1-CPU machine gives the gate a permit channel of capacity 1, so a high-water of 2 is unreachable by a CORRECT implementation — the assertion would be measuring the host, not the gate")
	}
	dir := countRetentionCorpus(t, astIdentityStmtsPerFile)
	withWalkAdmission(t, runtime.NumCPU())

	medianArmPeak(t, dir, 1, false)

	hw := walkAdmission.highWater()
	require.LessOrEqual(t, hw, int64(runtime.NumCPU()),
		"a %d-slot gate cannot admit more than %d files at once", runtime.NumCPU(), runtime.NumCPU())
	require.GreaterOrEqual(t, hw, int64(2),
		"a single uncontended call got only %d file(s) in flight — the gate is serializing a caller that contends with nobody", hw)

	t.Logf("single-call in-flight high-water: %d (ceiling %d, no-throttle floor 2)", hw, runtime.NumCPU())
}

// astIdentityPattern is the pattern TestMatch_AdmissionPreservesResults compares
// under. It matches EVERY statement countRetentionCorpus generates — 10,000 over
// this test's corpus — so the comparison spans a substantial result set rather
// than the thin one astPeakPattern yields.
//
// It is named here rather than reusing astPeakPattern on purpose: a later change
// to the harness's pattern must not be able to silently thin this comparison.
// This test asserts EQUALITY, not memory, so the low-yield constraint that
// governs the peak harness does not apply to it.
//
// DO NOT REWRITE THIS AS "x$_ := $A + $B". That form parses, compiles, and
// matches ZERO — the placeholder lands inside an identifier token — so it is a
// silent zero that would hollow this entire comparison while every gate stayed
// green.
const astIdentityPattern = "$X := $A + $B"

// astIdentityStmtsPerFile is 200 and NOT astPeakStmtsPerFile. The 1-slot arm is
// serialized by construction, so a larger corpus costs suite time for no extra
// discrimination — and at 3,000 statements this pattern would carry 150,000
// matches through a fully serialized walk.
const astIdentityStmtsPerFile = 200

// TestMatch_AdmissionPreservesResults is the no-behaviour-change gate: the
// admission budget must bound WHEN files are walked, never WHAT the walk returns.
//
// THE 1-SLOT ARM IS THE ADVERSARIAL ONE. A gate of 1 fully serializes the walk —
// the maximum perturbation the mechanism can apply, and measurably so: at one
// slot the calibration's single-call wall went from ~120ms to 826-833ms. So this
// arm genuinely exercises the blocked path rather than a nominal one. The 1<<20
// arm is the control, and it exercises the SAME code path — admit is still
// called and still returns a real release closure — rather than a bypass, so the
// comparison isolates the BUDGET rather than the presence of the gate.
//
// THE COMPARISON IS ORDER-INSENSITIVE, AND THAT IS NOT A STYLE CHOICE. ast.Match
// does not sort at this layer: mergeMatches (count.go:351) appends each file's
// matches into the shared slice in worker-completion order. A naive slice
// equality would be flaky in BOTH tree states, before and after the gate — a
// scheduled false failure against correct work.
func TestMatch_AdmissionPreservesResults(t *testing.T) {
	dir := countRetentionCorpus(t, astIdentityStmtsPerFile)
	lang := treesitter.Language("go")
	pat, perr := Parse(astIdentityPattern)
	require.NoError(t, perr)
	cp, cerr := Compile(pat, lang, "")
	require.NoError(t, cerr)
	defer cp.Close()

	arm := func(slots int) ([]RawMatch, WalkStats) {
		withWalkAdmission(t, slots)
		raws, stats, merr := Match(context.Background(), dir, lang, cp, nil, Scope{Repo: "corpus"})
		require.NoError(t, merr)
		stats.DurationMS = 0
		return raws, stats
	}

	oneSlot, oneStats := arm(1)
	unbounded, unboundedStats := arm(1 << 20)

	// CARDINALITY GUARD. Set equality cannot see a set that emptied on BOTH
	// sides, so the count is cross-checked against ast.Count — an independent
	// second measurement of the same quantity, which must agree for some reason
	// other than sharing a bug with Match. Never assert one arm's length against
	// the other's alone: two sets that lost the same members are still equal.
	tally, _, terr := Count(context.Background(), dir, lang, cp, nil, Scope{Repo: "corpus"})
	require.NoError(t, terr)
	require.NotZero(t, tally.Total, "ast.Count found nothing — the corpus or the pattern is wrong, and any equality below would be vacuous")
	// Both lengths are asserted against tally.Total — an EXTERNAL expectation from
	// ast.Count — never against each other. require.Len(t, a, len(b)) would be the
	// hollowing this guard exists to catch: two sets that lost the same members
	// are still equal.
	require.Len(t, oneSlot, tally.Total, "the 1-slot arm's match count must agree with an independent ast.Count")
	require.Len(t, unbounded, tally.Total, "the unbounded arm's match count must agree with an independent ast.Count")

	require.ElementsMatch(t, oneSlot, unbounded,
		"a 1-slot gate returned a different match set than an effectively unbounded one — admission must bound when files are walked, never what the walk returns")

	// WALK COUNTER PARITY IS PART OF "NO BEHAVIOUR CHANGE". The whole struct is
	// compared rather than chosen fields, mirroring count_test.go:146-154, so a
	// field ADDED to WalkStats later is covered automatically. This is also where
	// the cancellation reasoning in match_admission.go is checked empirically: if
	// admit ever perturbed a skip attribution, SkippedRead / SkippedParseError /
	// SkippedParseLimit would move.
	require.Equal(t, unboundedStats, oneStats,
		"the walk's own counters must be identical under both budgets (DurationMS aside)")

	t.Logf("admission preserves results: %d matches under 1 slot and under 1<<20, ast.Count agrees at %d",
		len(oneSlot), tally.Total)
}
