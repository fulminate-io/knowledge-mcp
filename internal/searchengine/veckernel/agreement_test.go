// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"testing"
)

// agreement_test.go is TEST CLASS (a): every arm agrees with the float64 oracle
// and with every other arm, scale-relative, and ranks candidates in the same
// order.
//
// Every test here NAMES THE ARM IT GRADED. A suite that grades "whatever
// dispatch picked" reports success identically whether the assembly ran or
// quietly did not, which is the failure this package is built against.

// prodDims are the production widths the float-native index is specified at,
// plus 3072 as a width above every one of them.
var prodDims = []int{256, 512, 768, 1024, 1536, 2048, 3072}

func TestArmsAgreeWithOracle(t *testing.T) {
	arms := testArms()
	if len(arms) == 0 {
		t.Fatal("no arms compiled in")
	}
	t.Logf("grading %d arm(s): %v", len(arms), armNamesOf(arms))

	for _, a := range arms {
		for _, dim := range prodDims {
			for seed := uint64(1); seed <= 8; seed++ {
				x, y := seededPair(seed*1000+uint64(dim), dim)
				if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
					t.Errorf("seed=%d: %v", seed, err)
				}
			}
		}
	}
}

func TestArmsAgreeWithReference(t *testing.T) {
	arms := testArms()
	ref := arms[len(arms)-1]
	if ref.name != TierReference {
		t.Fatalf("tier table is malformed: last tier is %q, expected %q", ref.name, TierReference)
	}
	if len(arms) == 1 {
		t.Skipf("only the reference arm is compiled in on %s — nothing to cross-grade", Kernel())
	}

	for _, a := range arms[:len(arms)-1] {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range prodDims {
				for seed := uint64(1); seed <= 8; seed++ {
					x, y := seededPair(seed*7919+uint64(dim), dim)
					if err := gradeArmsAgree(a.name, a.dot, ref.name, ref.dot, x, y); err != nil {
						t.Errorf("seed=%d: %v", seed, err)
					}
				}
			}
		})
	}
}

// TestGatherAgreesWithReference grades the id-list batch path, which on the
// assembly arm is a DIFFERENT KERNEL from the scalar dot — four rows fused, its
// own tail handling, its own accumulator fold. Grading only the scalar entry
// point would leave the kernel that actually runs in a traversal ungraded.
func TestGatherAgreesWithReference(t *testing.T) {
	const rows = 512
	arms := testArms()

	for _, dim := range []int{256, 1024, 2048} {
		block, query := seededBlock(uint64(dim)*31, rows, dim)

		// Sweep id-list lengths across every residue mod 4, because the
		// assembly gather processes ids four at a time and finishes the
		// remainder singly — lengths 0..3 past a group boundary are where a
		// grouping bug hides.
		for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 15, 16, 17, 63, 64, 65} {
			ids := scatteredIDs(uint64(n)*17+uint64(dim), n, rows)

			for _, a := range arms {
				got := make([]float32, len(ids))
				if len(ids) > 0 {
					a.gather(got, query, block, dim, ids)
				}
				for i, id := range ids {
					row := block[int(id)*dim : int(id)*dim+dim]
					label := fmt.Sprintf("%s gather dim=%d n=%d slot=%d id=%d", a.name, dim, n, i, id)
					if err := gradeScalarAgainstOracle(label, float64(got[i]), query, row); err != nil {
						t.Error(err)
					}
				}
			}
		}
	}
}

// TestTopEightRankingAgreement is the ranking half of class (a): numeric
// agreement does not imply ranking agreement, and ranking is what a search
// index actually ships.
func TestTopEightRankingAgreement(t *testing.T) {
	const (
		rows = 2048
		k    = 8
	)
	arms := testArms()

	for _, dim := range []int{256, 1024, 2048} {
		block, query := seededBlock(uint64(dim)*101+7, rows, dim)
		ids := scatteredIDs(uint64(dim)+3, 1024, rows)

		want := make([]float32, len(ids))
		gatherUnroll4(want, query, block, dim, ids)

		// The slack a ranking swap must beat to count as a real disagreement:
		// two scores that both carry up to scaleRelTol*scale of error can
		// legitimately cross if they are closer together than twice that.
		slack := 2 * scaleRelTol * scaleOf(query, block[:dim])

		for _, a := range arms {
			got := make([]float32, len(ids))
			a.gather(got, query, block, dim, ids)
			if err := gradeRanking(fmt.Sprintf("%s dim=%d", a.name, dim), got, want, ids, slack); err != nil {
				t.Error(err)
			}
		}
	}
}

func armNamesOf(arms []arm) []string {
	out := make([]string, len(arms))
	for i := range arms {
		out[i] = arms[i].name
	}
	return out
}
