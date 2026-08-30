// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"runtime"
	"testing"
	"time"
)

// harvest_test.go is the INSTRUMENT that produces the numbers pinTable is filled
// from. It is not a gate and asserts nothing about speed.
//
// WHY IT EXISTS RATHER THAN A BENCHMARK RUN. A floor is only a floor if the
// measurement it is compared against used the same protocol, and the floor gate
// pins its own protocol deliberately — a fixed hop count, a fixed run count, and
// a corpus sized against this host's cache — instead of letting the benchmark
// framework choose, because a random walk over the corpus gets cheaper the
// longer it runs. Harvesting the pins from
// `go test -bench` would set them from a DIFFERENT protocol than the one that
// later checks them, and the gap would show up as a permanent, unexplained
// offset that whoever inherits it would resolve by loosening the gate.
//
// It therefore calls measureTraverseNsPerDistance — the very function
// TestPerfFloorsTraverse calls — so the pinned number and the checked number are
// produced by one code path.
//
// ENV-GATED SEPARATELY from the floor gate, and the skip names what goes
// unmeasured. Running it is a deliberate act on hardware whose numbers are about
// to be written down, not something a routine suite should spend minutes on.

// HarvestEnv gates the pin harvester. Set it to 1 to run.
const HarvestEnv = "VECKERNEL_PERF_HARVEST"

// microIters is the FIXED iteration count for the cache-hot micro measurement.
//
// Fixed for the same reason perfHops is: a number written into pinTable must
// come from a stated protocol. The micro shape is cache-resident so the count
// barely moves the result, but "barely" is not "documented", and the next person
// to re-harvest needs to reproduce this run rather than approximate it.
const microIters = 200_000

// TestPerfHarvestPins measures every supported tier at every pinned dim, in both
// benchmark shapes, and prints rows ready to paste into pinTable.
//
// WHAT IT ASSERTS, since an instrument that asserts nothing can be silently
// wired to nothing: that it produced exactly one finite, positive measurement
// per (supported tier, pinned dim) pair. The expected count comes from the tier
// table and the dim list — an external fixture — not from counting what the loop
// happened to emit, so a harvester that skipped half its work fails here instead
// of printing a short table nobody counts.
func TestPerfHarvestPins(t *testing.T) {
	if !envEnabled(HarvestEnv) {
		t.Skipf("PINS NOT HARVESTED. Set %s=1 to measure this machine. Without it no numbers "+
			"are produced for pinTable, and the amd64 or arm64 rows for this host stay whatever "+
			"they already were.", HarvestEnv)
	}

	arms := testArms()
	want := len(arms) * len(pinnedDims)
	got := 0

	class := machineClass()
	if class == "" {
		t.Fatalf("machineClass() resolved to \"\" on %s/%s: this architecture has no class, so "+
			"harvested rows would key to nothing and could never be read back. Add a class and a "+
			"resolution rule before harvesting here.", runtime.GOOS, runtime.GOARCH)
	}

	size, err := corpusBytes()
	if err != nil {
		t.Fatalf("%v", err)
	}
	cache, _ := largestCacheBytes()
	t.Logf("HARVEST on %s/%s, machine class %q — protocol: traverse = %d hops, %d runs (min is "+
		"pinned, max/min recorded as the spread) over a "+
		"%d MiB corpus (%.1fx this host's %d MiB largest reported cache) at %d candidates per "+
		"hop; micro = %d cache-hot iterations best of 3",
		runtime.GOOS, runtime.GOARCH, class, perfHops, measureRuns, size>>20,
		float64(size)/float64(cache), cache>>20, mMax0, microIters)

	for _, a := range arms {
		for _, dim := range pinnedDims {
			traverse := measureTraverseNsPerDistance(a, dim)
			micro := measureMicroNsPerDistance(a, dim)

			if !isFinitePositive(traverse.Min) || !isFinitePositive(micro) {
				t.Errorf("%s/dim=%d produced a non-measurement: traverse=%+v micro=%v",
					a.name, dim, traverse, micro)
				continue
			}
			got++

			spread := traverse.SpreadRatio()
			tol := toleranceFor(spread)

			// The spread is printed BESIDE the pinned value, not just folded into
			// the tolerance, because the tolerance is a derived number and the
			// spread is the observation it came from. A future reader adjusting
			// toleranceFor needs the observation.
			t.Logf("SPREAD\t%-14s dim=%-5d min %7.1f  median %7.1f  max %7.1f  spread %.2fx  -> tolerance %.2fx",
				a.name, dim, traverse.Min, traverse.Median, traverse.Max, spread, tol)

			// Printed as the literal pinTable row so transcription is a copy
			// rather than a retyping, and a transposed digit has one fewer place
			// to enter.
			t.Logf("PIN\t{Class: %s, Tier: %s, Dim: %d, TraverseNsPerDistance: %.1f, SpreadRatio: %.2f, Tolerance: %.2f, MicroNsPerDistance: %.1f, Machine: %q},",
				classConstName(class), tierConstName(a.name), dim, traverse.Min, spread, tol, micro, "FILL IN THE SILICON")
		}
	}

	// THE ID-REMAINDER CELL, harvested in the same session on the same corpus so
	// it is comparable with the 64-id row directly above it.
	t.Logf("-- id-remainder cells: %d ids at dim %d (the four-row grouping leaves a %d-row tail; "+
		"every pinned cell above uses %d ids, which the grouping divides exactly, so the tail "+
		"path they exercise is timed by nothing) --",
		idRemainderCount, idRemainderDim, idRemainderCount%4, mMax0)
	for _, a := range arms {
		full := measureTraverseNsPerDistance(a, idRemainderDim)
		part := measureIDRemainderNsPerDistance(a, idRemainderDim)
		if !isFinitePositive(part.Min) {
			t.Errorf("%s id-remainder cell produced a non-measurement: %+v", a.name, part)
			continue
		}
		spread := part.SpreadRatio()
		t.Logf("IDREM\t%-14s ids=%d dim=%d  min %7.1f  median %7.1f  max %7.1f  spread %.2fx  "+
			"-> tolerance %.2fx   (64-id min %7.1f, tail costs %+.1f%% per distance)",
			a.name, idRemainderCount, idRemainderDim, part.Min, part.Median, part.Max,
			spread, toleranceFor(spread), full.Min, 100*(part.Min-full.Min)/full.Min)
		t.Logf("IDREMPIN\t{Class: %s, Tier: %s, Dim: %d, TraverseNsPerDistance: %.1f, SpreadRatio: %.2f, Tolerance: %.2f, Machine: %q},",
			classConstName(class), tierConstName(a.name), idRemainderDim, part.Min, spread,
			toleranceFor(spread), "FILL IN THE SILICON")
	}

	if got != want {
		t.Fatalf("harvested %d measurements, expected %d (%d supported tier(s) x %d pinned dims). "+
			"A short table is not a partial result to read around — it means the harvester did not "+
			"measure what pinTable is about to claim it measured.", got, want, len(arms), len(pinnedDims))
	}
}

// measureMicroNsPerDistance is the cache-hot single-pair number, best of three.
//
// It keeps only a minimum where the traverse measurement now keeps a spread, and
// that asymmetry is deliberate: no gate asserts on the micro figure, so it needs
// no tolerance. It is reported for attribution, not for gating.
//
// It is reported ALONGSIDE the traverse number and never instead of it: a
// microbenchmark on two cache-resident vectors flatters every kernel by deleting
// the cache misses that dominate a real traversal.
func measureMicroNsPerDistance(a arm, dim int) float64 {
	x, y := seededPair(uint64(dim), dim)

	var sink float32
	best := math.Inf(1)
	for range 3 {
		start := time.Now()
		for range microIters {
			sink += a.dot(x, y)
		}
		elapsed := time.Since(start)
		if v := float64(elapsed.Nanoseconds()) / float64(microIters); v < best {
			best = v
		}
	}
	runtime.KeepAlive(sink)
	return best
}

func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// classConstName maps a resolved machine class back to the Go constant that
// names it, on the same reasoning as tierConstName below: a harvested row is
// pasted, not retyped, and a class spelled as a bare string literal where every
// other row carries a constant is a row that survives a class rename.
func classConstName(class string) string {
	switch class {
	case ClassARM64:
		return "ClassARM64"
	case ClassAMD64AVX512:
		return "ClassAMD64AVX512"
	case ClassAMD64NoAVX512:
		return "ClassAMD64NoAVX512"
	default:
		return "CLASS_CONSTANT_UNKNOWN_FOR_" + class
	}
}

// tierConstName maps a tier's runtime string back to the Go constant that names
// it, so a harvested row can be pasted into pinTable without hand-translating.
//
// An unknown tier returns a form that DOES NOT COMPILE, on purpose: a new tier
// whose constant this function has not learned should stop the paste at the
// compiler rather than silently landing a string literal where every other row
// carries a constant.
func tierConstName(tier string) string {
	switch tier {
	case TierReference:
		return "TierReference"
	case TierNEON:
		return "TierNEON"
	case TierAVX512:
		return "TierAVX512"
	case TierAVX2:
		return "TierAVX2"
	default:
		return "TIER_CONSTANT_UNKNOWN_FOR_" + tier
	}
}
