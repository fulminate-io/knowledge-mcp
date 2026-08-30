// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"os"
	"strings"
)

// dispatch.go owns which kernel runs and how a caller finds out.
//
// THE WHOLE DESIGN IS ONE CLAIM: the tier that ran is the tier that is
// reported, and a tier that could not run says so with a reason. A kernel
// library that dispatches silently is indistinguishable from one that has
// quietly declined its SIMD path, and a declined SIMD path returns correct,
// slow results forever without a single test going red. The shoot-out that
// preceded this package found exactly that in a third-party library — its
// batched kernel silently fell back to scalar at the widest contemplated
// dimension, at precisely this project's neighbor-count — and it was only
// visible because the harness asserted the dispatch flag instead of trusting it.

// Tier names. These strings are the package's vocabulary: Kernel() returns one,
// ForceEnv accepts one, and the pinned performance floors are keyed by one.
const (
	// TierReference is the portable four-way-unrolled Go kernel. Always
	// present, always supported, always last in preference order.
	TierReference = "go-unroll4"

	// TierNEON is the hand-written AArch64 Advanced SIMD kernel.
	TierNEON = "arm64-neon"

	// TierAVX512 and TierAVX2 are the two amd64 tiers, both avo-generated and
	// both compiled into any amd64 build without the veckernel_noasm tag.
	//
	// THEY ARE TWO FIRST-CLASS TIERS, NOT A LADDER WITH AVX-512 ASSUMED TO WIN.
	// See dispatchPolicy in pins.go; the order they are returned in is set by
	// amd64PreferAVX512 in kernel_amd64.go, from a measurement.
	TierAVX512 = "amd64-avx512"
	TierAVX2   = "amd64-avx2"
)

// knownTiers is EVERY tier name in this package's vocabulary, with what each
// one is, regardless of which build it lands in.
//
// Naming a tier that this build does not contain gets an error that says "not
// built into this binary" WITH the reason, rather than "no such tier". The two
// are different mistakes with different fixes: an operator running the amd64
// benchmark procedure on an arm64 laptop has picked the wrong machine, while an
// operator who typed "avx512" has picked the wrong string, and an error that
// conflates them sends both of them looking in the wrong place.
var knownTiers = map[string]string{
	TierReference: "the portable four-way-unrolled Go kernel, present in every build",
	TierNEON:      "the arm64 Advanced SIMD kernel, compiled only into arm64 builds",
	TierAVX512:    "the amd64 AVX-512F kernel, compiled only into amd64 builds",
	TierAVX2:      "the amd64 AVX2/FMA kernel, compiled only into amd64 builds",
}

// ForceEnv names the environment variable that pins the tier.
//
// Unset or empty selects the preferred supported tier. Set to a tier name it
// pins that tier. Set to anything else the package PANICS at init naming the
// value and listing what this build offers.
//
// FORCING IS NOT A DEBUG CONVENIENCE, it is how the tiers get tested. A CI
// machine's capabilities are not a choice — an amd64 runner pool is inconsistent
// about whether AVX-512 is present, and modern Intel client silicon has AVX-512
// fused off entirely — so a suite that can only ever exercise whatever tier the
// host happened to pick leaves the others ungraded on every machine that lacks
// them. Every tier must be forceable, and forcing a tier the host cannot execute
// must fail with the HARDWARE REASON so the suite can skip it loudly instead of
// reporting a pass it never earned.
const ForceEnv = "VECKERNEL_FORCE"

// arm is one kernel tier: a name, the two entry points, and whether this CPU
// can actually execute it.
type arm struct {
	name   string
	dot    func(a, b []float32) float32
	gather func(dst, query, block []float32, dim int, ids []uint32)

	// supported is whether THIS CPU can execute this tier. A tier compiled into
	// the binary but unsupported by the running silicon stays in the table —
	// removing it would make "this CPU lacks AVX-512" and "this build has no
	// AVX-512 kernel" indistinguishable, and those need different fixes.
	supported bool
	// why explains !supported, in operator terms.
	why string
}

// TierStatus is the public view of one compiled tier.
type TierStatus struct {
	Name      string
	Supported bool
	// Reason is populated only when Supported is false.
	Reason string
	// Active is true for the tier Kernel() reports.
	Active bool
}

// UnsupportedTierError means the named tier IS compiled into this binary but
// the running CPU cannot execute it.
//
// It is a distinct type because callers must distinguish it from a typo: a test
// forcing AVX-512 on a runner without AVX-512 should SKIP LOUDLY WITH THIS
// REASON, whereas a test forcing "avx512" (wrong spelling) should fail. Folding
// both into one opaque error is how a whole tier ends up never graded on any
// machine while the suite stays green.
type UnsupportedTierError struct {
	Tier   string
	Reason string
}

func (e *UnsupportedTierError) Error() string {
	return fmt.Sprintf("veckernel: tier %q is compiled in but this CPU cannot execute it: %s", e.Tier, e.Reason)
}

// UnbuiltTierError means the named tier is a tier this package KNOWS ABOUT but
// this build does not contain — the wrong architecture, or a tier not yet
// written.
type UnbuiltTierError struct {
	Tier   string
	Reason string
}

func (e *UnbuiltTierError) Error() string {
	return fmt.Sprintf("veckernel: tier %q is not built into this binary: %s", e.Tier, e.Reason)
}

var (
	// active is the tier every exported call dispatches through; tiers is the
	// full compiled table, preferred first, reference last.
	//
	// Neither is safe to write concurrently with reads. The only writer after
	// init is the dispatch test, which re-invokes reselect after t.Setenv and
	// does not run in parallel.
	active arm
	tiers  []arm
)

func init() { reselect() }

// reselect rebuilds the tier table for this build and CPU, then applies ForceEnv.
func reselect() {
	tiers = append(asmArms(), arm{
		name:      TierReference,
		dot:       dotF32Unroll4,
		gather:    gatherUnroll4,
		supported: true,
	})

	chosen, err := selectArm(os.Getenv(ForceEnv), tiers)
	if err != nil {
		// NO PREFIX ADDED HERE. Every error selectArm returns already names this
		// package, including the two exported typed ones, whose Error() a caller
		// may also print on its own. Prefixing again produced
		// "veckernel: veckernel: tier ... cannot execute it" in the first run
		// that ever hit this path on real hardware — an AMD Milan box refusing a
		// forced AVX-512 tier. Cosmetic, but the panic text is the entire
		// diagnostic an operator gets at init, so it says the name once.
		panic(err.Error())
	}
	active = chosen
}

// selectArm resolves a ForceEnv value against a tier table.
//
// Kept a PURE function of its two inputs so the dispatch tests can drive every
// branch — including branches for tiers this machine does not have — without
// mutating process state or needing the hardware.
func selectArm(force string, table []arm) (arm, error) {
	if len(table) == 0 {
		return arm{}, fmt.Errorf("veckernel: no tiers compiled in (a build defect, not a configuration one)")
	}

	force = strings.TrimSpace(force)

	if force == "" {
		for _, t := range table {
			if t.supported {
				if t.dot == nil || t.gather == nil {
					return arm{}, fmt.Errorf("veckernel: tier %q claims support but has no implementation (build defect)", t.name)
				}
				return t, nil
			}
		}
		return arm{}, fmt.Errorf("veckernel: no supported tier among %s (the reference tier must always be supported)",
			strings.Join(tierNames(table), ", "))
	}

	for _, t := range table {
		if t.name != force {
			continue
		}
		if !t.supported {
			return arm{}, &UnsupportedTierError{Tier: t.name, Reason: t.why}
		}
		if t.dot == nil || t.gather == nil {
			return arm{}, fmt.Errorf("veckernel: tier %q claims support but has no implementation (build defect)", t.name)
		}
		return t, nil
	}

	if what, ok := knownTiers[force]; ok {
		return arm{}, &UnbuiltTierError{Tier: force, Reason: unbuiltReason(what, table)}
	}

	return arm{}, fmt.Errorf("veckernel: %s=%q names no tier this package knows; this build offers: %s",
		ForceEnv, force, strings.Join(tierNames(table), ", "))
}

// unbuiltReason explains why a tier this package knows about is missing from
// this build, DERIVED FROM THE TABLE rather than from a build tag.
//
// The two causes are distinguishable from the table alone. A table holding only
// the reference means this binary carries no assembly at all — the
// veckernel_noasm opt-out, or an architecture no kernel has been written for. A
// table holding some other assembly tier means this binary has assembly, just
// for a different architecture. Reading it off the table keeps selectArm a pure
// function of its two inputs, which is what lets the dispatch tests drive this
// branch for tiers the test machine does not have.
func unbuiltReason(what string, table []arm) string {
	var asm []string
	for _, t := range table {
		if t.name != TierReference {
			asm = append(asm, t.name)
		}
	}
	if len(asm) == 0 {
		return what + "; this binary carries no assembly tier at all — either it was built with " +
			"the " + noasmTag + " tag, or no kernel has been written for its architecture"
	}
	return what + "; this binary's assembly tiers are " + strings.Join(asm, ", ") +
		", so the requested one targets a different architecture"
}

// noasmTag is the compile-time opt-out that removes assembly from the binary on
// every architecture. Named once here because the error above and the package
// documentation both quote it.
const noasmTag = "veckernel_noasm"

func tierNames(table []arm) []string {
	out := make([]string, len(table))
	for i := range table {
		out[i] = table[i].name
	}
	return out
}

// Kernel returns the name of the tier that is actually running.
//
// Every test in this package asserts on it. That is the point: a test that
// grades "whatever dispatch picked" passes identically whether the assembly ran
// or quietly did not.
func Kernel() string { return active.name }

// Tiers returns every tier compiled into this binary, preferred first,
// reference last, each with whether this CPU can execute it and which one is
// active.
func Tiers() []TierStatus {
	out := make([]TierStatus, len(tiers))
	for i := range tiers {
		out[i] = TierStatus{
			Name:      tiers[i].name,
			Supported: tiers[i].supported,
			Active:    tiers[i].name == active.name,
		}
		// Reason is the explanation for an ABSENCE, so it is carried only when
		// there is an absence to explain. The arm table stores a why on every
		// tier because the string is a compile-time constant of that tier, but
		// surfacing it on a SUPPORTED tier reads as a live complaint about
		// hardware that is working — "CPU does not report NEON support" printed
		// beside Supported:true is worse than no message at all.
		if !tiers[i].supported {
			out[i].Reason = tiers[i].why
		}
	}
	return out
}

// ASMAvailable reports whether an architecture-specific assembly tier is both
// compiled in and executable here. False means Kernel() is TierReference.
func ASMAvailable() bool {
	for _, t := range tiers {
		if t.name != TierReference && t.supported {
			return true
		}
	}
	return false
}
