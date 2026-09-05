// SPDX-License-Identifier: Apache-2.0

// include_tests_test.go — the two ways a corpus scan reaches test files, and the
// accounting that makes a run which reached them distinguishable from one that
// did not.
//
// EVERY ROW DRIVES THE REAL ANALYZER OVER A REAL TREE. The corpus read is the
// package's scripted caller; the walk, the contract parse and the fold are the
// production ones, because the behavior under test is entirely on that side.
// A test that hand-built a WalkStats or a Check would assert nothing about the
// path a caller actually takes.

package corpusscan

import (
	"context"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// splitRepo holds ONE instance of the pattern in a non-test file and ONE in a
// test file, so every count below is attributable: 1 means the default walk, 2
// means a widened one, and 0 means the check did not run.
func splitRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, map[string]string{
		"lib/lib.go":      deferCloseBad,
		"lib/lib_test.go": deferCloseBad,
		"lib/clean.go":    knownNegative,
	})
}

// withIncludeTests returns req carrying the run-wide knob at the given value.
func withIncludeTests(req foundation.Request, value string) foundation.Request {
	if req.Extra == nil {
		req.Extra = map[string]string{}
	}
	req.Extra[ExtraKeyIncludeTests] = value
	return req
}

// declaredAstCheck is an ast check carrying the per-check declaration.
func declaredAstCheck(pattern string) map[string]string {
	md := astCheckMeta(pattern, "warning", "fx-bad", "fx-good")
	md[corpus.MetaAppliesToTests] = "true"
	return md
}

// siteFiles lists the files the match findings flagged, in finding order.
func siteFiles(findings []foundation.Finding) []string {
	out := []string{}
	for _, f := range matchFindings(findings) {
		out = append(out, f.Metadata[MetaKeyFile])
	}
	return out
}

// TestCorpusScan_IncludeTestsKnobWidensTheWalk is R1's mechanism: the run-wide
// knob reaches the walk, and its absence leaves today's result untouched.
func TestCorpusScan_IncludeTestsKnobWidensTheWalk(t *testing.T) {
	root := splitRepo(t)
	gc := astCorpus(checkNode("chk-1", "no naked defer Close", "handle the error Close returns",
		astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	t.Run("absent: the test file is unreachable", func(t *testing.T) {
		got := siteFiles(runScan(t, scanRequest(gc, "repo", root)))
		if len(got) != 1 || got[0] != "lib/lib.go" {
			t.Fatalf("the default walk flags the non-test site alone, got %v", got)
		}
	})

	t.Run("explicit false: identical to absent", func(t *testing.T) {
		got := siteFiles(runScan(t, withIncludeTests(scanRequest(gc, "repo", root), "false")))
		if len(got) != 1 || got[0] != "lib/lib.go" {
			t.Fatalf("an explicit false is the documented default, got %v", got)
		}
	})

	t.Run("true: the test-file instance is reached", func(t *testing.T) {
		got := siteFiles(runScan(t, withIncludeTests(scanRequest(gc, "repo", root), "true")))
		if len(got) != 2 {
			t.Fatalf("the widened walk flags both instances, got %v", got)
		}
		found := false
		for _, f := range got {
			if f == "lib/lib_test.go" {
				found = true
			}
		}
		if !found {
			t.Errorf("the site the knob exists to reach is missing from %v", got)
		}
	})
}

// TestCorpusScan_AppliesToTestsWidensThatCheckAlone is R2's headline: the
// declaration is PER CHECK. The undeclared sibling in the same run is the
// falsifying control — a knob applied run-wide instead would flag both.
func TestCorpusScan_AppliesToTestsWidensThatCheckAlone(t *testing.T) {
	root := splitRepo(t)
	gc := astCorpus(
		checkNode("chk-declared", "declared", "this class lives in tests",
			declaredAstCheck("defer $X.Close()")),
		checkNode("chk-plain", "plain", "this one does not",
			astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
	)

	byCheck := map[string][]string{}
	for _, f := range matchFindings(runScan(t, scanRequest(gc, "repo", root))) {
		id := f.Metadata[MetaKeyCheckID]
		byCheck[id] = append(byCheck[id], f.Metadata[MetaKeyFile])
	}
	if len(byCheck["chk-declared"]) != 2 {
		t.Errorf("the declared check walks test files with no run-wide knob, got %v", byCheck["chk-declared"])
	}
	if want := []string{"lib/lib.go"}; len(byCheck["chk-plain"]) != 1 || byCheck["chk-plain"][0] != want[0] {
		t.Errorf("the undeclared check in the SAME run must be untouched, got %v", byCheck["chk-plain"])
	}
}

// TestCorpusScan_TestFilesScannedDisclosure is R3's counter proven ALIVE through
// this package's own execute path: the walk's number reaches a finding a
// consumer can fold, rather than being computed and dropped where the stats are.
func TestCorpusScan_TestFilesScannedDisclosure(t *testing.T) {
	root := splitRepo(t)
	gc := astCorpus(checkNode("chk-1", "no naked defer Close", "handle it",
		astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	t.Run("knob off: no disclosure and the fold reports zero", func(t *testing.T) {
		findings := runScan(t, scanRequest(gc, "repo", root))
		if got := findingsByTitlePrefix(findings, DisclosureTitleTestFiles); len(got) != 0 {
			t.Fatalf("a run that reached no test file discloses nothing, got %+v", got)
		}
		v := ClassifyRun(findings)
		if v.TestFilesScanned != 0 {
			t.Errorf("TestFilesScanned = %d, want 0", v.TestFilesScanned)
		}
		// THE FALSIFYING LEG: the five existing counters and the verdict are
		// what they were before this feature existed.
		if v.SitesFlagged != 1 || v.ChecksExecuted != 1 || v.ChecksRefused != 0 || v.LLMOnlyNotExecuted != 0 || v.Truncated {
			t.Errorf("a knob-off run's existing counters must be untouched, got %+v", v)
		}
	})

	t.Run("knob on: the count reaches the fold", func(t *testing.T) {
		findings := runScan(t, withIncludeTests(scanRequest(gc, "repo", root), "true"))
		lead := findingsByTitlePrefix(findings, DisclosureTitleTestFiles)
		if len(lead) != 1 {
			t.Fatalf("exactly one disclosure describes the run, got %d", len(lead))
		}
		if got := lead[0].Metrics[MetricTestFilesScanned]; got != 1 {
			t.Errorf("the disclosure carries the walk's own number, got %v", got)
		}
		// THE WORDING IS THE BODY A CALLER READS, and it is the whole of R3's
		// purpose: a reader tells a run that reached tests from one that did not
		// by reading this sentence. Neither the title nor the metric can see a
		// malformed template, so the string is asserted directly.
		if !strings.Contains(lead[0].Summary, "reached 1 go test file(s)") {
			t.Errorf("the disclosure must name the LANGUAGE whose test files were reached, got %q", lead[0].Summary)
		}
		if strings.Contains(lead[0].Summary, corpus.MetaAppliesToTests) {
			t.Errorf("the disclosure renders a metadata key where the language belongs, got %q", lead[0].Summary)
		}
		v := ClassifyRun(findings)
		if v.TestFilesScanned != 1 {
			t.Errorf("ClassifyRun must read the count into the verdict, got %d", v.TestFilesScanned)
		}
		// THE DEFAULT-ARM TRAP: a title ClassifyRun does not know is counted as
		// a flagged site, which turns a clean corpus FLAGGED.
		if v.SitesFlagged != 2 {
			t.Errorf("the disclosure is not a site: sites_flagged = %d, want the 2 real sites", v.SitesFlagged)
		}
	})
}

// TestCorpusScan_IncludeTestsRefusesBadInput is the bad-input leg. Each refusal
// names the offending value and the accepted vocabulary and never defaults.
func TestCorpusScan_IncludeTestsRefusesBadInput(t *testing.T) {
	root := splitRepo(t)
	gc := astCorpus(checkNode("chk-1", "c", "d", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	for _, bad := range []string{"", "  ", "yes", "TRUE", "1", "0"} {
		t.Run("value "+bad, func(t *testing.T) {
			_, err := CorpusScanAnalyzer{}.Run(context.Background(), withIncludeTests(scanRequest(gc, "repo", root), bad))
			if err == nil {
				t.Fatalf("a value outside the vocabulary must be refused, not coerced")
			}
			if !strings.Contains(err.Error(), ExtraKeyIncludeTests) {
				t.Errorf("the refusal must name the key, got %q", err)
			}
			if !strings.Contains(err.Error(), "true") || !strings.Contains(err.Error(), "false") {
				t.Errorf("and enumerate the admitted values, got %q", err)
			}
		})
	}

	t.Run("a language with no test-file convention refuses an explicit value", func(t *testing.T) {
		for _, v := range []string{"true", "false"} {
			req := withIncludeTests(scanRequest(gc, "repo", root), v)
			req.Language = "rust"
			_, err := CorpusScanAnalyzer{}.Run(context.Background(), req)
			if err == nil {
				t.Fatalf("include_tests=%s for rust must be refused: ast filters nothing there, so the control would do nothing", v)
			}
			if !strings.Contains(err.Error(), "rust") {
				t.Errorf("the refusal names the offending language, got %q", err)
			}
			if !strings.Contains(err.Error(), "go") {
				t.Errorf("and lists the languages that do support it, got %q", err)
			}
		}
		// THE CONTROL: the same language with the key ABSENT is not refused
		// here — an omitted flag misleads nobody, and this run fails later for
		// its own reason (an empty rust corpus) rather than on the knob.
		req := scanRequest(gc, "repo", root)
		req.Language = "rust"
		_, err := CorpusScanAnalyzer{}.Run(context.Background(), req)
		if err != nil && strings.Contains(err.Error(), ExtraKeyIncludeTests) {
			t.Errorf("an omitted flag must never be refused for the language, got %q", err)
		}
	})
}
