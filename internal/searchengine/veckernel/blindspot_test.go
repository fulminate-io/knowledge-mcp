// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"strings"
	"testing"
)

// blindspot_test.go carries two things the agreement suite would otherwise
// leave implicit: the ranking gate's own known-positive controls, and a pinned
// statement of what the scale-relative tolerance CANNOT see.

// TestScaleRelativeGateBlindSpot PINS a known limit of the ratified tolerance
// shape — discovered by running the known-positive controls, not reasoned about
// in advance.
//
// THE LIMIT: on a strongly-canceling corpus — which is what two random
// embedding vectors are — the scale-relative gate does NOT reject a kernel that
// multiplies every result by 1.001. The reason is arithmetic and unavoidable:
// the gate divides by sum|a_i*b_i|, and on canceling data |result| is orders of
// magnitude below that sum, so a 0.1% error ON THE RESULT sits far below 1e-4 OF
// THE SCALE. No scale-relative tolerance can see it, because the quantity it
// measures against is not the quantity being perturbed.
//
// WHY IT IS ACCEPTED RATHER THAN FIXED, and what covers it instead:
//
//   - A literal relative tolerance is not an available alternative. It is
//     unmeetable by ANY correct float32 implementation on exactly this corpus,
//     which is why the scale-relative form was ratified. The log line below
//     records how unmeetable, in numbers, on every run.
//   - A UNIFORM multiplicative error preserves ranking EXACTLY, so the property
//     a search index actually depends on survives it — and the top-8 ranking
//     gate is the assertion that guards that property.
//   - A NON-UNIFORM error, the kind that really does reorder results, IS caught;
//     TestRankingGateRejectsReordering is the standing proof.
//   - A multiplicative error large enough to matter numerically IS caught on
//     non-canceling data, which is the corpus the scaled-by-1.001 control runs.
//
// If this test goes red because the gate STARTED rejecting the scaled kernel on
// canceling data, the tolerance has been tightened past what the arms can meet
// and the agreement suite is about to become flaky. Read the reasoning before
// touching scaleRelTol.
func TestScaleRelativeGateBlindSpot(t *testing.T) {
	scaled := func(a, b []float32) float32 { return dotF32Unroll4(a, b) * 1.001 }

	for _, dim := range []int{256, 1024, 2048} {
		x, y := seededPair(uint64(dim)*13+5, dim) // signed random: cancels heavily
		exact := dotF64Exact(x, y)
		scale := scaleOf(x, y)

		if err := gradeAgainstOracle("scaled", scaled, x, y); err != nil {
			t.Errorf("dim=%d: the gate rejected a uniform 1.001 scaling on canceling data. "+
				"That is a TIGHTENING of the ratified tolerance, not an improvement — re-read "+
				"the reasoning above before changing scaleRelTol. Gate said: %v", dim, err)
		}

		t.Logf("dim=%4d |exact|=%.4e scale=%.4e -> a uniform 1.001 scaling is %.2e literal-relative "+
			"but only %.2e scale-relative (gate threshold %.0e); ranking is unaffected by construction",
			dim, math.Abs(exact), scale, 0.001, 0.001*math.Abs(exact)/scale, scaleRelTol)
	}
}

// TestRankingGateRejectsReordering proves gradeRanking catches a top-8 swap.
//
// The wrong kernel perturbs each score by a row-dependent amount, which is what
// a real ranking defect looks like: numerically small, systematically ordered
// differently from the scores themselves.
func TestRankingGateRejectsReordering(t *testing.T) {
	const (
		rows = 2048
		dim  = 256
		k    = 8
	)
	block, query := seededBlock(777, rows, dim)
	ids := scatteredIDs(11, 1024, rows)

	want := make([]float32, len(ids))
	gatherUnroll4(want, query, block, dim, ids)

	got := make([]float32, len(ids))
	copy(got, want)
	for i, id := range ids {
		got[i] += 0.5 * block[int(id)*dim]
	}

	slack := 2 * scaleRelTol * scaleOf(query, block[:dim])

	err := gradeRanking("perturbed", got, want, ids, slack)
	if err == nil {
		t.Fatal("GATE IS BLIND: gradeRanking accepted a reordered top-8")
	}
	if strings.Contains(err.Error(), "CORPUS UNFIT") {
		t.Fatalf("control corpus is unfit rather than rejecting — the control proves nothing "+
			"in this state: %v", err)
	}
	t.Logf("gate fired as required: %v", err)

	// Negative control: an identical ranking must be ACCEPTED. Without this, a
	// gradeRanking that rejected everything would pass the assertion above.
	if err := gradeRanking("clean", want, want, ids, slack); err != nil {
		t.Fatalf("gradeRanking rejected an identical ranking: %v", err)
	}
}

// TestRankingSeparationGuardFires proves the CORPUS-UNFIT branch is reachable.
//
// That branch exists so a near-tie at the k/k+1 boundary is reported as an
// ungradeable corpus rather than as a pass or a flake. A guard nobody has ever
// seen fire is a guard that might be misspelled, or keyed on a comparison that
// can never be true.
func TestRankingSeparationGuardFires(t *testing.T) {
	ids := []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8}
	scores := []float32{9, 8, 7, 6, 5, 4, 3, 2.0000001, 2}

	err := gradeRanking("tied", scores, scores, ids, 1e-3)
	if err == nil || !strings.Contains(err.Error(), "CORPUS UNFIT") {
		t.Fatalf("separation guard did not fire on a k/k+1 gap of ~1e-7 against a 1e-3 slack; got %v", err)
	}
	t.Logf("separation guard fired as required: %v", err)

	// Negative control on the guard: a well-separated corpus must NOT be
	// reported unfit, or the guard would suppress every real ranking assertion.
	wide := []float32{9, 8, 7, 6, 5, 4, 3, 2, 1}
	if err := gradeRanking("separated", wide, wide, ids, 1e-3); err != nil {
		t.Fatalf("guard reported a well-separated corpus as unfit: %v", err)
	}
}
