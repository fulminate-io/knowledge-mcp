// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math/rand/v2"
	"runtime"
	"testing"
	"time"
)

// wired_pins_test.go prices the WIRED CALLER's shape, not the kernel's.
//
// See wiredTraversePins for why that is a different question from pinTable's.
// The short form: the production traversal filters each neighbor row through a
// visited set before scoring, so the run the gather finally receives is a
// VARIABLE length hitting every residue mod four, and the filter itself is Go
// work the kernel-shaped benchmark never pays.

// wiredRunLengths is the deterministic cycle of collected-run lengths this
// simulation walks.
//
// DETERMINISTIC RATHER THAN RANDOM, because a floor must be reproducible: a
// randomly varying run length would make the pinned number depend on a seed and
// turn a regression into a coin flip. The cycle covers every residue mod four —
// the fused gather scores four rows at a time and finishes the rest one at a
// time, so a shape that only ever fed it multiples of four would leave the tail
// path timed by nothing, which is exactly the hole idRemainderCount exists to
// point at.
var wiredRunLengths = []int{mMax0, mMax0 - 1, mMax0 - 2, mMax0 - 3, mMax0 / 2, mMax0/2 - 1}

// wiredRoute builds the FIXED ITINERARY this cell walks: perfHops nodes drawn
// uniformly at random from the corpus under a fixed seed.
//
// A FIXED ITINERARY RATHER THAN AN ARGMAX WALK, and the reason is measured
// rather than stylistic. An argmax walk's next hop depends on which candidate
// won, so varying the scored run length — which is the whole point of this cell
// — ALSO changes where the walk goes, and part of any difference becomes the new
// route's locality rather than the wiring's cost. remainder_test.go established
// exactly this: a cliff that looked like a per-row tail cost collapsed to +0.2%
// once the itinerary was held fixed, because it had been the walk all along.
//
// A FIRST DRAFT OF THIS CELL USED AN ARGMAX WALK AND PRODUCED AN INCOHERENT
// TABLE — dim 2048 measured CHEAPER per distance than dim 1024, which cannot be
// true of a wider vector against the same memory system. The varying run length
// was steering the walk into a small revisited cycle that fit in cache, so the
// number described the cache rather than the wiring. Those rows were discarded
// rather than pinned. A uniform random route cannot collapse that way: it
// touches perfHops independent nodes across a corpus deliberately larger than
// this host's cache.
func wiredRoute(c *traverseCorpus, hops int) []uint32 {
	route := make([]uint32, hops)
	r := rand.New(rand.NewPCG(0x1499, 0))
	for i := range route {
		route[i] = uint32(r.IntN(c.nodes))
	}
	return route
}

// runWiredTraverseSim walks the fixed itinerary in the production caller's
// shape: COLLECT the run into scratch (the pass that filters visited ids), then
// score the whole collected run in ONE gather call.
//
// READ THIS AS A MODEL OF THE WIRING, NOT AS FIDELITY TO A QUERY, the same
// caveat runTraverseSim carries. It reproduces the two properties that separate
// the wired caller from the kernel benchmark — a per-id collection pass, and a
// run length that lands on every residue mod four instead of always being a
// multiple of it — and deliberately not the heaps, the beam or the revisit
// behavior.
//
// Returns the number of distances computed, which is what the caller divides by.
func runWiredTraverseSim(a arm, c *traverseCorpus, route []uint32, scratch []float32, ids []uint32) int {
	total := 0
	for h, cur := range route {
		want := wiredRunLengths[h%len(wiredRunLengths)]
		row := c.neighbors[int(cur)*c.perNode : (int(cur)+1)*c.perNode]

		// PASS 1 — the collection the wired caller pays before any scoring. It
		// walks the row and keeps a subset, which is the shape of the visited
		// filter even though this model keeps a fixed count rather than
		// consulting a real visited set.
		collected := ids[:0]
		for i := 0; i < len(row) && len(collected) < want; i++ {
			collected = append(collected, row[i])
		}

		// PASS 2 — one gather for the whole collected run.
		query := c.block[int(cur)*c.dim : int(cur)*c.dim+c.dim]
		a.gather(scratch[:len(collected)], query, c.block, c.dim, collected)
		total += len(collected)
	}
	return total
}

// measureWiredTraverseNsPerDistance is the ONE measurement function both the
// harvester and the gate call, so a pinned number and a checked number can never
// come from two protocols that drifted apart.
func measureWiredTraverseNsPerDistance(a arm, dim int) measurement {
	c := buildTraverseCorpus(dim)
	scratch := make([]float32, mMax0)
	ids := make([]uint32, mMax0)
	route := wiredRoute(c, perfHops)

	// Touch the corpus once so first-touch page faults are not in a timed run.
	runWiredTraverseSim(a, c, route[:200], scratch, ids)

	got := make([]float64, 0, measureRuns)
	for range measureRuns {
		start := time.Now()
		n := runWiredTraverseSim(a, c, route, scratch, ids)
		elapsed := time.Since(start)
		got = append(got, float64(elapsed.Nanoseconds())/float64(n))
	}
	return summarize(got)
}

// TestWiredTraversePinFloors is the two-sided floor gate for the wired cell.
//
// IT RUNS IN TWO MODES, AND THE DIFFERENCE IS DISCLOSED RATHER THAN SILENT.
// Without PerfEnv it checks the TABLE — that every row this host's class needs
// exists, is keyed to this class, and carries a derived Tolerance — and reports
// that no timing was taken. With PerfEnv set it additionally measures each cell
// and applies the floor. A reader of a passing run can tell which happened from
// the log lines; neither mode reports a floor it did not check.
//
// The timing half is env-gated for the reason PerfEnv exists at all: these are
// multi-second measurements over a corpus deliberately larger than this host's
// cache, and running them on every `go test` would make an unrelated commit's
// suite depend on machine load.
func TestWiredTraversePinFloors(t *testing.T) {
	class := machineClass()
	t.Logf("machine class resolved to %q on %s/%s", class, runtime.GOOS, runtime.GOARCH)

	if !wiredClassHasPins(class) {
		// A MEASURING run on unharvested hardware is a hard error: the caller asked
		// for floors that do not exist, and borrowing another class's numbers is
		// exactly what this table refuses to do. WITHOUT PerfEnv there is nothing
		// to check — a class with zero rows has no table shape — so the outcome is
		// a DISCLOSED skip carrying the harvest instruction, not a red. The red
		// form made every hosted CI runner whose microarchitecture nobody can
		// harvest (ephemeral, load-noisy, class-heterogeneous across allocations —
		// observed: "amd64-no-avx512") fail the whole client suite for hardware
		// coverage no CI machine can cure.
		if envEnabled(PerfEnv) {
			t.Errorf("NO WIRED-TRAVERSE PINS AT ALL FOR MACHINE CLASS %q — this host's class has "+
				"never been harvested for the wired cell, and no other class's numbers are "+
				"borrowed for it. Run TestWiredTraverseHarvestPins on this hardware and paste "+
				"the rows into wiredTraversePins.", class)
			return
		}
		t.Skipf("machine class %q has no harvested wired-traverse pins; nothing to shape-check. "+
			"To gate this hardware, run TestWiredTraverseHarvestPins on it and paste the rows "+
			"into wiredTraversePins.", class)
	}

	timing := envEnabled(PerfEnv)
	if !timing {
		t.Logf("TIMING NOT TAKEN. The wired floors were NOT checked in this run; only the "+
			"table's shape was. Set %s=1 to measure and gate them.", PerfEnv)
	}

	checked, structural := 0, 0
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			p, ok := wiredPinFor(class, a.name, dim)
			if !ok {
				t.Errorf("NO WIRED PIN for class %s/%s/dim=%d — a supported tier with no floor "+
					"is an unguarded tier; add a row to wiredTraversePins", class, a.name, dim)
				continue
			}
			if p.Unmeasured || p.TraverseNsPerDistance == 0 {
				t.Errorf("WIRED PIN UNMEASURED for %s/%s/dim=%d — a row that exists but carries "+
					"no measurement guards nothing. Fill it from a harvest on this hardware.",
					class, a.name, dim)
				continue
			}
			if p.Tolerance <= 0 {
				t.Errorf("WIRED PIN %s/%s/dim=%d carries no Tolerance. Every measured row must "+
					"derive one from its harvest spread via toleranceFor; a row without it "+
					"cannot be gated and this cell is unguarded.", class, a.name, dim)
				continue
			}
			if p.Machine == "" {
				t.Errorf("WIRED PIN %s/%s/dim=%d names no Machine — a pin without the part it "+
					"was measured on is a rumor", class, a.name, dim)
				continue
			}
			structural++

			if !timing {
				continue
			}

			got := measureWiredTraverseNsPerDistance(a, dim)
			limit := p.TraverseNsPerDistance * p.Tolerance
			stale := p.TraverseNsPerDistance * staleFloorFactor
			checked++

			switch {
			case got.Min > limit:
				// THE FAILURE TEXT NAMES ONLY WHAT THIS CELL CAN ACTUALLY OBSERVE.
				// An earlier version claimed it would catch "a collection pass that
				// started allocating per hop, or a run split back into per-id
				// calls" — neither of which this simulation can exhibit, because it
				// has no production collection pass and issues its own gather
				// calls. Measured: an 8-to-209 allocations-per-search regression
				// leaves this gate entirely GREEN. Allocation is pinned separately
				// by TestSearchAllocationsAreBounded in formats/hnsw, and the
				// per-group inlining that this cell IS sensitive to has its own
				// deterministic gate in TestPrefetchFastPathStaysInlinable.
				t.Errorf("WIRED REGRESSION %s/dim=%d: %.1f ns/distance exceeds %.1f (pin %.1f "+
					"from %s, x%.2f allowed, derived from this cell's %.2fx harvest spread). This "+
					"cell prices the KERNEL AND ITS CALL SHAPE under a fixed itinerary — a slower "+
					"gather, a lost prefetch, or a dispatch that stopped selecting the assembly "+
					"tier lands here. Run spread: min %.1f median %.1f max %.1f.",
					a.name, dim, got.Min, limit, p.TraverseNsPerDistance, p.Machine, p.Tolerance,
					p.SpreadRatio, got.Min, got.Median, got.Max)
			case got.Min < stale:
				t.Errorf("WIRED STALE FLOOR %s/dim=%d: measured %.1f ns/distance, which is below "+
					"%.2fx the %.1f pin. Faster than the floor by this much is not good news — "+
					"it is evidence the floor no longer describes this wiring on this machine, "+
					"and a one-sided gate would have called it a clean pass. Re-harvest.",
					a.name, dim, got.Min, staleFloorFactor, p.TraverseNsPerDistance)
			default:
				t.Logf("WIRED OK %s/dim=%d: %.1f ns/distance against pin %.1f (x%.2f of pin, "+
					"limit %.1f)", a.name, dim, got.Min, p.TraverseNsPerDistance,
					got.Min/p.TraverseNsPerDistance, limit)
			}
		}
	}

	t.Logf("wired cells: %d structurally verified, %d timed and gated", structural, checked)
	if structural == 0 {
		t.Errorf("NOTHING WAS CHECKED for class %q — every slot was missing or unmeasured, so a "+
			"pass here would mean only that the loop ran", class)
	}
}

// TestWiredTraverseHarvestPins prints wiredTraversePins rows for this host.
//
// It is the ONLY sanctioned source of those rows: it calls the same measurement
// function the gate calls, so a pinned number and a checked number share one
// code path rather than being two protocols that drift. Rows are pasted into
// pins.go by hand, which keeps the table a reviewed artifact rather than a
// generated one.
func TestWiredTraverseHarvestPins(t *testing.T) {
	requirePerfEnabled(t)

	class := machineClass()
	t.Logf("harvesting wired-traverse rows for class %q on %s/%s", class, runtime.GOOS, runtime.GOARCH)

	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			m := measureWiredTraverseNsPerDistance(a, dim)
			spread := m.SpreadRatio()
			t.Logf("WIREDPIN {Class: %s, Tier: %s, Dim: %d, TraverseNsPerDistance: %.1f, "+
				"SpreadRatio: %.2f, Tolerance: %.2f, Machine: <fill>},  // min %.1f median %.1f max %.1f",
				class, a.name, dim, m.Min, spread, toleranceFor(spread), m.Min, m.Median, m.Max)
		}
	}
}
