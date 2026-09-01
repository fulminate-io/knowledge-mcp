// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/cpu"
)

// dispatch_test.go is TEST CLASS (d): the active tier is asserted, never
// assumed, and every tier is forceable with a loud, typed reason when it cannot
// run here.
//
// THESE TESTS MUTATE PACKAGE STATE (they call reselect after t.Setenv) and
// therefore must never call t.Parallel.

// defaultTier is what dispatch prefers with NO pin applied.
//
// The tests below assert on this rather than on Kernel(), because the suite is
// meant to be run under an ambient VECKERNEL_FORCE — that is how each tier gets
// graded on a machine that would not otherwise select it, and the README tells
// operators to do exactly that. A test that asserted Kernel() directly would
// fail whenever someone followed those instructions.
func defaultTier(t *testing.T) string {
	t.Helper()
	a, err := selectArm("", tiers)
	if err != nil {
		t.Fatalf("default tier selection failed: %v", err)
	}
	return a.name
}

// TestActiveTierIsTheExpectedOneForThisArch is the assertion that makes a silent
// fallback impossible to ship. It hard-codes what each build MUST prefer.
func TestActiveTierIsTheExpectedOneForThisArch(t *testing.T) {
	got := defaultTier(t)

	// The veckernel_noasm tag removes assembly from the binary on EVERY
	// architecture, so it decides the expectation before GOARCH does.
	if !asmCompiledIn {
		if got != TierReference {
			t.Fatalf("built with veckernel_noasm, so the reference must be preferred; got %q "+
				"(tiers: %+v)", got, Tiers())
		}
		if ASMAvailable() {
			t.Fatal("built with veckernel_noasm, so ASMAvailable() must be false")
		}
		for _, ts := range Tiers() {
			if ts.Name != TierReference {
				t.Errorf("built with veckernel_noasm, so no assembly tier should be compiled in; "+
					"found %q", ts.Name)
			}
		}
		t.Logf("veckernel_noasm build on %s: preferred tier=%q", runtime.GOARCH, got)
		return
	}

	switch runtime.GOARCH {
	case "arm64":
		if got != TierNEON {
			t.Fatalf("on arm64 the NEON tier must be preferred, got %q (tiers: %+v)", got, Tiers())
		}
		if !ASMAvailable() {
			t.Fatal("on arm64 ASMAvailable() must be true")
		}
	case "amd64":
		// THE EXPECTATION COMES FROM THE HARDWARE, not from the dispatcher.
		// x/sys/cpu is a different package reading CPUID for itself, so a typo
		// in either gate diverges here instead of agreeing with itself.
		want, wantASM := amd64ExpectedTier()
		if got != want {
			t.Fatalf("on amd64 with AVX2=%v AVX512F=%v FMA=%v the preferred tier must be %q; "+
				"got %q (tiers: %+v)", cpu.X86.HasAVX2, cpu.X86.HasAVX512F, cpu.X86.HasFMA,
				want, got, Tiers())
		}
		if ASMAvailable() != wantASM {
			t.Fatalf("on amd64 ASMAvailable() must be %v for this CPU's feature set; got %v",
				wantASM, ASMAvailable())
		}
	default:
		if got != TierReference {
			t.Fatalf("on %s no assembly tier exists, so the reference must be preferred; got %q",
				runtime.GOARCH, got)
		}
	}

	// With no ambient pin, what dispatch prefers is what actually runs.
	if force := os.Getenv(ForceEnv); force == "" {
		if Kernel() != got {
			t.Errorf("no %s is set, so the active tier must be the preferred one %q; got %q",
				ForceEnv, got, Kernel())
		}
	} else {
		t.Logf("%s=%q is pinning the active tier to %q; graded the PREFERENCE (%q) instead",
			ForceEnv, force, Kernel(), got)
	}
	t.Logf("GOARCH=%s preferred=%q active=%q tiers=%+v", runtime.GOARCH, got, Kernel(), Tiers())
}

// TestTierCensus names every compiled tier and states, loudly, which ones this
// host cannot execute and why.
//
// A tier this silicon cannot run is skipped by the graders. If that skip were
// silent, a whole tier could be ungraded on every machine in the fleet while
// every suite reported green — the exact hole the forced-tier seam exists to
// close. This test is the recorder that makes such an absence visible.
func TestTierCensus(t *testing.T) {
	all := Tiers()
	if len(all) == 0 {
		t.Fatal("no tiers compiled in")
	}
	graded := 0
	for _, ts := range all {
		switch {
		case ts.Supported:
			graded++
			t.Logf("TIER %-14s supported active=%v — GRADED by this run", ts.Name, ts.Active)
		default:
			t.Logf("TIER %-14s NOT SUPPORTED HERE, SKIPPED AND UNGRADED BY THIS RUN — reason: %s",
				ts.Name, ts.Reason)
		}
	}
	if graded == 0 {
		t.Fatal("no tier is supported on this host — the reference tier must always be supported")
	}
	if graded != len(testArms()) {
		t.Fatalf("census disagrees with the grader table: %d supported tiers but %d graded",
			graded, len(testArms()))
	}
}

// TestForceEnvSelectsTheReference proves the force seam actually reaches the
// dispatcher — not merely that selectArm is a correct pure function, but that
// the environment variable changes what DotF32 runs.
func TestForceEnvSelectsTheReference(t *testing.T) {
	// The PREFERRED tier, not the currently-active one: an ambient
	// VECKERNEL_FORCE may already have pinned the reference, and comparing
	// against that would make the known-positive below fire spuriously.
	before := defaultTier(t)

	// ORDER IS LOAD-BEARING. Cleanups run last-registered-first, and t.Setenv
	// registers its own cleanup to restore the variable. Registering reselect
	// BEFORE t.Setenv therefore makes reselect run AFTER the restore, so the
	// dispatcher re-reads the ORIGINAL environment and the pin does not leak
	// into the rest of the suite. Registering it after would re-read the still-
	// forced value and leave every later test running the reference.
	t.Cleanup(reselect)
	t.Setenv(ForceEnv, TierReference)
	reselect()

	if Kernel() != TierReference {
		t.Fatalf("%s=%s did not take effect: active tier is %q", ForceEnv, TierReference, Kernel())
	}

	// KNOWN POSITIVE for this assertion: on a host where the default tier is
	// NOT the reference, the force must have actually CHANGED something. Without
	// this, a dispatcher hard-wired to the reference would pass the test above.
	switch {
	case ASMAvailable() && before == TierReference:
		t.Fatal("assembly is available yet the default tier was already the reference — " +
			"the forcing assertion above would be vacuous")
	case !ASMAvailable():
		// Stated rather than left implicit: with only one tier there is nothing
		// to switch away from, so the assertion above proves the force seam
		// parses its input, not that it changes the dispatch.
		t.Logf("only one tier on this build (%s); the force assertion confirms the seam is read "+
			"but cannot demonstrate a tier CHANGE — that half is covered on multi-tier builds",
			Kernel())
	default:
		t.Logf("force changed the active tier from %q to %q", before, Kernel())
	}

	// And it must still compute correctly while forced.
	x, y := seededPair(42, 1024)
	if err := gradeAgainstOracle("forced-"+TierReference, DotF32, x, y); err != nil {
		t.Error(err)
	}
}

// TestForceEnvRestoresDefault is the other half: unsetting the pin returns the
// preferred tier. A force seam that cannot be released is a footgun in a
// long-lived process.
func TestForceEnvRestoresDefault(t *testing.T) {
	// Registered before any t.Setenv so it runs after every restore — see the
	// ordering note in TestForceEnvSelectsTheReference.
	t.Cleanup(reselect)

	t.Setenv(ForceEnv, TierReference)
	reselect()
	if Kernel() != TierReference {
		t.Fatalf("setup failed: %q active", Kernel())
	}

	t.Setenv(ForceEnv, "")
	reselect()

	want := TierReference
	if asmCompiledIn {
		switch runtime.GOARCH {
		case "arm64":
			want = TierNEON
		case "amd64":
			want, _ = amd64ExpectedTier()
		}
	}
	if Kernel() != want {
		t.Fatalf("after clearing %s the active tier should be %q, got %q", ForceEnv, want, Kernel())
	}
}

// TestReselectPanicNamesThePackageExactlyOnce guards the DEFECT AS IT WAS
// OBSERVED, on the path that produced it.
//
// The sibling assertion inside TestSelectArmVocabulary checks selectArm's
// errors. That is not this: the doubling happened in reselect, which wrapped a
// already-self-naming error in a second prefix, and an operator only ever sees
// the result through the init-time panic. So this drives reselect itself and
// reads the recovered panic value — the exact string a misconfigured process
// prints and the only diagnostic it gets.
//
// It forces a tier this binary does not contain, which is an UnbuiltTierError on
// every architecture: the amd64 tiers are absent on arm64 and the arm64 tier is
// absent on amd64, so the branch is reachable everywhere rather than only on the
// hardware that first exposed it.
func TestReselectPanicNamesThePackageExactlyOnce(t *testing.T) {
	// Registered BEFORE t.Setenv so it runs AFTER the env is restored — see the
	// ordering note in TestForceEnvSelectsTheReference. Without it the forced
	// value leaks into every later test in the package.
	t.Cleanup(reselect)

	foreign := TierAVX512
	if runtime.GOARCH == "amd64" {
		foreign = TierNEON
	}
	t.Setenv(ForceEnv, foreign)

	msg, panicked := recoverReselect()
	if !panicked {
		t.Fatalf("forcing %q on %s did not panic, so this test graded nothing. Either that tier "+
			"is now built here, or reselect stopped refusing unknown pins.", foreign, runtime.GOARCH)
	}
	if n := strings.Count(msg, "veckernel:"); n != 1 {
		t.Errorf("init panic names the package %d times, want exactly 1. This is the defect an "+
			"AMD Milan box surfaced by refusing a forced AVX-512 tier: %q", n, msg)
	}
	if !strings.Contains(msg, foreign) {
		t.Errorf("init panic must name the tier that was refused; got %q", msg)
	}
	t.Logf("init panic payload: %s", msg)
}

// recoverReselect calls reselect and returns the panic message, if any.
func recoverReselect() (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = fmt.Sprint(r), true
		}
	}()
	reselect()
	return "", false
}

// TestSelectArmVocabulary drives every branch of the resolver as a pure
// function, including the branches for tiers this machine does not have. That
// is the point of keeping it pure: the AVX-512-unsupported path is testable on
// an arm64 laptop.
func TestSelectArmVocabulary(t *testing.T) {
	noop := func(_, _ []float32) float32 { return 0 }
	noopG := func(_, _, _ []float32, _ int, _ []uint32) {}

	table := []arm{
		{name: "fast-tier", dot: noop, gather: noopG, supported: false, why: "CPU lacks the feature bit"},
		{name: "mid-tier", dot: noop, gather: noopG, supported: true},
		{name: TierReference, dot: noop, gather: noopG, supported: true},
	}

	t.Run("empty-picks-first-supported", func(t *testing.T) {
		got, err := selectArm("", table)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.name != "mid-tier" {
			t.Fatalf("got %q, want mid-tier (fast-tier is unsupported and must be skipped)", got.name)
		}
	})

	t.Run("whitespace-is-trimmed", func(t *testing.T) {
		got, err := selectArm("  mid-tier  ", table)
		if err != nil || got.name != "mid-tier" {
			t.Fatalf("got %q, %v", got.name, err)
		}
	})

	t.Run("unsupported-tier-is-typed-and-explains", func(t *testing.T) {
		_, err := selectArm("fast-tier", table)
		var ute *UnsupportedTierError
		if !errors.As(err, &ute) {
			t.Fatalf("want *UnsupportedTierError, got %T: %v", err, err)
		}
		if ute.Tier != "fast-tier" || ute.Reason == "" {
			t.Fatalf("error must name the tier and carry a reason, got %+v", ute)
		}
		// This is what lets a CI suite skip loudly rather than pass silently.
		t.Logf("skip-with-reason payload: %v", err)
	})

	t.Run("known-tier-absent-from-this-table-is-typed", func(t *testing.T) {
		_, err := selectArm(TierAVX512, table)
		var ube *UnbuiltTierError
		if !errors.As(err, &ube) {
			t.Fatalf("want *UnbuiltTierError for a known tier this table lacks, got %T: %v", err, err)
		}
		if ube.Tier != TierAVX512 {
			t.Fatalf("error must name the tier, got %+v", ube)
		}
		// This table HAS assembly tiers, just not the requested one, so the
		// reason must say "different architecture" rather than "no assembly".
		if !strings.Contains(ube.Reason, "different architecture") {
			t.Errorf("a table carrying other assembly tiers must blame the architecture; got %q", ube.Reason)
		}
		t.Logf("cross-architecture force payload: %v", err)
	})

	// KNOWN POSITIVE FOR THE OTHER BRANCH of unbuiltReason. Both branches are
	// reachable and they say different things; a branch nobody has driven is a
	// branch that might be keyed on a condition that can never hold.
	t.Run("known-tier-with-no-assembly-in-the-table-blames-the-build", func(t *testing.T) {
		refOnly := []arm{{name: TierReference, dot: noop, gather: noopG, supported: true}}
		_, err := selectArm(TierNEON, refOnly)
		var ube *UnbuiltTierError
		if !errors.As(err, &ube) {
			t.Fatalf("want *UnbuiltTierError, got %T: %v", err, err)
		}
		if !strings.Contains(ube.Reason, "no assembly tier at all") {
			t.Errorf("a reference-only table must blame the build, not an architecture; got %q", ube.Reason)
		}
		if !strings.Contains(ube.Reason, "veckernel_noasm") {
			t.Errorf("the reason must name the opt-out tag an operator would have set; got %q", ube.Reason)
		}
	})

	// THE SAME BRANCH ON THE LIVE TABLE. The two subtests above drive the
	// resolver as a pure function; this one proves the tier vocabulary of the
	// ACTUAL build routes a foreign-architecture tier to the typed error rather
	// than to "no such tier". Before both architectures had kernels this case
	// could not exist, and the reference-only fallthrough would have reported a
	// real tier name as a typo.
	t.Run("foreign-architecture-tier-on-the-live-table", func(t *testing.T) {
		foreign := TierAVX512
		if runtime.GOARCH == "amd64" {
			foreign = TierNEON
		}
		_, err := selectArm(foreign, tiers)
		if _, ok := errors.AsType[*UnbuiltTierError](err); !ok {
			t.Fatalf("forcing %q on %s must be an *UnbuiltTierError, got %T: %v",
				foreign, runtime.GOARCH, err, err)
		}
		t.Logf("live-table foreign force on %s: %v", runtime.GOARCH, err)
	})

	t.Run("unknown-name-errors-and-lists-the-vocabulary", func(t *testing.T) {
		_, err := selectArm("avx512", table) // plausible misspelling of TierAVX512
		if err == nil {
			t.Fatal("a name this package does not know must be an error, never a silent default")
		}
		var ube *UnbuiltTierError
		var ute *UnsupportedTierError
		if errors.As(err, &ube) || errors.As(err, &ute) {
			t.Fatalf("a typo must not be reported as an unbuilt or unsupported tier: %v", err)
		}
		for _, want := range []string{"avx512", "mid-tier", TierReference} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must quote the bad value and list the vocabulary; missing %q in: %v", want, err)
			}
		}
	})

	// REGRESSION TEST FOR A DEFECT FOUND ON HARDWARE. reselect used to wrap
	// selectArm's error as "veckernel: " + err.Error(), while the two exported
	// typed errors already name the package themselves — so the first run that
	// ever reached this path on real silicon (an AMD Milan box refusing a forced
	// AVX-512 tier) panicked with "veckernel: veckernel: tier ...".
	//
	// It is asserted over EVERY error branch rather than the one that was wrong,
	// because the fix moved the prefix from one place to five and the failure
	// mode of that move is a branch that now names the package zero times. The
	// panic text is the whole diagnostic an operator gets at init; it must name
	// the package exactly once, never twice and never not at all.
	t.Run("every-error-names-the-package-exactly-once", func(t *testing.T) {
		hollow := []arm{{name: "hollow", supported: true}}
		unsupportedOnly := []arm{{name: "only", dot: noop, gather: noopG, supported: false, why: "no feature bit"}}

		cases := []struct {
			name  string
			force string
			table []arm
		}{
			{"empty-table", "", nil},
			{"no-supported-tier", "", unsupportedOnly},
			{"unsupported-tier", "fast-tier", table},
			{"unbuilt-tier", TierAVX512, table},
			{"unknown-name", "avx512", table},
			{"hollow-tier-by-default", "", hollow},
			{"hollow-tier-by-name", "hollow", hollow},
		}
		for _, tc := range cases {
			_, err := selectArm(tc.force, tc.table)
			if err == nil {
				t.Errorf("%s: expected an error, got nil — this branch is no longer reachable "+
					"and the assertion below proved nothing about it", tc.name)
				continue
			}
			if n := strings.Count(err.Error(), "veckernel:"); n != 1 {
				t.Errorf("%s: error names the package %d times, want exactly 1: %q",
					tc.name, n, err.Error())
			}
		}
	})

	t.Run("empty-table-is-a-build-defect", func(t *testing.T) {
		if _, err := selectArm("", nil); err == nil {
			t.Fatal("an empty tier table must error, not return a zero arm")
		}
	})

	t.Run("supported-tier-with-no-implementation-is-refused", func(t *testing.T) {
		bad := []arm{{name: "hollow", supported: true}}
		if _, err := selectArm("", bad); err == nil {
			t.Fatal("a tier claiming support with nil function pointers must be refused, not dispatched to")
		}
	})
}
