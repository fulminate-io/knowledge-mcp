// SPDX-License-Identifier: Apache-2.0

//go:build (arm64 || amd64) && !veckernel_noasm

package veckernel

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPrefetchFastPathStaysInlinable asserts the COMPILER'S OWN DECISION about
// prefetchTargets, by asking it.
//
// WHY A COMPILER ASSERTION RATHER THAN A BENCHMARK. The fast path runs once per
// four-row group inside the gather's hot loop, so losing its inlining costs a
// real call per group — measured at about 4% at dim 256. But that is far below
// the run-to-run spread on a machine carrying other work (this package's own pin
// table records spreads to 1.76x on a loaded host), so a timing gate cannot see
// it reliably. The inline decision, by contrast, is DETERMINISTIC: the compiler
// either fits the function under its budget or it does not, and it will say
// which. Gating the cause instead of the symptom is what makes this catchable.
//
// IT IS ALSO INVISIBLE IN A DIFF, which is the other half of why it needs a
// gate. The regression that prompted this was a few lines of schedule handling
// added to the fast path: entirely reasonable-looking, and it pushed the cost
// from 61 to 147 against a budget of 80.
func TestPrefetchFastPathStaysInlinable(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "build", "-gcflags=-m=2", "./").CombinedOutput()
	if err != nil {
		t.Fatalf("go build -gcflags=-m=2 failed: %v\n%s", err, out)
	}
	text := string(out)

	// CONTROL, same run: the compiler really did report inline decisions for this
	// package. Without it, a build whose diagnostics changed shape — or which
	// produced no output at all — would satisfy the negative assertion below by
	// saying nothing.
	if !strings.Contains(text, "can inline") {
		t.Fatalf("the compiler emitted no inline decisions at all, so this gate measured nothing:\n%s", text)
	}

	if strings.Contains(text, "cannot inline prefetchTargets:") {
		t.Errorf("prefetchTargets IS NO LONGER INLINABLE, so the gather now pays a call per "+
			"four-row group. Move whatever was added to it into prefetchTargetsScheduled and "+
			"branch in the gathers on pfScheduleIsDefault, which is what keeps the fast path "+
			"under the inliner's budget.\ncompiler said: %s", inlineLineFor(text, "prefetchTargets"))
	}
	if !strings.Contains(text, "can inline prefetchTargets ") {
		t.Errorf("the compiler did not report prefetchTargets as inlinable; it may have been "+
			"renamed or removed, in which case this gate is no longer guarding anything.\n%s",
			inlineLineFor(text, "prefetchTargets"))
	}

	// THE GENERAL PATH IS NOT REQUIRED TO INLINE and asserting that it does not
	// would be brittle, so nothing is claimed about it here. What matters is only
	// that the SHIPPED schedule never reaches it — the gathers branch on a
	// loop-invariant bool, and prefetch_schedule_test.go covers that dispatch.
}

// inlineLineFor extracts the compiler's line about one symbol, so a failure
// quotes the decision rather than making the reader re-run the build.
func inlineLineFor(text, sym string) string {
	for ln := range strings.SplitSeq(text, "\n") {
		if strings.Contains(ln, "inline "+sym) || strings.Contains(ln, "inline "+sym+":") {
			return strings.TrimSpace(ln)
		}
	}
	return "(no inline decision found for " + sym + ")"
}
