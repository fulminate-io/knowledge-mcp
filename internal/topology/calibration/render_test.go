// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// These are the ONLY tests in this package that read DISPLAYED TEXT. Every other
// assertion about a calibration number reads a struct field, and a struct field
// is not what a human is misled by.

// TestRenderReport_UndefinedAndJoinedZero covers R1 and R2, each with a
// two-sided fixture.
func TestRenderReport_UndefinedAndJoinedZero(t *testing.T) {
	// R1: everything undefined. No numeric figure may appear for either axis.
	allUndefined := ScoreReport{
		CommitSHA: shaA,
		Checks:    []CheckScore{{CheckID: "go:a"}, {CheckID: "go:b"}},
		Rules:     []RuleScore{{RuleID: "go/allocation-size-overflow"}},
	}
	out := RenderReport(allUndefined)
	if !strings.Contains(out, undefinedPrecision) {
		t.Fatalf("an undefined precision must render its phrase, got:\n%s", out)
	}
	if !strings.Contains(out, undefinedRecall) {
		t.Fatalf("an undefined recall must render its phrase, got:\n%s", out)
	}
	for _, forbidden := range []string{"0.0", "0%", "100%"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("an undefined result must never render as %q, got:\n%s", forbidden, out)
		}
	}

	// R2: the disclosure REPLACES every precision figure. TWO checks, because a
	// renderer that replaced only the first would pass a one-check fixture.
	joinedZero := ScoreReport{
		CommitSHA:  shaA,
		JoinedZero: true,
		Checks: []CheckScore{
			{CheckID: "go:a", SiteClaims: 4, SiteMatched: 0, Precision: 0, PrecisionDefined: true},
			{CheckID: "go:b", SiteClaims: 9, SiteMatched: 0, Precision: 0, PrecisionDefined: true},
		},
		Rules: []RuleScore{{RuleID: "go/x", GroundTruth: 5, RecallDefined: true, FileRecallDefined: true}},
	}
	zeroOut := RenderReport(joinedZero)
	if strings.Count(zeroOut, joinedZeroDisclosure) != 2 {
		t.Fatalf("the disclosure must replace the figure for EVERY check, got:\n%s", zeroOut)
	}
	for line := range strings.SplitSeq(zeroOut, "\n") {
		if !strings.Contains(line, "go:a") && !strings.Contains(line, "go:b") {
			continue
		}
		if strings.Contains(line, "site claims)") {
			t.Fatalf("no numeric precision figure may survive a zero join, got line: %s", line)
		}
	}

	// THE NEGATIVE, CONSTRAINED ON BOTH AXES. A fixture left free on the recall
	// axis reds correct work, because R1's recall phrase is legitimately emitted
	// for any rule sitting at GroundTruth == 0.
	ordinary := ScoreReport{
		CommitSHA: shaA,
		Checks:    []CheckScore{{CheckID: "go:a", SiteClaims: 4, SiteMatched: 2, Precision: 0.5, PrecisionDefined: true}},
		Rules: []RuleScore{{
			RuleID: "go/x", GroundTruth: 5, LineHit: 3, FileHit: 4,
			Recall: 0.6, RecallDefined: true, FileRecall: 0.8, FileRecallDefined: true,
		}},
	}
	ordinaryOut := RenderReport(ordinary)
	for _, phrase := range []string{undefinedPrecision, undefinedRecall, joinedZeroDisclosure} {
		if strings.Contains(ordinaryOut, phrase) {
			t.Fatalf("an ordinary report must not carry %q, got:\n%s", phrase, ordinaryOut)
		}
	}
}

// TestRenderReport_CarriesDenominatorsAndLedgers covers R3 through R8.
func TestRenderReport_CarriesDenominatorsAndLedgers(t *testing.T) {
	base := ScoreReport{
		CommitSHA:  shaA,
		CheckKinds: []string{"ast_pattern"},
		Checks:     []CheckScore{{CheckID: "go:a", SiteClaims: 4, SiteMatched: 2, Precision: 0.5, PrecisionDefined: true}},
		Rules: []RuleScore{{
			RuleID: "go/allocation-size-overflow", GroundTruth: 5, LineHit: 3, FileHit: 4,
			Recall: 0.6, RecallDefined: true, FileRecall: 0.8, FileRecallDefined: true,
		}},
		Unmatched: []AlertSite{
			{MirrorPath: "internal/tools/tools_logs_search.go", StartLine: 276, RuleID: "go/allocation-size-overflow",
				InternalPath: "cmd/knowledge/internal/tools/tools_logs_search.go", InternalClass: PathMapped},
			{MirrorPath: "README.md", StartLine: 1, RuleID: "go/allocation-size-overflow", InternalClass: PathMirrorOnly},
		},
		// DIFFERENT concrete lengths, so a renderer printing one where the other
		// belongs fails rather than coincidentally agreeing.
		Extra:     make([]foundation.Finding, 7),
		ExtraFile: make([]foundation.Finding, 3),
		NonSite: []foundation.Finding{
			nonSiteFinding("go:a", foundation.SeverityWarning),
			nonSiteFinding("go:b", foundation.SeverityCritical),
		},
	}
	out := RenderReport(base)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// R8: first line carries the marker AND the exact commit; last is the terminator.
	if !strings.HasPrefix(lines[0], blockMarkerPrefix) || !strings.Contains(lines[0], shaA) {
		t.Fatalf("first line must be the R8 marker with the commit, got: %s", lines[0])
	}
	if lines[len(lines)-1] != blockTerminator {
		t.Fatalf("last line must be the R8 terminator, got: %s", lines[len(lines)-1])
	}

	// R7: exactly one scope line, the ast-only literal.
	if strings.Count(out, scopeAstOnly) != 1 {
		t.Fatalf("the ast-only scope literal must appear exactly once, got:\n%s", out)
	}

	// R3 + R5: the precision figure carries its denominator AND the refusal
	// ledger, with each distinct severity named, on the same line.
	precisionLine := lineContaining(t, lines, "go:a:")
	for _, want := range []string{"50.0%", "2 of 4 site claims", refusalPrefix + "2", "warning", "critical"} {
		if !strings.Contains(precisionLine, want) {
			t.Fatalf("the precision line must carry %q, got: %s", want, precisionLine)
		}
	}

	// R4: the mapped alert names its counterpart; the mirror-only one says so.
	if !strings.Contains(out, "cmd/knowledge/internal/tools/tools_logs_search.go") {
		t.Fatalf("a mapped unmatched alert must render its internal counterpart, got:\n%s", out)
	}
	if !strings.Contains(out, mirrorOnlyCounterpart) {
		t.Fatalf("a mirror-only unmatched alert must render the literal, got:\n%s", out)
	}

	// R3 + R6: the site ratio carries its denominator; file extras are their own
	// labeled count and are not folded into it.
	if !strings.Contains(out, "extra site claims: 7 against 5 ground-truth alerts") {
		t.Fatalf("the over-flag ratio must carry its denominator, got:\n%s", out)
	}
	if !strings.Contains(out, "extra file claims: 3") {
		t.Fatalf("file extras must render as their own labeled count, got:\n%s", out)
	}

	// R7's other two states, on the SAME report with only CheckKinds changed —
	// three states, because a renderer emitting one form unconditionally passes
	// any single-state fixture.
	twoKinds := base
	twoKinds.CheckKinds = []string{"ast_pattern", "graph_assertion"}
	twoOut := RenderReport(twoKinds)
	if !strings.Contains(twoOut, scopeExecutedPrefix+"ast_pattern, graph_assertion") {
		t.Fatalf("a multi-kind scope must list the kinds sorted, got:\n%s", twoOut)
	}
	if strings.Contains(twoOut, scopeAstOnly) {
		t.Fatalf("a multi-kind scope must not claim ast-only, got:\n%s", twoOut)
	}

	undeclared := base
	undeclared.CheckKinds = nil
	undeclaredOut := RenderReport(undeclared)
	if !strings.Contains(undeclaredOut, scopeUndeclared) {
		t.Fatalf("an undeclared scope must say so, got:\n%s", undeclaredOut)
	}
	for _, forbidden := range []string{scopeAstOnly, scopeExecutedPrefix} {
		if strings.Contains(undeclaredOut, forbidden) {
			t.Fatalf("an undeclared scope must not render %q, got:\n%s", forbidden, undeclaredOut)
		}
	}
}

// lineContaining returns the single line holding needle, failing when absent.
func lineContaining(t *testing.T, lines []string, needle string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return l
		}
	}
	t.Fatalf("no line contains %q", needle)
	return ""
}
