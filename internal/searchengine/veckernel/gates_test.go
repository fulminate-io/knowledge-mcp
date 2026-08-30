// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

// gates_test.go holds the graders and the corpus generators every other test
// file drives. The graders are written as FUNCTIONS RETURNING AN ERROR rather
// than as t.Fatal calls for one reason: a grader that is only ever invoked on
// correct input is indistinguishable from a grader wired to nothing. Returning
// an error lets controls_test.go run each grader against deliberately WRONG
// kernels in the same suite and assert it rejects them.

// dotFunc is one implementation under grading.
type dotFunc func(a, b []float32) float32

// testArms returns every tier this build offers AND this CPU can execute.
//
// It rebuilds the table from asmArms rather than reading the package-level
// `tiers` so the graders keep working while a dispatch test has pinned `active`
// to something else.
//
// UNSUPPORTED TIERS ARE EXCLUDED HERE AND REPORTED ELSEWHERE. A tier this
// silicon cannot execute must not be silently absent from the census — that is
// what makes a whole tier ungraded on every machine while the suite stays
// green — so TestTierCensus in dispatch_test.go names every compiled tier and
// logs a loud skip reason for each one this host cannot run.
func testArms() []arm {
	var out []arm
	for _, t := range append(asmArms(), arm{
		name:      TierReference,
		dot:       dotF32Unroll4,
		gather:    gatherUnroll4,
		supported: true,
	}) {
		if t.supported {
			out = append(out, t)
		}
	}
	return out
}

// scaleRelTol is the ratified agreement tolerance: error measured against the
// magnitude the accumulator traversed, not against the result.
//
// A LITERAL RELATIVE TOLERANCE IS UNMEETABLE HERE and that is arithmetic, not
// pessimism. For two random vectors the dot cancels almost completely — the
// result sits near zero while the running sum visits values of order
// sum|a_i*b_i| — so |got-want|/|want| is unbounded for ANY correct float32
// implementation. The standard error bound for a floating-point dot is
// |computed - exact| <= gamma_n * sum|a_i*b_i| with gamma_n ~ (n/k)*eps for k
// independent accumulators. At the widest production dim (2048), k is 4 for the
// reference, 16 for the arm64 tier (4 accumulators x 4 lanes), 32 for AVX2
// (4 x 8) and 64 for AVX-512 (4 x 16), giving bounds of roughly 3e-5, 8e-6,
// 4e-6 and 2e-6. THE TOLERANCE IS SET BY THE LOOSEST TIER, which is the
// reference and always will be: every assembly tier splits the sum across more
// lanes and therefore lands strictly tighter. 1e-4 clears the reference with
// roughly 3x headroom and still rejects a dropped tail element (~1/n ~ 5e-4 of
// scale) by a wide margin, which controls_test.go proves rather than assumes.
const scaleRelTol = 1e-4

// rankingK is the depth of the ranking agreement gate: the top EIGHT results
// must come back in the same order from every tier.
//
// Eight because that is the neighborhood a caller actually reads. Grading the
// full candidate list would fail on ties far down the tail that no consumer ever
// sees, and grading only the top one would miss the reorderings that change what
// a result page looks like.
const rankingK = 8

// gradeAgainstOracle checks one kernel against the float64 serial oracle.
//
// The oracle is the EXTERNAL expectation. Grading the assembly arm only against
// the Go reference would be an identity check in disguise: both are float32
// dots written by the same hand, and a shared misreading of the operation would
// agree perfectly and pass. float64 accumulation of float32 inputs carries 29
// spare mantissa bits, so it grades from outside the family under test.
func gradeAgainstOracle(name string, dot dotFunc, a, b []float32) error {
	return gradeScalarAgainstOracle(name, float64(dot(a, b)), a, b)
}

// gradeScalarAgainstOracle is the same grade applied to an ALREADY-COMPUTED
// result, which is what the gather tests need: the value under grading came out
// of a batch call and there is no per-row dotFunc to re-invoke.
func gradeScalarAgainstOracle(name string, got float64, a, b []float32) error {
	const tol = scaleRelTol
	want := dotF64Exact(a, b)
	scale := scaleOf(a, b)
	if scale == 0 {
		if got != want {
			return fmt.Errorf("%s: zero-scale input: got %v, want exactly %v", name, got, want)
		}
		return nil
	}
	rel := math.Abs(got-want) / scale
	if !(rel <= tol) { // NOT (<=) so a NaN rel is a failure rather than a pass.
		return fmt.Errorf("%s: dim=%d scale-relative error %.3e exceeds %.3e (got %v, oracle %v, scale %v)",
			name, len(a), rel, tol, got, want, scale)
	}
	return nil
}

// gradeArmsAgree checks two kernels against each other, scale-relative.
func gradeArmsAgree(nameA string, dotA dotFunc, nameB string, dotB dotFunc, a, b []float32) error {
	const tol = scaleRelTol
	ga, gb := float64(dotA(a, b)), float64(dotB(a, b))
	scale := scaleOf(a, b)
	if scale == 0 {
		if ga != gb {
			return fmt.Errorf("%s vs %s: zero-scale input disagrees: %v vs %v", nameA, nameB, ga, gb)
		}
		return nil
	}
	rel := math.Abs(ga-gb) / scale
	if !(rel <= tol) {
		return fmt.Errorf("%s vs %s: dim=%d scale-relative divergence %.3e exceeds %.3e (%v vs %v)",
			nameA, nameB, len(a), rel, tol, ga, gb)
	}
	return nil
}

// rankedID is one scored candidate.
type rankedID struct {
	id    uint32
	score float64
}

// topK ranks ids by descending score, breaking ties by ascending id so the
// order is total and a tie cannot masquerade as a ranking disagreement.
func topK(ids []uint32, scores []float32, k int) []rankedID {
	all := make([]rankedID, len(ids))
	for i := range ids {
		all[i] = rankedID{id: ids[i], score: float64(scores[i])}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].id < all[j].id
	})
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
}

// gradeRanking checks that a kernel's top-k ORDER matches the reference's.
//
// Ranking agreement is the gate that matters to a search index, and it is not
// implied by numeric agreement: two kernels can agree to 1e-4 of scale on every
// candidate and still swap two neighbors whose scores differ by less than that.
//
// SEPARATION GUARD. Because that swap can be legitimate, this grader first
// checks the reference's own k-th and (k+1)-th scores are further apart than the
// numeric slack. If they are not, the CORPUS is unfit to grade ranking on and
// the grader says so rather than reporting a flaky pass or a flaky failure.
func gradeRanking(name string, got, want []float32, ids []uint32, slack float64) error {
	const k = rankingK

	wantTop := topK(ids, want, k+1)
	if len(wantTop) > k {
		gap := wantTop[k-1].score - wantTop[k].score
		if gap <= slack {
			return fmt.Errorf("CORPUS UNFIT: reference scores at rank %d and %d differ by %.3e, "+
				"within the %.3e numeric slack — a ranking swap here would be legitimate, "+
				"so pick a different seed rather than grading on this one", k, k+1, gap, slack)
		}
	}

	gotTop, refTop := topK(ids, got, k), wantTop
	if len(refTop) > k {
		refTop = refTop[:k]
	}
	for i := range refTop {
		if gotTop[i].id != refTop[i].id {
			return fmt.Errorf("%s: top-%d ranking diverges at position %d: got id %d (score %v), "+
				"reference id %d (score %v)", name, k, i, gotTop[i].id, gotTop[i].score,
				refTop[i].id, refTop[i].score)
		}
	}
	return nil
}

// -- corpora -----------------------------------------------------------------

// seededPair returns a deterministic (a, b) pair of the given dim, values in
// [-1, 1). Deterministic per (seed, dim) so a failure is reproducible from the
// test name alone.
func seededPair(seed uint64, dim int) ([]float32, []float32) {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	a := make([]float32, dim)
	b := make([]float32, dim)
	for i := range dim {
		a[i] = r.Float32()*2 - 1
		b[i] = r.Float32()*2 - 1
	}
	return a, b
}

// seededBlock returns a flat rows*dim corpus and a query, values in [-1, 1).
func seededBlock(seed uint64, rows, dim int) (block, query []float32) {
	r := rand.New(rand.NewPCG(seed, seed^0xda3e39cb94b95bdb))
	block = make([]float32, rows*dim)
	for i := range block {
		block[i] = r.Float32()*2 - 1
	}
	query = make([]float32, dim)
	for i := range query {
		query[i] = r.Float32()*2 - 1
	}
	return block, query
}

// scatteredIDs returns n distinct row ids in a deliberately non-monotonic
// order, because HNSW neighbor runs are scattered and a gather that only ever
// sees ascending ids is not being tested on the shape it will meet.
func scatteredIDs(seed uint64, n, rows int) []uint32 {
	r := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
	perm := r.Perm(rows)
	if n > rows {
		n = rows
	}
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		out[i] = uint32(perm[i])
	}
	return out
}
