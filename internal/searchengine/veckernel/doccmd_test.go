// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	_ "embed"
	"strings"
	"testing"
)

// readmeSrc is this package's README, EMBEDDED rather than read from disk.
//
// Same reason as genasm_test.go: these binaries are cross-compiled and shipped
// to a benchmark box that has no checkout, and a file-reading version failed
// there with "cannot read README.md: no such file or directory" — turning a
// documentation gate into a red correctness run on the one machine whose
// architecture the round existed to validate. Embedded, the bytes travel with
// the binary and the check is identical everywhere.
//
//go:embed README.md
var readmeSrc string

// doccmd_test.go makes the README's TIMING COMMANDS testable.
//
// WHY A TEST READS DOCUMENTATION. Go caches test results, and a cached result is
// replayed without executing anything. A timing gate invoked without `-count=1`
// therefore reports numbers from a run that did not happen — four consecutive
// invocations were observed returning byte-identical output down to the 0.1 ns,
// because only the first executed.
//
// THE TEST CANNOT DETECT THIS ABOUT ITSELF, which is the whole difficulty and
// the reason this gate lives on the documentation instead. A cached run does not
// execute, so there is no code inside it to notice that it was skipped; any
// in-process check would be part of the very thing that got replayed. The defense
// has to sit at the call site, and the call sites that matter are the ones this
// README hands to an operator.
//
// This is the sanctioned narrow use of -count=1: timing gates, where caching
// silently fabricates a result. It is not a license to sprinkle -count=1 across
// ordinary tests.

// timingCommandMarkers are the env prefixes that make a command a TIMING
// command. Anything invoking the package's measurement paths must defeat the
// cache; ordinary correctness commands in the README deliberately need not.
var timingCommandMarkers = []string{"VECKERNEL_PERF=1", "VECKERNEL_PERF_HARVEST=1"}

// wantTimingCommands is how many documented timing invocations the README is
// expected to contain.
//
// A FIXTURE-DERIVED COUNT, not one read back out of the scan. Without it a
// README rewrite that deleted every timing command — or a marker string that
// stopped matching — would leave this test iterating over nothing and reporting
// a serene pass, which is the same vacuity the -count=1 rule exists to prevent.
const wantTimingCommands = 2

func TestDocumentedTimingCommandsDisableTheTestCache(t *testing.T) {
	if strings.TrimSpace(readmeSrc) == "" {
		t.Fatal("the embedded README is empty; this gate would pass having read nothing")
	}

	found := 0
	for line := range strings.SplitSeq(readmeSrc, "\n") {
		if !isTimingCommand(line) {
			continue
		}
		found++
		if !strings.Contains(line, "-count=1") {
			t.Errorf("documented timing command lacks -count=1, so an operator following it "+
				"can be handed a cached result from a run that never executed:\n    %s",
				strings.TrimSpace(line))
		}
	}

	if found != wantTimingCommands {
		t.Fatalf("found %d documented timing command(s), expected %d. Either the README lost "+
			"one, or the markers %v no longer match how they are written — and in both cases "+
			"this gate just checked less than it claims to.",
			found, wantTimingCommands, timingCommandMarkers)
	}
	t.Logf("%d documented timing command(s) verified to disable the test cache", found)
}

// isTimingCommand reports whether a README line invokes `go test` under one of
// the measurement env gates.
func isTimingCommand(line string) bool {
	if !strings.Contains(line, "go test") {
		return false
	}
	for _, m := range timingCommandMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}
