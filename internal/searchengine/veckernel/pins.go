// SPDX-License-Identifier: Apache-2.0

package veckernel

// pins.go holds the PERFORMANCE CONTRACT ITSELF: the per-class, per-tier,
// per-dim floors, and the policy that decides which of two supported tiers
// dispatch prefers.
//
// It is a non-test file on purpose. The floors are part of what this package
// promises, the README quotes them, and a benchmark run fills its rows here
// rather than in somebody's benchmark notes.
//
// The vocabulary these tables are keyed by lives in machineclass.go.

// pinTable is the standing floor table, KEYED BY MACHINE CLASS.
//
// Every row was produced by TestPerfHarvestPins, which calls the SAME
// measurement function the floor gate calls, so the pinned number and the
// checked number share one code path rather than being two protocols that drift.
//
// PROVENANCE, per class:
//
//   - ClassARM64 — Apple M4 Max. Mostly the kernel shoot-out's numbers; the NEON
//     dim 256 and 512 rows were re-harvested 2026-08-25 because the shoot-out's
//     figures there were 2.5x looser than the tier's real cost. See the row
//     comment.
//   - ClassAMD64AVX512 — GCE c3-standard-8, Intel Xeon Platinum 8481C (Sapphire
//     Rapids), 2026-08-25.
//   - ClassAMD64NoAVX512 — GCE n2d-standard-8, AMD EPYC 7B13 (Milan), 2026-08-25.
//     Genuinely AVX2-only silicon, so this class's AVX2 numbers carry no
//     forced-tier approximation.
//
// THE PINNED VALUE ON THE CLOUD CLASSES IS THE BEST OF TWO INDEPENDENT HARVESTS,
// each of which is itself the best of three runs. Same reasoning as the
// best-of-three inside one harvest: benchmark noise on a shared cloud host is
// one-sided — a preempted or throttled run is only ever slower — so the minimum
// is the closest estimate of the kernel's actual cost, and a floor set from it is
// the stricter, more useful one. The two c3 harvests agreed to within 2% at every
// slot except amd64-avx2 at dim 256 (23.5 vs 19.6), which is why two were taken.
//
// READ EACH CLASS AGAINST ITSELF, NEVER ACROSS CLASSES. The c3 part has a 105 MiB
// L3 against this benchmark's 128 MiB corpus, so its traverse is substantially
// cache-resident where the M4 Max's is not; the classes price different memory
// systems and a ratio between them is not a kernel comparison. Within each class
// the reference row is the calibration. See README.md.
//
// A CLASS WITH NO ROW HERE GETS NO FLOOR. There is deliberately no nearest-class
// fallback: pinFor keys on the exact class, and the floor gate reports a class it
// has no rows for as "nothing was checked" rather than borrowing another class's
// numbers. ClassAMD64NoAVX512 has NO amd64-avx512 row and must never gain one —
// that tier cannot execute on such a part, so its absence is correct rather than
// missing, and TestEveryClassesPinsAreCompleteAndMeasured asserts the absence.
var pinTable = []Pin{
	// -- arm64, NEON -------------------------------------------------------
	// MIXED PROVENANCE, AND IT IS LABELED PER ROW BECAUSE IT MATTERS. The dim
	// 1024 and 2048 rows carry the shoot-out's original figures for the fused
	// four-row Go-assembly arm, the same shape neonGather implements, and they
	// still track what the tier achieves (measured 53.6 and 118.9 against them,
	// 0.81x and 1.01x).
	//
	// The dim 256 and 512 rows were RE-HARVESTED 2026-08-25 by
	// TestPerfHarvestPins on the same machine. The shoot-out's figures there —
	// 37.5 and 55.0 — sat about 2.5x above what the tier actually does, so the
	// gate's 2x limit left roughly a FIVEFOLD window at those two widths: NEON
	// could have halved its throughput and still passed. A floor that loose is
	// not a floor, it is a formality, and the two widths where a kernel is most
	// arithmetic-bound are the worst place to have one.
	//
	// The two re-harvested rows carry a micro figure because the harvester
	// produces one; the two older rows do not, because the shoot-out did not
	// record that shape. reportPin says "not set for this benchmark shape"
	// rather than printing a zero as though it were a measurement.
	//
	// DIM 256 IS NOW THE TIGHTEST ROW IN THE WHOLE TABLE, running about 1.34x on
	// an ordinary floor-gate pass, and that is disclosed rather than discovered
	// later. Two things stack: the pin is a minimum-of-minimums like every other
	// row, which sits at the optimistic end by design; and dim 256 is the
	// SHORTEST measurement the protocol takes — 2000 hops x 64 candidates at
	// ~16 ns is ~2 ms of work, short enough to be scheduling-noise-dominated,
	// where dim 2048 runs ~8x longer and is correspondingly steadier. Observed
	// spread across four runs was 15.9-21.2 ns against a 31.8 limit. If this row
	// ever goes flaky the fix is to re-pin IT from a typical run; loosening
	// regressionFactor would trade one row's precision for every row's.
	{Class: ClassARM64, Tier: TierNEON, Dim: 256, TraverseNsPerDistance: 11.8, SpreadRatio: 1.12, Tolerance: 1.40, MicroNsPerDistance: 16.3, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 512, TraverseNsPerDistance: 23.6, SpreadRatio: 1.07, Tolerance: 1.34, MicroNsPerDistance: 26.9, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 1024, TraverseNsPerDistance: 51.4, SpreadRatio: 1.04, Tolerance: 1.30, MicroNsPerDistance: 51.3, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 2048, TraverseNsPerDistance: 115.4, SpreadRatio: 1.02, Tolerance: 1.30, MicroNsPerDistance: 95.7, Machine: armMachine},

	// -- arm64, reference --------------------------------------------------
	// The portable kernel's own floor. Pinning it matters as much as pinning the
	// assembly: it is what every non-arm64, non-amd64 build runs, and a
	// regression here is invisible from the assembly tier's numbers.
	{Class: ClassARM64, Tier: TierReference, Dim: 256, TraverseNsPerDistance: 65.8, SpreadRatio: 1.07, Tolerance: 1.34, MicroNsPerDistance: 47.0, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 512, TraverseNsPerDistance: 122.6, SpreadRatio: 1.05, Tolerance: 1.31, MicroNsPerDistance: 99.6, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 1024, TraverseNsPerDistance: 231.6, SpreadRatio: 1.04, Tolerance: 1.30, MicroNsPerDistance: 217.4, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 2048, TraverseNsPerDistance: 490.0, SpreadRatio: 1.01, Tolerance: 1.30, MicroNsPerDistance: 445.9, Machine: armMachine},

	// -- amd64, AVX-512F ---------------------------------------------------
	// Faster than AVX2 at every width on this part, by 9% at dim 256 rising to
	// 22% at dim 1024 and falling back to 5% at dim 2048 — which is why
	// amd64PreferAVX512 is true. The frequency downclock dispatchPolicy warns
	// about did not materialize on Sapphire Rapids; it is a hazard of older
	// server parts, and the policy stays a measurement precisely so a machine
	// class where it DOES bite gets its own row rather than this one's answer.
	{Class: ClassAMD64AVX512, Tier: TierAVX512, Dim: 256, TraverseNsPerDistance: 13.3, SpreadRatio: 1.11, Tolerance: 1.39, MicroNsPerDistance: 9.8, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX512, Dim: 512, TraverseNsPerDistance: 24.4, SpreadRatio: 1.14, Tolerance: 1.43, MicroNsPerDistance: 16.0, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX512, Dim: 1024, TraverseNsPerDistance: 64.0, SpreadRatio: 1.20, Tolerance: 1.50, MicroNsPerDistance: 29.6, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX512, Dim: 2048, TraverseNsPerDistance: 90.3, SpreadRatio: 1.11, Tolerance: 1.39, MicroNsPerDistance: 56.8, Machine: amd64Machine},

	// -- amd64, AVX2/FMA ---------------------------------------------------
	// Measured on the SAME instance in the same session through the forced-tier
	// seam, which is the only way to compare two tiers without a machine
	// difference hiding inside the number.
	//
	// A CAVEAT THAT TRAVELS WITH THESE ROWS: forcing AVX2 on AVX-512-capable
	// silicon approximates but does not reproduce a true AVX2-only part, whose
	// frequency behavior is a different bin. These are AVX2's numbers ON THIS
	// PART, which is what the dispatch decision for this part needs, and not a
	// prediction for the client silicon that has AVX-512 fused off.
	{Class: ClassAMD64AVX512, Tier: TierAVX2, Dim: 256, TraverseNsPerDistance: 14.9, SpreadRatio: 1.08, Tolerance: 1.36, MicroNsPerDistance: 12.7, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX2, Dim: 512, TraverseNsPerDistance: 27.5, SpreadRatio: 1.14, Tolerance: 1.43, MicroNsPerDistance: 22.2, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX2, Dim: 1024, TraverseNsPerDistance: 74.7, SpreadRatio: 1.14, Tolerance: 1.43, MicroNsPerDistance: 40.6, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX2, Dim: 2048, TraverseNsPerDistance: 108.7, SpreadRatio: 1.09, Tolerance: 1.37, MicroNsPerDistance: 78.5, Machine: amd64Machine},

	// -- amd64, reference --------------------------------------------------
	// The harness calibration for the amd64 half of the table, and the same job
	// it does on arm64: it is the portable algorithm, so its numbers say what
	// this instance is worth independently of any assembly, and the assembly
	// rows can be read against it rather than as figures from an unrelated rig.
	{Class: ClassAMD64AVX512, Tier: TierReference, Dim: 256, TraverseNsPerDistance: 102.8, SpreadRatio: 1.03, Tolerance: 1.30, MicroNsPerDistance: 95.1, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierReference, Dim: 512, TraverseNsPerDistance: 202.7, SpreadRatio: 1.03, Tolerance: 1.30, MicroNsPerDistance: 191.0, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierReference, Dim: 1024, TraverseNsPerDistance: 427.8, SpreadRatio: 1.01, Tolerance: 1.30, MicroNsPerDistance: 381.9, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierReference, Dim: 2048, TraverseNsPerDistance: 788.0, SpreadRatio: 1.00, Tolerance: 1.30, MicroNsPerDistance: 753.7, Machine: amd64Machine},

	// -- amd64 WITHOUT AVX-512, AVX2/FMA -----------------------------------
	// GENUINELY AVX2-ONLY SILICON, which is what makes this class worth its own
	// run rather than an extrapolation. The ClassAMD64AVX512 AVX2 rows above were
	// taken by FORCING the tier on a part that also has AVX-512, and a forced
	// tier runs in that part's frequency bin rather than in the bin a non-AVX-512
	// part actually ships in. These rows have no such approximation in them.
	//
	// This is also the class most amd64 users are in: a large installed base of
	// client silicon has AVX-512 fused off, so AVX2 is not a fallback rung there,
	// it is the only assembly tier and the one that runs every query.
	//
	// THE amd64-avx512 TIER HAS NO ROW IN THIS CLASS AND MUST NEVER GAIN ONE. It
	// cannot execute on a part without AVX-512F, so a row here could only have
	// come from copying the other class's numbers.
	{Class: ClassAMD64NoAVX512, Tier: TierAVX2, Dim: 256, TraverseNsPerDistance: 21.0, SpreadRatio: 1.21, Tolerance: 1.52, MicroNsPerDistance: 20.6, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierAVX2, Dim: 512, TraverseNsPerDistance: 32.4, SpreadRatio: 1.06, Tolerance: 1.32, MicroNsPerDistance: 31.9, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierAVX2, Dim: 1024, TraverseNsPerDistance: 85.3, SpreadRatio: 1.03, Tolerance: 1.30, MicroNsPerDistance: 50.3, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierAVX2, Dim: 2048, TraverseNsPerDistance: 155.4, SpreadRatio: 1.34, Tolerance: 1.68, MicroNsPerDistance: 92.3, Machine: amd64NoAVX512Machine},

	// -- amd64 WITHOUT AVX-512, reference ----------------------------------
	// The calibration row for this class, and it earns its keep here more than
	// anywhere else in the table: it is the same portable algorithm as the other
	// classes' reference rows, so it is the only thing that makes this class's
	// assembly numbers readable against a class measured on different silicon.
	{Class: ClassAMD64NoAVX512, Tier: TierReference, Dim: 256, TraverseNsPerDistance: 128.1, SpreadRatio: 1.15, Tolerance: 1.43, MicroNsPerDistance: 95.8, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierReference, Dim: 512, TraverseNsPerDistance: 204.8, SpreadRatio: 1.02, Tolerance: 1.30, MicroNsPerDistance: 182.1, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierReference, Dim: 1024, TraverseNsPerDistance: 379.7, SpreadRatio: 1.04, Tolerance: 1.30, MicroNsPerDistance: 355.8, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierReference, Dim: 2048, TraverseNsPerDistance: 761.5, SpreadRatio: 1.06, Tolerance: 1.33, MicroNsPerDistance: 699.9, Machine: amd64NoAVX512Machine},
}

// amd64Machine names the silicon every ClassAMD64AVX512 row above was measured
// on.
//
// One constant rather than twelve string literals, because a pin's Machine is
// the difference between a measurement and a rumor and twelve copies of it is
// twelve chances for one row to end up claiming a different part than the run it
// came from. The model number is READ FROM THE INSTANCE — /proc/cpuinfo reported
// "Intel(R) Xeon(R) Platinum 8481C CPU @ 2.70GHz" — rather than inferred from
// the machine family, because a family is a purchasing category and a part
// number is what executed the code.
const armMachine = "Apple M4 Max"

const amd64Machine = "GCE c3-standard-8 (Intel Xeon Platinum 8481C, Sapphire Rapids)"

// amd64NoAVX512Machine names the silicon every ClassAMD64NoAVX512 row was
// measured on. Same reasoning as amd64Machine, and the model was likewise read
// from the instance: /proc/cpuinfo reported "AMD EPYC 7B13", family 25, with
// "avx avx2" present and NO avx512 flag of any kind — which is the property that
// puts it in this class and the property that makes its AVX2 numbers real rather
// than a forced-tier approximation.
const amd64NoAVX512Machine = "GCE n2d-standard-8 (AMD EPYC 7B13, Milan)"

// idRemainderPins is the floor table for the ID-REMAINDER cell: the same
// traverse at idRemainderCount ids instead of mMax0.
//
// A SEPARATE TABLE RATHER THAN AN EXTRA KEY ON pinTable, because it is a
// different question with a different shape. pinTable answers "what does a hop
// cost at each production width"; this answers "what does the per-row tail cost
// at all", and it needs exactly one width to do so. Folding an id count into
// pinFor's key would put a dimension on every lookup in the package to serve
// four rows.
//
// Rows are filled by the IDREMPIN lines TestPerfHarvestPins prints.
//
// WHAT THESE ROWS ACTUALLY MEASURE, which is NOT what the cell was named for.
//
// The 63-id figure is roughly double the 64-id figure on the AVX-512-capable
// class and roughly equal on the other two. That looked like a per-row tail cost
// and was recorded as one. TWO CONTROLS ON THE HARDWARE THAT SHOWS IT PROVED
// OTHERWISE:
//
//   - Replacing every scalar-dot call in the tail with a single fused four-row
//     call — the exact fix a tail cost would demand — moved the number by 0.4%.
//     The tail KERNEL is not the cost.
//   - Holding the walk FIXED, so every id count visits the same precomputed
//     nodes in the same order, collapses the difference to +0.2%: 60/63/64 ids
//     cost 259.7/260.2/257.7 ns on the c3.
//
// So the effect is the ARGMAX MOVING THE WALK. Scoring 63 candidates instead of
// 64 elects a different winner, the traversal follows a different path, and that
// path has worse locality. A memory-bound assembly tier pays the whole
// difference; the portable reference is compute-bound and cannot reach the
// memory system hard enough to notice — which is why its silence looked like
// evidence that the walk had not changed, and was not.
//
// THESE ROWS ARE STILL WORTH PINNING, but as what they are: a stable,
// reproducible tripwire on a hop whose id count is not a multiple of four
// (spreads 1.00-1.04 per class). They are NOT a measurement of the per-row tail,
// and no kernel change should be justified by them. The instrument that isolates
// the tail is the fixed-itinerary control in remainder_test.go, and it puts the
// tail at 0-2%.
var idRemainderPins = []Pin{
	{Class: ClassARM64, Tier: TierNEON, Dim: idRemainderDim, TraverseNsPerDistance: 51.6, SpreadRatio: 1.01, Tolerance: 1.30, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: idRemainderDim, TraverseNsPerDistance: 230.7, SpreadRatio: 1.01, Tolerance: 1.30, Machine: armMachine},

	{Class: ClassAMD64AVX512, Tier: TierAVX512, Dim: idRemainderDim, TraverseNsPerDistance: 135.7, SpreadRatio: 1.04, Tolerance: 1.30, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierAVX2, Dim: idRemainderDim, TraverseNsPerDistance: 141.2, SpreadRatio: 1.02, Tolerance: 1.30, Machine: amd64Machine},
	{Class: ClassAMD64AVX512, Tier: TierReference, Dim: idRemainderDim, TraverseNsPerDistance: 427.8, SpreadRatio: 1.01, Tolerance: 1.30, Machine: amd64Machine},

	{Class: ClassAMD64NoAVX512, Tier: TierAVX2, Dim: idRemainderDim, TraverseNsPerDistance: 85.3, SpreadRatio: 1.03, Tolerance: 1.30, Machine: amd64NoAVX512Machine},
	{Class: ClassAMD64NoAVX512, Tier: TierReference, Dim: idRemainderDim, TraverseNsPerDistance: 376.8, SpreadRatio: 1.00, Tolerance: 1.30, Machine: amd64NoAVX512Machine},
}

// wiredTraversePins is the floor table for the WIRED-CALLER cell: the same
// corpus and hop count as pinTable, walked in the shape the production caller
// actually has now that it batches.
//
// WHY A SEPARATE CELL RATHER THAN TRUSTING pinTable. pinTable measures the
// KERNEL: every hop hands the gather exactly mMax0 ids, a multiple of four, with
// no collection pass in front of it. The production traversal does neither. It
// filters each neighbor row through a visited set first, so the run it finally
// scores is a VARIABLE length that lands on every residue mod four, and it pays
// that filter on the Go side before the kernel sees anything. A regression in
// the wiring — a collection pass that reallocates per hop, a scratch that stops
// being reused, a run accidentally split into several gather calls — moves this
// cell and moves nothing in pinTable, because pinTable never exercises the
// wiring at all.
//
// THE SAME PROTOCOL GOVERNS THIS TABLE AS THE OTHERS: rows are keyed by exact
// machine class with no nearest-class fallback, Tolerance is DERIVED from the
// cell's own harvested SpreadRatio by toleranceFor rather than chosen, and the
// gate is two-sided so a measurement far BELOW the pin is reported as a stale
// floor rather than celebrated.
//
// A CLASS WITH NO ROW HERE GETS NO FLOOR AND IS REPORTED AS UNCHECKED. Rows are
// filled from TestWiredTraverseHarvestPins on the hardware in question; a class
// this developer machine is not a member of must be harvested on that hardware
// rather than extrapolated. That is why the cloud classes are absent below
// instead of carrying numbers derived from the arm64 run.
// THESE NUMBERS ARE NOT COMPARABLE WITH pinTable's, AND THAT IS BY DESIGN. This
// cell walks a UNIFORM RANDOM ITINERARY where pinTable walks an argmax chain, so
// it is deliberately more cache-hostile and its ns/distance is higher at every
// width. The two tables answer different questions and a ratio between them is
// not a measurement of anything. Read each against itself.
//
// PROVENANCE: harvested 2026-08-25 on armMachine by TestWiredTraverseHarvestPins,
// BEST OF TWO INDEPENDENT HARVESTS each itself the best of five runs — the same
// one-sided-noise reasoning pinTable records, since a preempted run is only ever
// slower. Each row's Tolerance is derived by toleranceFor from the WIDER of the
// two harvests' spreads, so a cell that was noisy in either run is gated at the
// looser of the two rather than at a figure one lucky harvest suggested.
//
// THE SPREADS ON THIS TABLE ARE WIDER THAN pinTable's AND THE REASON IS KNOWN:
// the harvest ran on a developer machine carrying concurrent agent lanes (the
// same contention that makes golangci-lint's machine-global lock collide), so
// dim 256 NEON saw a 1.76x spread and is gated at 2.20x accordingly. That is
// honest rather than tight. Re-harvest on a quiet machine before treating the
// two narrow-width NEON rows as precise floors; the wide-width rows, whose
// spreads are 1.05-1.19, are already usable as they stand.
var wiredTraversePins = []Pin{
	// -- arm64, NEON -------------------------------------------------------
	{Class: ClassARM64, Tier: TierNEON, Dim: 256, TraverseNsPerDistance: 48.1, SpreadRatio: 1.76, Tolerance: 2.20, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 512, TraverseNsPerDistance: 97.2, SpreadRatio: 1.25, Tolerance: 1.56, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 1024, TraverseNsPerDistance: 148.4, SpreadRatio: 1.27, Tolerance: 1.59, Machine: armMachine},
	{Class: ClassARM64, Tier: TierNEON, Dim: 2048, TraverseNsPerDistance: 237.0, SpreadRatio: 1.19, Tolerance: 1.49, Machine: armMachine},

	// -- arm64, reference --------------------------------------------------
	// The portable kernel through the same wiring. It earns its place for the
	// reason pinTable's reference rows do: it is what every non-arm64,
	// non-amd64 build runs, and a wiring regression there is invisible from the
	// assembly tier's numbers.
	{Class: ClassARM64, Tier: TierReference, Dim: 256, TraverseNsPerDistance: 315.4, SpreadRatio: 1.23, Tolerance: 1.54, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 512, TraverseNsPerDistance: 385.0, SpreadRatio: 1.09, Tolerance: 1.36, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 1024, TraverseNsPerDistance: 525.0, SpreadRatio: 1.28, Tolerance: 1.60, Machine: armMachine},
	{Class: ClassARM64, Tier: TierReference, Dim: 2048, TraverseNsPerDistance: 947.1, SpreadRatio: 1.07, Tolerance: 1.34, Machine: armMachine},

	// -- THE CLOUD CLASSES ARE DELIBERATELY ABSENT -------------------------
	// ClassAMD64AVX512 and ClassAMD64NoAVX512 have NO wired rows. This developer
	// machine is not a member of either class, and the no-nearest-class rule
	// means the gate reports "never harvested" on those hosts rather than
	// borrowing arm64's numbers. Filling them needs a run on that hardware, in
	// the same GCE campaign the later kernel work schedules.
}

// wiredPinFor returns the wired-traverse pin for one (class, tier, dim).
//
// EXACT CLASS MATCH, NO NEAREST-CLASS FALLBACK — the same rule pinFor states,
// for the same reason: borrowing another class's number produces a gate that
// passes while guarding silicon nobody measured.
func wiredPinFor(class, tier string, dim int) (Pin, bool) {
	for _, p := range wiredTraversePins {
		if p.Class == class && p.Tier == tier && p.Dim == dim {
			return p, true
		}
	}
	return Pin{}, false
}

// wiredClassHasPins reports whether wiredTraversePins carries ANY row for a
// class, so the gate can distinguish "this slot is missing from a measured
// class" from "this machine has never been harvested".
func wiredClassHasPins(class string) bool {
	for _, p := range wiredTraversePins {
		if p.Class == class {
			return true
		}
	}
	return false
}

// idRemainderPinFor returns the id-remainder pin for one (class, tier).
func idRemainderPinFor(class, tier string) (Pin, bool) {
	for _, p := range idRemainderPins {
		if p.Class == class && p.Tier == tier {
			return p, true
		}
	}
	return Pin{}, false
}

// pinFor returns the pin for one (class, tier, dim), and whether it exists.
//
// EXACT CLASS MATCH, NO NEAREST-CLASS FALLBACK. A host whose class has no row
// gets no floor and the caller reports that, which is the entire point of keying
// by class: borrowing another class's number would produce a gate that passes
// while guarding silicon nobody measured.
func pinFor(class, tier string, dim int) (Pin, bool) {
	for _, p := range pinTable {
		if p.Class == class && p.Tier == tier && p.Dim == dim {
			return p, true
		}
	}
	return Pin{}, false
}

// classHasPins reports whether pinTable carries ANY row for a class.
//
// It exists so the floor gate can tell two different failures apart: "this class
// has been measured but this tier/dim slot is missing" is a hole in a table
// somebody filled, while "this class has no rows at all" is a machine nobody has
// benchmarked. They read identically from a per-slot lookup and they need
// different fixes — add a row, versus run the procedure on that hardware.
func classHasPins(class string) bool {
	for _, p := range pinTable {
		if p.Class == class {
			return true
		}
	}
	return false
}

// idRemainderCount is the id-list length the id-remainder cell is pinned at.
//
// 63, NOT 64, AND THAT ONE FEWER IS THE ENTIRE POINT. The fused gather scores
// four rows at a time and finishes what is left one row at a time, so an id list
// whose length is a multiple of four never runs the per-row tail at all. Every
// pinned cell in pinTable uses mMax0 = 64 ids, which is exactly such a multiple:
// the tail path is compiled, correctness-graded at every residue by the tail
// suite, and TIMED BY NOTHING. A regression that landed only there — a
// mis-scheduled single-row loop, a lost prefetch, a spilled register — would not
// move a single pinned number.
//
// 63 gives fifteen full groups plus a three-row remainder, which is the longest
// tail the grouping can produce.
//
// WHAT THIS CELL IS FOR, AND WHAT IT IS NOT. It is a FLOOR UNDER AN UNGUARDED
// CODE PATH, on a deterministic pinned workload — that is the whole claim. It
// does NOT isolate the tail's true cost: scoring 63 of a 64-neighbor row
// changes which candidate wins the hop, so the walk diverges from the 64-id walk
// and part of any difference is the different traversal rather than the tail.
//
// THE MEASURED SIZE OF THE EFFECT IS SMALL, and the number is recorded here
// because a review round claimed it was 56-78% and that figure will otherwise be
// repeated. On an M4 Max at dim 1024: arm64-neon 52.9 ns at 63 ids against
// 51.4 ns at 64, or +2.9%; go-unroll4 231.4 against 231.3, or +0.0%. The
// reference number is the control that makes the NEON one readable — the
// reference gather has no four-row grouping and therefore no tail, and it shows
// no difference at all. The large figures came from dim-256 sweeps where the
// walk changed: in those, remainder=1 measured CHEAPER than remainder=0, which
// is impossible if the tail were driving the cost.
const idRemainderCount = 63

// idRemainderDim is the single width the id-remainder cell is pinned at.
//
// One width rather than four: the cell exists to put a floor under a code path
// that currently has none, and the path's cost is dominated by per-row call and
// setup overhead rather than by width. 1024 is the middle production width and
// the one the rest of the table is most densely measured at.
const idRemainderDim = 1024

// pinnedDims are the widths the floor table and the benchmark suite cover.
var pinnedDims = []int{256, 512, 1024, 2048}

// dispatchPolicy is the RULE for preferring one supported tier over another,
// and it exists as a written policy because the obvious rule is wrong.
//
// THE POLICY: between two tiers that a machine can both execute, dispatch
// prefers the one that is FASTER IN THE TRAVERSE-SHAPED BENCHMARK ON THAT
// MACHINE CLASS. Preference is a MEASUREMENT, not an ordering by register
// width. Where measurements disagree across machine classes, the table above
// carries a row per class and the preference is set per class.
//
// WHY NOT "WIDEST WINS", which is what everyone writes first:
//
//   - AVX-512 can be NET SLOWER than AVX2 on the same silicon. Wide-vector
//     execution on some server parts drops the core's clock, and a kernel that
//     is memory-bound — which this one is, on a corpus that does not fit in
//     cache — gains little from the extra width while paying the full frequency
//     penalty. A static widest-wins ladder ships the slower kernel to exactly
//     those machines.
//   - AVX2 IS THE MAINSTREAM PATH regardless of which is faster. A large
//     installed base of modern client silicon has AVX-512 fused off entirely, so
//     AVX2 is what most machines will actually run. Treating it as a fallback
//     rung would leave the most-executed tier the least-benchmarked one.
//
// MEASURED SO FAR, ON ONE CLASS: on Sapphire Rapids the downclock did not bite
// and AVX-512 led at every width (see amd64PreferAVX512 for the figures). That
// settles Sapphire Rapids and nothing else. The policy is written the way it is
// precisely so the next class is measured rather than assumed to match this one.
//
// CONSEQUENCES, all of which the amd64 phase exercised rather than reworked —
// filling the table took no change to any seam below:
//
//   - Both amd64 tiers get their own rows in pinTable, neither derived from the
//     other.
//   - Every tier is forceable through ForceEnv regardless of what dispatch
//     prefers, so both can be benchmarked and agreement-graded on one machine.
//   - Forcing a tier the host cannot execute yields UnsupportedTierError with
//     the hardware reason, so a CI runner without AVX-512 skips that tier LOUDLY
//     instead of silently grading nothing.
//   - The floor test asserts the comparison directly: where two non-reference
//     tiers are both supported, the tier dispatch PREFERS must not be slower
//     than the other by more than dispatchPreferenceMargin. That assertion is
//     what turns this comment into a check.
const dispatchPolicyDoc = "preference between supported tiers is measured per machine class, not ordered by register width"

// dispatchPreferenceMargin is how much slower the PREFERRED tier may measure
// than a less-preferred one before the floor test calls the ordering wrong.
//
// 1.10 — ten percent. Benchmark noise on a shared machine is a few percent, so
// a preference that loses by more than ten is losing for a real reason and the
// table's ordering for that machine class needs revisiting.
//
// HOW MUCH ROOM THAT LEAVES, measured: on the Sapphire Rapids run the preferred
// tier WON at every width, by ratios of 0.86 / 0.83 / 0.80 / 0.98 (preferred
// over passed-over). The margin was nowhere near being tested, which is the
// state to want — it means the gate is a tripwire on a wrong ordering rather
// than a tolerance the current ordering is squeaking past. The tightest cell is
// dim 2048 at 0.98, which is the memory-bound width where the two tiers nearly
// converge; if a future part inverts the ordering, that is the cell that will
// say so first.
const dispatchPreferenceMargin = 1.10
