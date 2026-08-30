// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// perf_test.go turns the pinned floors in pins.go into assertions.
//
// ENV-GATED, NOT BUILD-TAG-GATED, and that is a deliberate choice against the
// grain. A build tag would keep timing-sensitive work out of the default suite
// just as well — but a file behind a tag is a file the linter never compiles
// unless every lint invocation is told about that tag, and a pass that silently
// skips files reports success having checked nothing. This project has been bitten
// by exactly that. Env-gating keeps the file in the normal build and lint graph
// while still keeping the timing work opt-in.
//
// The skip is LOUD: it names the variable to set and what will not be checked
// without it. A skip that reads like a pass is the failure this file is
// otherwise designed to catch.

// PerfEnv gates the timing assertions in this file. Set it to 1 to run them.
const PerfEnv = "VECKERNEL_PERF"

// perfHops is the FIXED number of hops every floor measurement performs, and
// fixing it is the whole reason this function does not just call
// testing.Benchmark.
//
// THE TRAVERSE NUMBER DEPENDS ON HOW LONG YOU WALK, which is a property of the
// workload and not a flaw to be tuned away. A random walk over a 128 MiB corpus
// revisits nodes, so the longer it runs the more of its working set is already
// cached and the faster each subsequent distance looks. Measured at authoring on
// the same machine and the same kernel: NEON at dim 256 reports 23.9
// ns/distance over 3000 hops and 9.9 ns/distance when testing.Benchmark scaled
// the walk into the hundreds of thousands. Both are honest; they measure
// different things.
//
// 2000 hops is chosen to sit at QUERY SCALE. A single beam search at ef=64
// expands on the order of a hundred candidates, so a few thousand hops is the
// order of magnitude a handful of real queries costs — not the six-figure walk
// an auto-scaled benchmark performs, which measures a corpus that has been
// warmed far past what any one query would warm.
//
// A floor compared against a measurement whose protocol drifts is not a floor,
// so the protocol is pinned here rather than left to the benchmark framework.
const perfHops = 2000

// envEnabled reports whether an opt-in env gate is set to exactly "1".
//
// Exactly "1" rather than "any non-empty value": an operator who exports
// VECKERNEL_PERF=0 to turn the gate OFF must not have turned it on.
func envEnabled(name string) bool { return os.Getenv(name) == "1" }

func requirePerfEnabled(t *testing.T) {
	t.Helper()
	if !envEnabled(PerfEnv) {
		t.Skipf("PERFORMANCE FLOORS NOT CHECKED. Set %s=1 to run them. Without it, a kernel "+
			"silently running its scalar path instead of its SIMD path — a 4-6x regression at "+
			"every production width — passes this suite. See the README's benchmark section.",
			PerfEnv)
	}
}

// measureTraverseNsPerDistance runs exactly perfHops hops for one tier and dim
// and returns nanoseconds per distance.
//
// Deliberately NOT testing.Benchmark: that would auto-scale the hop count and
// make the result depend on how long the framework decided to run, which is the
// one thing a floor cannot tolerate. See perfHops.
//
// The measurement is repeated and the BEST run is taken. Best rather than mean
// because the noise on a shared developer machine is one-sided — scheduler
// preemption and thermal throttling only ever make a run slower — so the
// minimum is the closest estimate of the kernel's actual cost, and using it
// keeps the gate from failing for reasons that have nothing to do with the code.
// measurement is one cell's timing evidence: not a single number, but the
// distribution the runs actually produced.
//
// RETURNING THE SPREAD IS THE POINT. The previous version computed five runs,
// kept the minimum and threw the rest away — which meant the one quantity needed
// to set an honest per-cell tolerance was measured and then discarded on every
// single harvest. Min is still what gets pinned; Max/Min is what sizes the gate.
type measurement struct {
	Min, Median, Max float64
}

// SpreadRatio is Max/Min: the cell's own measured noise.
func (m measurement) SpreadRatio() float64 {
	if m.Min <= 0 {
		return 0
	}
	return m.Max / m.Min
}

// measureRuns is how many timed passes each cell gets. Five, and odd on purpose
// so Median is an observed run rather than an average of two.
const measureRuns = 5

func measureTraverseNsPerDistance(a arm, dim int) measurement {
	c := buildTraverseCorpus(dim)
	scratch := make([]float32, mMax0)

	// Touch the corpus once so first-touch page faults are not in any timed run.
	runTraverseSim(a, c, 200, scratch)

	got := make([]float64, 0, measureRuns)
	for range measureRuns {
		start := time.Now()
		n := runTraverseSim(a, c, perfHops, scratch)
		elapsed := time.Since(start)
		got = append(got, float64(elapsed.Nanoseconds())/float64(n))
	}
	return summarize(got)
}

// measureIDRemainderNsPerDistance prices a hop of idRemainderCount ids — a
// length the four-row grouping does NOT divide, so the per-row tail runs on
// every hop.
//
// Same corpus, same hop count, same run count as the 64-id measurement, so the
// two are directly comparable and the difference is attributable to the tail.
func measureIDRemainderNsPerDistance(a arm, dim int) measurement {
	c := buildTraverseCorpus(dim)
	scratch := make([]float32, mMax0)

	runTraverseSimN(a, c, 200, idRemainderCount, scratch)

	got := make([]float64, 0, measureRuns)
	for range measureRuns {
		start := time.Now()
		n := runTraverseSimN(a, c, perfHops, idRemainderCount, scratch)
		elapsed := time.Since(start)
		got = append(got, float64(elapsed.Nanoseconds())/float64(n))
	}
	return summarize(got)
}

// measureIDsNsPerDistance prices a hop of exactly ids candidates on an
// already-built corpus, so a caller can compare two id counts or two gather
// variants without rebuilding anything between them.
func measureIDsNsPerDistance(a arm, c *traverseCorpus, ids int) measurement {
	scratch := make([]float32, mMax0)
	runTraverseSimN(a, c, 200, ids, scratch)

	got := make([]float64, 0, measureRuns)
	for range measureRuns {
		start := time.Now()
		n := runTraverseSimN(a, c, perfHops, ids, scratch)
		elapsed := time.Since(start)
		got = append(got, float64(elapsed.Nanoseconds())/float64(n))
	}
	return summarize(got)
}

// summarize turns raw run times into the pinned minimum plus its spread.
//
// MIN REMAINS THE PINNED VALUE. Noise on a shared machine is one-sided — a
// preempted or throttled run is only ever slower — so the minimum is the closest
// estimate of the kernel's actual cost. What changes is that the other runs are
// now kept as evidence about how trustworthy that minimum is, instead of being
// dropped on the floor.
func summarize(runs []float64) measurement {
	if len(runs) == 0 {
		return measurement{}
	}
	s := append([]float64(nil), runs...)
	sort.Float64s(s)
	return measurement{Min: s[0], Median: s[len(s)/2], Max: s[len(s)-1]}
}

// TestPerfFloorsTraverse is the standing regression gate: every supported tier
// at every pinned dim must stay within regressionFactor of its floor.
func TestPerfFloorsTraverse(t *testing.T) {
	requirePerfEnabled(t)

	class := machineClass()
	t.Logf("machine class resolved to %q on %s/%s", class, runtime.GOOS, runtime.GOARCH)

	checked, missing := 0, 0
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			p, ok := pinFor(class, a.name, dim)
			if !ok {
				missing++
				// The two absences are different failures with different fixes,
				// so they get different messages rather than one that fits
				// neither: an unmeasured CLASS needs a benchmark run on this
				// hardware, a missing SLOT in a measured class needs a row.
				if !classHasPins(class) {
					t.Errorf("NO PINS AT ALL FOR MACHINE CLASS %q — this host's class has never "+
						"been benchmarked, and no other class's numbers are borrowed for it. Run "+
						"the procedure in README.md on this hardware. (tier %s, dim %d)",
						class, a.name, dim)
					continue
				}
				t.Errorf("NO PIN for class %s/%s/dim=%d — a supported tier with no floor is an "+
					"unguarded tier; add a row to pinTable", class, a.name, dim)
				continue
			}
			if p.Unmeasured || p.TraverseNsPerDistance == 0 {
				missing++
				t.Logf("PIN UNMEASURED for %s/%s/dim=%d — NOT CHECKED. Fill it from a run on "+
					"this hardware.", runtime.GOARCH, a.name, dim)
				continue
			}

			if p.Tolerance <= 0 {
				missing++
				t.Errorf("PIN %s/%s/dim=%d carries no Tolerance. Every measured row must derive "+
					"one from its harvest spread via toleranceFor; a row without it cannot be "+
					"gated and this cell is unguarded.", class, a.name, dim)
				continue
			}

			got := measureTraverseNsPerDistance(a, dim)
			limit := p.TraverseNsPerDistance * p.Tolerance
			stale := p.TraverseNsPerDistance * staleFloorFactor
			ratio := got.Min / p.TraverseNsPerDistance
			checked++

			switch {
			case got.Min > limit:
				t.Errorf("REGRESSION %s/dim=%d: %.1f ns/distance exceeds %.1f (pin %.1f from %s, "+
					"x%.2f allowed, derived from this cell's %.2fx harvest spread). A tier that "+
					"has silently stopped using its SIMD path lands here. Run spread this time: "+
					"min %.1f median %.1f max %.1f.",
					a.name, dim, got.Min, limit, p.TraverseNsPerDistance, p.Machine, p.Tolerance,
					p.SpreadRatio, got.Min, got.Median, got.Max)
			case got.Min < stale:
				// THE OTHER SIDE OF THE GATE. Faster than the floor by this much
				// is not good news, it is evidence the floor is not describing
				// this kernel on this machine any more — and a one-sided gate
				// would have called it a clean pass.
				t.Errorf("STALE FLOOR %s/dim=%d: measured %.1f ns/distance, which is %.2fx the "+
					"pin of %.1f (from %s). A floor a tier beats by more than %.0f%% is not "+
					"conservative, it is absent: this cell would pass with the kernel running "+
					"%.1fx slower than it does. RE-HARVEST this class — the pin predates the "+
					"current kernel, the current protocol, or this hardware.",
					a.name, dim, got.Min, ratio, p.TraverseNsPerDistance, p.Machine,
					(1-staleFloorFactor)*100, 1/staleFloorFactor)
			default:
				t.Logf("OK  %-12s dim=%-5d min %7.1f med %7.1f max %7.1f ns/distance  "+
					"(pin %6.1f, tol %.2fx, limit %6.1f, ratio %.2fx)",
					a.name, dim, got.Min, got.Median, got.Max,
					p.TraverseNsPerDistance, p.Tolerance, limit, ratio)
			}
		}
	}

	// KNOWN POSITIVE FOR THE GATE ITSELF. If every pin were unmeasured, or the
	// tier table were empty, the loop above would complete without asserting
	// anything and report a clean pass. Requiring at least one real check makes
	// an all-empty table a failure rather than a green run.
	if checked == 0 {
		t.Fatalf("NO FLOOR WAS ACTUALLY CHECKED (%d pins missing or unmeasured). This gate "+
			"asserted nothing and must not be read as a pass.", missing)
	}
	t.Logf("%d floor(s) checked, %d slot(s) unmeasured or missing", checked, missing)
}

// TestPerfFloorsIDRemainder puts the same two-sided gate under the ID-REMAINDER
// path, which every other pinned cell leaves untimed.
//
// Every cell in pinTable uses mMax0 = 64 ids, and 64 is a multiple of the fused
// gather's four-row grouping — so the per-row tail those kernels carry is
// compiled, correctness-graded, and never once measured. This gate is the only
// thing in the package that would notice it regressing.
func TestPerfFloorsIDRemainder(t *testing.T) {
	requirePerfEnabled(t)

	class := machineClass()
	checked := 0
	for _, a := range testArms() {
		p, ok := idRemainderPinFor(class, a.name)
		if !ok {
			t.Errorf("NO ID-REMAINDER PIN for class %s tier %s — the per-row tail is unguarded "+
				"on this host, which is the state this cell exists to end", class, a.name)
			continue
		}
		if p.Tolerance <= 0 {
			t.Errorf("id-remainder pin %s/%s carries no Tolerance and cannot be gated", class, a.name)
			continue
		}

		got := measureIDRemainderNsPerDistance(a, p.Dim)
		limit := p.TraverseNsPerDistance * p.Tolerance
		stale := p.TraverseNsPerDistance * staleFloorFactor
		checked++

		switch {
		case got.Min > limit:
			t.Errorf("ID-REMAINDER REGRESSION %s (%d ids, dim %d): %.1f ns/distance exceeds %.1f "+
				"(pin %.1f from %s, x%.2f allowed). min %.1f median %.1f max %.1f.",
				a.name, idRemainderCount, p.Dim, got.Min, limit, p.TraverseNsPerDistance,
				p.Machine, p.Tolerance, got.Min, got.Median, got.Max)
		case got.Min < stale:
			t.Errorf("ID-REMAINDER STALE FLOOR %s (%d ids, dim %d): measured %.1f against a pin of "+
				"%.1f from %s — %.2fx. Re-harvest this class.",
				a.name, idRemainderCount, p.Dim, got.Min, p.TraverseNsPerDistance, p.Machine,
				got.Min/p.TraverseNsPerDistance)
		default:
			t.Logf("OK  %-12s %d ids dim=%d  min %7.1f med %7.1f  (pin %6.1f, tol %.2fx, ratio %.2fx)",
				a.name, idRemainderCount, p.Dim, got.Min, got.Median,
				p.TraverseNsPerDistance, p.Tolerance, got.Min/p.TraverseNsPerDistance)
		}
	}

	if checked == 0 {
		t.Fatalf("NO ID-REMAINDER FLOOR WAS CHECKED for class %q. This gate asserted nothing and "+
			"must not be read as a pass.", class)
	}
	t.Logf("%d id-remainder floor(s) checked for class %q", checked, class)
}

// TestPerfFloorGateRejectsASlowKernel is the known positive for the comparison
// itself: the gate must FAIL a kernel that is genuinely too slow.
//
// It runs the reference tier against an ASSEMBLY tier's floor, which the
// reference cannot meet by a wide margin at every production width. Without it,
// a gate whose comparison operator was inverted, or whose limit was computed as
// zero, would pass every measurement forever.
//
// THE BORROWED FLOOR IS CHOSEN BY MEASUREMENT-ON-FILE, not by tier name, so this
// control works unchanged on every architecture with an assembly tier. It takes
// the assembly tier with the LOWEST pinned floor at this width — the hardest one
// for the reference to meet — because a control that borrowed a slack floor
// could fail to fire for a reason that has nothing to do with the gate.
func TestPerfFloorGateRejectsASlowKernel(t *testing.T) {
	requirePerfEnabled(t)

	const dim = 1024

	var ref arm
	var borrowedTier string
	borrowedTolerance := 0.0
	borrowedFloor := math.Inf(1)
	for _, a := range testArms() {
		if a.name == TierReference {
			ref = a
			continue
		}
		p, ok := pinFor(machineClass(), a.name, dim)
		if !ok || p.Unmeasured || p.TraverseNsPerDistance <= 0 {
			continue
		}
		if p.TraverseNsPerDistance < borrowedFloor {
			borrowedFloor, borrowedTier, borrowedTolerance = p.TraverseNsPerDistance, a.name, p.Tolerance
		}
	}
	if ref.dot == nil {
		t.Fatal("reference tier not found in the tier table")
	}
	if borrowedTier == "" {
		t.Skipf("NO ASSEMBLY FLOOR TO BORROW for class %q at dim=%d: this control needs a "+
			"supported assembly tier with a MEASURED pin FOR THIS CLASS, and there is none here. "+
			"The gate's comparison is therefore UNPROVEN on this host — not proven working.",
			machineClass(), dim)
	}

	got := measureTraverseNsPerDistance(ref, dim)
	limit := borrowedFloor * borrowedTolerance

	if got.Min <= limit {
		t.Fatalf("GATE IS BLIND: the portable reference measured %.1f ns/distance and was "+
			"accepted against %s's limit of %.1f (floor %.1f x tolerance %.2f). The reference is "+
			"several times slower than any assembly tier at this width, so a gate that admits it "+
			"would admit a tier that had stopped using SIMD entirely.",
			got.Min, borrowedTier, limit, borrowedFloor, borrowedTolerance)
	}
	t.Logf("gate fired as required: reference %.1f ns/distance exceeds %s's limit %.1f",
		got.Min, borrowedTier, limit)
}

// TestDispatchPreferenceIsMeasured enforces dispatchPolicy: where two
// non-reference tiers are both supported, the one dispatch PREFERS must not be
// meaningfully slower than the one it passed over.
//
// ON amd64 THIS IS THE GATE ON amd64PreferAVX512: both tiers are supported on
// AVX-512-capable silicon, so the assertion re-times each of them and fails if
// the preferred one loses. On arm64 there is exactly one assembly tier and no
// pair to compare. THAT STATE IS REPORTED, NOT SILENTLY PASSED — a comparison
// with nothing to compare is indistinguishable from a comparison that ran and
// agreed, and an arm64 run must not be read as having verified the ordering that
// only an amd64 run can verify.
func TestDispatchPreferenceIsMeasured(t *testing.T) {
	requirePerfEnabled(t)

	var asmTiers []arm
	for _, a := range testArms() {
		if a.name != TierReference {
			asmTiers = append(asmTiers, a)
		}
	}

	if len(asmTiers) < 2 {
		names := make([]string, len(asmTiers))
		for i := range asmTiers {
			names[i] = asmTiers[i].name
		}
		t.Logf("NO PAIR TO COMPARE on %s: %d assembly tier(s) supported here (%v). "+
			"dispatchPolicy says %s — with one tier there is no preference to verify, and this "+
			"assertion is INERT rather than satisfied. The ordering it grades (%s against %s, "+
			"set by amd64PreferAVX512) is verified only by an amd64 run on silicon that supports "+
			"both; do not read this run as having checked it.",
			runtime.GOARCH, len(asmTiers), names, dispatchPolicyDoc, TierAVX512, TierAVX2)
		return
	}

	// tiers is ordered by preference, so asmTiers[0] is what dispatch picks.
	preferred, other := asmTiers[0], asmTiers[1]
	for _, dim := range pinnedDims {
		gotPreferred := measureTraverseNsPerDistance(preferred, dim)
		gotOther := measureTraverseNsPerDistance(other, dim)

		if gotPreferred.Min > gotOther.Min*dispatchPreferenceMargin {
			t.Errorf("DISPATCH PREFERS THE SLOWER TIER at dim=%d: %s measured %.1f ns/distance, "+
				"%s measured %.1f — %.2fx slower, past the %.2fx noise margin. Per dispatchPolicy, "+
				"preference is a MEASUREMENT on this machine class, so either the ordering in "+
				"asmArms is wrong for this class or the pins need a per-class row.",
				dim, preferred.name, gotPreferred.Min, other.name, gotOther.Min,
				gotPreferred.Min/gotOther.Min, dispatchPreferenceMargin)
			continue
		}
		t.Logf("OK dim=%-5d preferred %s %.1f ns/distance (med %.1f) vs %s %.1f (med %.1f)", dim,
			preferred.name, gotPreferred.Min, gotPreferred.Median, other.name, gotOther.Min, gotOther.Median)
	}
}
