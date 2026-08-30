// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"runtime"

	"golang.org/x/sys/cpu"
)

// machineclass.go holds the KEY the performance contract is filed under: what a
// machine class is, how the running process resolves its own, the shape of one
// pinned measurement, and the two rules that turn a cell's measured spread into
// the bounds the gate asserts.
//
// It sits apart from pins.go — which holds the tables — because these are the
// parts a reader has to understand BEFORE any number means anything, and because
// the tables grow with every machine class measured while this does not.

// -- machine classes ---------------------------------------------------------

// The classes pinTable is keyed by.
//
// A CLASS IS SOMETHING THE RUNNING PROCESS CAN DETERMINE ABOUT ITSELF, and that
// is the whole design constraint rather than a convenience. A key the binary
// cannot resolve is a key that INHERITS by default: every host would land on
// whichever class the lookup happened to match, which is precisely the silent
// borrowing a per-class table exists to stop, only dressed up as a lookup.
//
// On amd64 the one self-resolvable split that matters here is also the exact
// axis the measured instance classes differ on — AVX-512 presence. A Sapphire
// Rapids part resolves to the class whose rows were measured on Sapphire Rapids,
// an AMD Milan part to the class whose rows were measured on Milan, with no
// operator input and no environment variable to forget.
const (
	// ClassARM64 is every arm64 host. There is one arm64 class because arm64
	// exposes no self-resolvable split this package needs; see machineClass.
	ClassARM64 = "arm64"

	// ClassAMD64AVX512 is an amd64 host whose silicon and OS offer AVX-512F, so
	// both amd64 tiers can execute and dispatch has a preference to make.
	ClassAMD64AVX512 = "amd64-avx512-capable"

	// ClassAMD64NoAVX512 is an amd64 host without AVX-512F: AVX2 is the only
	// assembly tier that can run, and it is the tier the large installed base of
	// such machines actually executes.
	ClassAMD64NoAVX512 = "amd64-no-avx512"
)

// machineClass resolves the running host to the class pinTable is keyed by, or
// "" when this architecture has no class defined.
//
// WHAT THIS DELIBERATELY DOES NOT CLAIM. A capability class is COARSER than a
// silicon class, and pretending otherwise would be the same lie as an
// unresolvable key. An Intel client part with AVX-512 fused off resolves to
// ClassAMD64NoAVX512 and will read floors measured on an AMD Milan; a Graviton
// resolves to ClassARM64 and reads floors measured on an Apple M4 Max. Every
// pin's Machine field names the silicon its numbers came from, so the coarseness
// is visible at the point of use instead of buried here. When a part is found
// that the class it resolves to genuinely misprices, the fix is a new class with
// its own resolution rule and its own rows — not a fudged tolerance.
//
// It gates on HasAVX512F, the SILICON fact, rather than on avx512Supported(),
// which is the narrower question of whether this build's AVX-512 kernel can run.
// The class describes the machine; tier support describes the kernel.
func machineClass() string {
	switch runtime.GOARCH {
	case "arm64":
		return ClassARM64
	case "amd64":
		if cpu.X86.HasAVX512F {
			return ClassAMD64AVX512
		}
		return ClassAMD64NoAVX512
	default:
		// No assembly tier exists here and no class has been measured. The floor
		// gate reports this as "nothing was checked" rather than borrowing.
		return ""
	}
}

// Pin is one pinned measurement: the ns/distance a tier achieved at a dim, on
// a named machine of a named class, in a named benchmark shape.
type Pin struct {
	// Class is the machine class these numbers were measured on and the ONLY
	// class they apply to. A host of a different class that finds no row here
	// gets no floor, never this one.
	Class string
	Tier  string
	Dim   int

	// TraverseNsPerDistance is the number from the traverse-shaped benchmark:
	// a 128 MiB corpus, 64 candidates per hop, and the next hop chosen from the
	// scores just computed. THIS IS THE NUMBER THAT MATTERS. The corpus does not
	// fit in cache and the hop chain is data-dependent, so it prices the memory
	// system and the branch predictor alongside the arithmetic — which is what a
	// real graph traversal does.
	TraverseNsPerDistance float64

	// MicroNsPerDistance is the cache-hot single-pair number. Useful for
	// isolating arithmetic throughput from memory behavior, and misleading on
	// its own: it flatters every kernel by removing the cache misses that
	// dominate a real traversal.
	MicroNsPerDistance float64

	// SpreadRatio is max/min across the harvest runs for this cell — the cell's
	// own measured noise, kept rather than discarded.
	//
	// A SINGLE GLOBAL TOLERANCE IS WRONG BECAUSE CELLS ARE NOT EQUALLY NOISY.
	// One measured cell spread 1.67x between its best and worst harvest run
	// while the arm64 assembly cells sat inside 1.08x. A 2.0 that is barely
	// survivable for the first is, for the second, a gate that waves through a
	// 90% regression. Recording the spread is what lets Tolerance be derived
	// instead of picked.
	SpreadRatio float64

	// Tolerance is the per-cell regression multiplier: the gate fails when a
	// measurement exceeds TraverseNsPerDistance * Tolerance. Derived from
	// SpreadRatio by toleranceFor at harvest time.
	Tolerance float64

	// Machine names the silicon the numbers came from. A pin without it is not
	// a measurement, it is a rumor.
	Machine string

	// Unmeasured marks a slot that is deliberately empty and awaiting a run on
	// the relevant hardware. The floor test SKIPS these LOUDLY, naming them, so
	// an empty table cannot be mistaken for a passing one.
	Unmeasured bool
}

// toleranceFor derives a cell's regression multiplier from the spread its own
// harvest measured.
//
// THE RULE: the cell's observed max/min, times a safety factor, clamped. A cell
// whose five harvest runs agreed within 8% gets a tight gate that can see a 35%
// regression; a cell that genuinely swings 1.67x gets a loose one, because a
// gate tighter than the cell's own noise is a gate that will be switched off
// within a month. Both come out of the same rule rather than out of a judgement
// call per row.
//
// This replaced a single global 2.0. That constant was simultaneously too loose
// for every quiet cell — it passed a 90% regression on cells that reproduce to
// 8% — and, on the noisiest cell measured, only 1.2x of headroom above the
// cell's own spread.
func toleranceFor(spreadRatio float64) float64 {
	const (
		safety = 1.25
		lo     = 1.30
		hi     = 2.50
	)
	t := spreadRatio * safety
	if t < lo {
		t = lo
	}
	if t > hi {
		t = hi
	}
	return t
}

// staleFloorFactor is the LOWER half of the two-sided gate: a measurement at or
// below this multiple of the pin fails as a STALE FLOOR.
//
// A floor far above what the tier actually achieves is not a conservative floor,
// it is an absent one. The arm64 NEON dim 256 pin sat at 37.5 while the tier ran
// at 14.8 — a 0.39x ratio — which meant the kernel could have HALVED its
// throughput and still passed. Nothing detected that for the life of the pin,
// because a one-sided gate cannot: every run looked wonderfully fast.
//
// 0.5 — a measurement twice as fast as its floor. That is far outside any
// machine-to-machine variation a legitimate pin should show, so firing means the
// pin was set on different hardware, under a different protocol, or against a
// slower kernel that has since been improved. All three demand a re-harvest, and
// the failure message says so.
const staleFloorFactor = 0.5
