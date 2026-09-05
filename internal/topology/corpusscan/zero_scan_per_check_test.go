// SPDX-License-Identifier: Apache-2.0

// zero_scan_per_check_test.go — the vacuous-pass closer under PER-CHECK scope.
//
// The refusal exists so a mistyped or over-narrow path_prefix cannot render as a
// clean corpus. It used to reason run-wide, on the documented ground that every
// ast check walked the SAME scope so one zero was every zero. A check that
// declares its class lives in test files walks wider than its neighbors, which
// makes that sentence false — and false in the dangerous direction: ONE widened
// check reaching a file cleared the guard for every narrow check that opened
// nothing, and those were folded into a CLEAN verdict.

package corpusscan

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// testOnlyPrefixRepo holds one test file with a matching site and one non-test
// file OUTSIDE the prefix, so a walk narrowed to the test file scans zero with
// the filter on and one with it off.
func testOnlyPrefixRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, map[string]string{
		"lib/lib_test.go": deferCloseBad,
		"other/other.go":  deferCloseBad,
	})
}

// mustRefuseRun drives the analyzer through the package's existing runScanErr
// and requires the refusal, so every row below asserts on a real message.
func mustRefuseRun(t *testing.T, req foundation.Request) error {
	t.Helper()
	err := runScanErr(req)
	if err == nil {
		t.Fatalf("expected the run to be refused, and a run that answers here is the vacuous green this guard exists to prevent")
	}
	return err
}

// TestCorpusScan_ZeroScanNamesTheTestFilterNotThePrefix is the wording leg. The
// refusal reasons about the walk that RAN: with the filter on, the prefix did
// reach the file and the filter is what removed it, so blaming the prefix sends
// a caller to widen a scope that was already correct.
func TestCorpusScan_ZeroScanNamesTheTestFilterNotThePrefix(t *testing.T) {
	root := testOnlyPrefixRepo(t)
	gc := astCorpus(checkNode("chk-1", "no naked defer Close", "handle it",
		astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))
	req := scanRequest(gc, "repo", root)
	req.PathPrefix = "lib"

	t.Run("filter on: refused, and the cause is the filter", func(t *testing.T) {
		err := mustRefuseRun(t, req)
		if !strings.Contains(err.Error(), "include_tests") {
			t.Errorf("the remedy is the flag; the message must name it, got %q", err)
		}
		if strings.Contains(err.Error(), "package_prefixes") {
			t.Errorf("the prefix reached the file and is not the cause, got %q", err)
		}
		if !strings.Contains(err.Error(), "chk-1") {
			t.Errorf("and the refusal names the check that scanned nothing, got %q", err)
		}
	})

	t.Run("filter off: the same prefix is not refused at all", func(t *testing.T) {
		// THE KNOWN-POSITIVE CONTROL for the row above: the prefix is correct,
		// which is exactly why blaming it was wrong.
		got := siteFiles(runScan(t, withIncludeTests(req, "true")))
		if len(got) != 1 || got[0] != "lib/lib_test.go" {
			t.Fatalf("with tests included the same prefix scans the file, got %v", got)
		}
	})

	t.Run("a genuinely wrong prefix still names the prefix", func(t *testing.T) {
		// THE FALSIFYING CONTROL: the fix must not swing to always-blame-the-
		// filter. This prefix matches nothing at all.
		wrong := scanRequest(gc, "repo", root)
		wrong.PathPrefix = "nosuch"
		err := mustRefuseRun(t, wrong)
		if !strings.Contains(err.Error(), "package_prefixes") {
			t.Errorf("a prefix that reached nothing IS the cause here, got %q", err)
		}
		if strings.Contains(err.Error(), "include_tests") {
			t.Errorf("and no test file was dropped, so the filter must not be named, got %q", err)
		}
	})
}

// TestCorpusScan_MixedScopeZeroIsPerCheck is the S12 leg, and the one a test
// that only re-ran the all-undeclared case would never exercise.
func TestCorpusScan_MixedScopeZeroIsPerCheck(t *testing.T) {
	root := testOnlyPrefixRepo(t)
	gc := astCorpus(
		checkNode("chk-declared", "declared", "this class lives in tests",
			declaredAstCheck("defer $X.Close()")),
		checkNode("chk-plain", "plain", "this one does not",
			astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
	)
	req := scanRequest(gc, "repo", root)
	req.PathPrefix = "lib"

	// The declared check reaches the file and flags it; the undeclared check
	// opens nothing. A run-level guard sees "something scanned" and reports the
	// undeclared check's silence as clean. It is not clean: that check answered
	// nothing.
	err := mustRefuseRun(t, req)
	if !strings.Contains(err.Error(), "chk-plain") {
		t.Errorf("the refusal must name the check that scanned nothing, got %q", err)
	}
	if strings.Contains(err.Error(), "chk-declared") {
		t.Errorf("and not the check that did scan, got %q", err)
	}

	// CONTROL: with the run-wide knob every check reaches the file, nothing
	// scanned zero, and the same request is answered rather than refused.
	got := siteFiles(runScan(t, withIncludeTests(req, "true")))
	if len(got) != 2 {
		t.Fatalf("control: with both checks widened the run answers, got %v", got)
	}
}

// THE GRAPH-ONLY EXEMPTION IS NOT RE-TESTED HERE. It already has its own
// regression, TestCorpusScan_GraphOnlyCorpusUnderAPrefixIsNotRefusedForOpeningNoFile
// in exec_graph_test.go, with a discriminating ast control in the same run; the
// per-check rule keys on walks, and a graph check records none, so that test
// covers this change unmodified.
