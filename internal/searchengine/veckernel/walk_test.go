// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"runtime"
	"testing"
)

// walk_test.go asserts the END-TO-END property the agreement gates only imply:
// that every tier, walking the same corpus, arrives at the same node.
//
// WHY THIS IS NOT REDUNDANT WITH THE AGREEMENT SUITE. Those gates compare
// SCORES, scale-relative, one hop at a time. A traversal compounds: the next hop
// is the argmax of the scores just computed, so a difference far below the
// agreement tolerance can flip one comparison, send two tiers down different
// branches, and leave them in unrelated regions of the graph after a few hundred
// hops. Numeric agreement does not imply itinerary agreement, and the itinerary
// is what a search actually returns.
//
// IT IS ALSO THE GATE ON SOFTWARE PREFETCH. PRFM and PREFETCHT0 are hints that
// cannot architecturally change a result — but "cannot" is what everyone says
// about the change that breaks something, and the prefetch pointers are computed
// in Go from the id list, where an off-by-one WOULD be real. A pointer bug there
// cannot corrupt a score (the hint only warms cache) but this test is where the
// end-to-end claim is recorded rather than assumed.

// walkHops is long enough for a divergence to become obvious and short enough to
// stay cheap in the ordinary suite.
const walkHops = 2000

// terminalNode walks the traverse simulation and returns where it ends up.
func terminalNode(a arm, c *traverseCorpus) uint32 {
	scratch := make([]float32, mMax0)
	cur := uint32(0)
	for range walkHops {
		row := c.neighbors[int(cur)*c.perNode : int(cur)*c.perNode+mMax0]
		query := c.block[int(cur)*c.dim : int(cur)*c.dim+c.dim]
		a.gather(scratch, query, c.block, c.dim, row)
		best, bestScore := row[0], scratch[0]
		for i := 1; i < mMax0; i++ {
			if scratch[i] > bestScore {
				best, bestScore = row[i], scratch[i]
			}
		}
		cur = best
	}
	return cur
}

// TestAllTiersWalkToTheSameTerminalNode is the itinerary gate.
func TestAllTiersWalkToTheSameTerminalNode(t *testing.T) {
	arms := testArms()
	if len(arms) < 2 {
		t.Skipf("only %d tier supported on this host (%s/%s) — there is no second itinerary to "+
			"compare against, so this gate is UNPROVEN here rather than satisfied",
			len(arms), runtime.GOOS, runtime.GOARCH)
	}

	for _, dim := range []int{256, 1024} {
		c := requireTraverseCorpus(t, dim)

		var refEnd uint32
		var refName string
		for _, a := range arms {
			if a.name == TierReference {
				refEnd, refName = terminalNode(a, c), a.name
			}
		}
		if refName == "" {
			t.Fatal("reference tier absent from the grader table")
		}

		for _, a := range arms {
			got := terminalNode(a, c)
			if got != refEnd {
				t.Errorf("dim=%d: tier %s ends its %d-hop walk at node %d, the %s tier at %d. "+
					"The tiers agree numerically but not on the itinerary — a score difference "+
					"below the agreement tolerance flipped an argmax and the walks separated.",
					dim, a.name, walkHops, got, refName, refEnd)
				continue
			}
			t.Logf("dim=%-5d %-14s terminal node %d after %d hops (matches %s)",
				dim, a.name, got, walkHops, refName)
		}
	}
}

// TestWalkIsReproducible is the known positive for the gate above.
//
// Without it, a terminalNode that returned a constant — a mis-sliced neighbor
// row, a loop that never advanced — would make every tier "agree" perfectly and
// the assertion above would pass having compared nothing. This drives the same
// walk twice and separately requires that DIFFERENT corpora reach DIFFERENT
// nodes, which a constant cannot do.
func TestWalkIsReproducible(t *testing.T) {
	a := testArms()[0]

	c256 := requireTraverseCorpus(t, 256)
	first := terminalNode(a, c256)
	second := terminalNode(a, c256)
	if first != second {
		t.Errorf("the same tier on the same corpus reached %d then %d — the walk is not "+
			"deterministic, so the cross-tier comparison cannot mean anything", first, second)
	}

	c1024 := requireTraverseCorpus(t, 1024)
	other := terminalNode(a, c1024)
	if other == first {
		t.Errorf("two different corpora both terminated at node %d. That is possible by "+
			"coincidence but overwhelmingly likely to mean terminalNode is returning a constant, "+
			"which would make TestAllTiersWalkToTheSameTerminalNode vacuous", first)
	}
	t.Logf("walk is deterministic (node %d twice) and corpus-sensitive (node %d at dim 1024)",
		first, other)
}
