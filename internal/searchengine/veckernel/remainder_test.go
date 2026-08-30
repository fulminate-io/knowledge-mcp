// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"
	"time"
)

// remainder_test.go is the DIAGNOSTIC INSTRUMENT for the id-remainder cliff, not
// a gate and not a fix.
//
// THE OBSERVATION IT EXISTS TO EXPLAIN. Scoring 63 candidates instead of 64
// costs +112% per distance on the AVX-512-capable class and +89% on its AVX2
// tier, while costing +2.9% on arm64 and -0.7% on AMD Milan. The portable
// reference — which has no four-row grouping and therefore no tail at all —
// moves less than half a percent on every class, which rules out the walk
// changing and points squarely at the tail path.
//
// THE DISCRIMINATING EXPERIMENT. The tail finishes its leftover rows through the
// SCALAR dot, one call per row. Three candidate explanations, and one A/B
// separates them:
//
//	A  current       15 fused groups + 3 scalar-dot calls
//	B  padded-fused  15 fused groups + 1 fused call scoring the 3 real rows
//	                 plus a repeat of the last one, the repeat discarded
//	C  no tail       15 fused groups, 60 ids, nothing left over
//
// If B lands near C, the scalar dot is the cost and the fix is a fused kernel for
// the short cases. If B lands near A, the cost is in the leftover ROWS — cold,
// scattered, and fetched without the overlap the fused kernel gets — and no
// amount of kernel shaping will help; the fix would have to change what the
// caller asks for. If B lands between them, both are real and the split says in
// what proportion.
//
// B COMPUTES THE SAME DISTANCES for the rows it reports. Padding repeats a row
// the caller already asked for, so the repeat computes a value that is simply
// thrown away; no real distance is approximated.
//
// It is graded scale-relative against the float64 oracle, NOT for bit-identity
// with the shipped gather. Routing a row through the fused kernel instead of the
// scalar dot changes the accumulator grouping, and the package is explicit that
// different groupings differ in the low bits — the first version of the control
// below demanded exact equality and failed on last-ULP differences that are
// correct in both kernels.

// gatherPaddedTail is variant B: the leftover rows go through the fused
// four-row kernel with the last real row repeated to fill the group.
func gatherPaddedTail(a arm) func(dst, query, block []float32, dim int, ids []uint32) {
	return func(dst, query, block []float32, dim int, ids []uint32) {
		full := len(ids) - len(ids)%4
		if full > 0 {
			a.gather(dst[:full], query, block, dim, ids[:full])
		}
		rest := ids[full:]
		if len(rest) == 0 {
			return
		}
		padded := make([]uint32, 4)
		copy(padded, rest)
		for i := len(rest); i < 4; i++ {
			padded[i] = rest[len(rest)-1]
		}
		var scratch [4]float32
		a.gather(scratch[:], query, block, dim, padded)
		copy(dst[full:], scratch[:len(rest)])
	}
}

// TestIDRemainderDiagnosis runs the A/B/C split and prints the attribution.
func TestIDRemainderDiagnosis(t *testing.T) {
	if !envEnabled(HarvestEnv) {
		t.Skipf("REMAINDER DIAGNOSIS NOT RUN. Set %s=1. Without it the id-remainder cliff has no "+
			"measured mechanism on this host — only a number.", HarvestEnv)
	}

	const dim = idRemainderDim
	c := buildTraverseCorpus(dim)
	t.Logf("REMAINDER DIAGNOSIS on %s/%s, class %q, dim %d, %d hops, best of %d",
		runtime.GOOS, runtime.GOARCH, machineClass(), dim, perfHops, measureRuns)

	for _, a := range testArms() {
		padded := arm{name: a.name + "/padded-tail", dot: a.dot, gather: gatherPaddedTail(a)}

		// A: the shipped path at 63 ids.
		curr := measureIDsNsPerDistance(a, c, idRemainderCount)
		// B: the same 63 ids with the tail fused instead of scalar.
		pad := measureIDsNsPerDistance(padded, c, idRemainderCount)
		// C: 60 ids — four full groups short of nothing, no tail at all.
		none := measureIDsNsPerDistance(a, c, 60)

		// NO CLIFF, NO MECHANISM TO ATTRIBUTE. On a host where 63 ids costs about
		// what 60 does there is nothing here to explain, and any verdict would be
		// a story fitted to noise. Say so instead.
		cliff := 100 * (curr.Min - none.Min) / none.Min
		verdict := "NO CLIFF ON THIS HOST — nothing to attribute"
		if cliff >= 25 {
			switch {
			case pad.Min <= none.Min*1.15:
				verdict = "SCALAR DOT IS THE COST — a fused 2/3-row kernel should remove nearly all of it"
			case pad.Min < curr.Min*0.85:
				verdict = "BOTH are real — a fused short kernel recovers part of it"
			default:
				verdict = "tail cost is in the ROWS, not the kernel — a fused short kernel will not help"
			}
		}

		t.Logf("REMDIAG %-14s A/current(63) %7.1f   B/padded-fused(63) %7.1f   C/no-tail(60) %7.1f   "+
			"cliff(A-vs-C) %+.1f%%   B-vs-C %+.1f%%   B-vs-A %+.1f%%   %s",
			a.name, curr.Min, pad.Min, none.Min, cliff,
			100*(pad.Min-none.Min)/none.Min, 100*(pad.Min-curr.Min)/curr.Min, verdict)
	}
}

// TestPaddedTailComputesTheRightDistances is the correctness control on B.
//
// A diagnosis run on a variant that computes the wrong answer measures nothing
// worth knowing, and padding a group by repeating a row is exactly the kind of
// trick that quietly writes the repeat into a caller's slot.
func TestPaddedTailComputesTheRightDistances(t *testing.T) {
	const rows = 128
	for _, a := range testArms() {
		padded := gatherPaddedTail(a)
		for _, dim := range []int{17, 256, 1024} {
			block, query := seededBlock(uint64(dim)*7, rows, dim)
			for _, n := range []int{1, 2, 3, 5, 6, 7, 63} {
				ids := scatteredIDs(uint64(n)+uint64(dim), n, rows)
				got := make([]float32, n)
				padded(got, query, block, dim, ids)
				for i, id := range ids {
					row := block[int(id)*dim : int(id)*dim+dim]
					label := fmt.Sprintf("padded-tail/%s dim=%d n=%d slot=%d", a.name, dim, n, i)
					if err := gradeScalarAgainstOracle(label, float64(got[i]), query, row); err != nil {
						t.Errorf("the diagnosis variant is not computing the right distance, so "+
							"its timings are meaningless: %v", err)
					}
				}
			}
		}
	}
}

// -- the walk control -------------------------------------------------------

// measureFixedItinerary prices a hop of exactly ids candidates while visiting a
// PRECOMPUTED sequence of nodes rather than following the argmax.
//
// THIS IS THE CONTROL THE A/B/C SPLIT DEMANDED. That split showed the tail
// KERNEL is irrelevant — replacing three scalar-dot calls with one fused call
// moved the number by 0.4% — while 63 ids still cost +84% per distance against
// 60. Two explanations survive that: the extra rows are genuinely expensive, or
// the id count changes the ARGMAX and therefore sends the walk somewhere with
// different locality.
//
// A fixed itinerary separates them by construction. Every id count visits the
// same nodes in the same order, so the memory access pattern is identical apart
// from the candidate rows themselves. If the cliff survives here it is real; if
// it vanishes, it was the walk all along and no kernel change can address it.
func measureFixedItinerary(a arm, c *traverseCorpus, ids, hops int) measurement {
	route := make([]uint32, hops)
	r := rand.New(rand.NewPCG(0xf13d, 0))
	for i := range route {
		route[i] = uint32(r.IntN(c.nodes))
	}
	scratch := make([]float32, mMax0)

	run := func() int {
		for _, cur := range route {
			row := c.neighbors[int(cur)*c.perNode : int(cur)*c.perNode+ids]
			query := c.block[int(cur)*c.dim : int(cur)*c.dim+c.dim]
			a.gather(scratch, query, c.block, c.dim, row)
		}
		return hops * ids
	}
	run()

	got := make([]float64, 0, measureRuns)
	for range measureRuns {
		start := time.Now()
		n := run()
		got = append(got, float64(time.Since(start).Nanoseconds())/float64(n))
	}
	return summarize(got)
}

// TestIDRemainderIsAWalkArtifact is the decisive experiment.
func TestIDRemainderIsAWalkArtifact(t *testing.T) {
	if !envEnabled(HarvestEnv) {
		t.Skipf("WALK CONTROL NOT RUN. Set %s=1.", HarvestEnv)
	}

	const dim = idRemainderDim
	c := buildTraverseCorpus(dim)
	t.Logf("WALK CONTROL on %s/%s, class %q, dim %d — every id count visits the SAME %d nodes in "+
		"the same order, so the argmax cannot move the walk",
		runtime.GOOS, runtime.GOARCH, machineClass(), dim, perfHops)

	for _, a := range testArms() {
		at60 := measureFixedItinerary(a, c, 60, perfHops)
		at63 := measureFixedItinerary(a, c, idRemainderCount, perfHops)
		at64 := measureFixedItinerary(a, c, mMax0, perfHops)

		d63 := 100 * (at63.Min - at60.Min) / at60.Min
		d64 := 100 * (at64.Min - at60.Min) / at60.Min

		verdict := "CLIFF SURVIVES a fixed itinerary — the tail rows really are expensive"
		if d63 < 15 {
			verdict = "CLIFF VANISHES on a fixed itinerary — it was the WALK, not the tail"
		}
		t.Logf("WALKCTL %-14s 60ids %7.1f   63ids %7.1f (%+.1f%%)   64ids %7.1f (%+.1f%%)   %s",
			a.name, at60.Min, at63.Min, d63, at64.Min, d64, verdict)
	}
}
