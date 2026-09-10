// SPDX-License-Identifier: Apache-2.0

// render_compact_test.go — the one-line-per-hit render: its two row classes,
// its per-line bounds, and the lead block it must never compact.

package corpusscan

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// astSite builds one ast row's finding the way astSiteFinding builds it, so the
// row assertions below read the same shape production emits.
func astSite(file string, line string, checkID, name string) foundation.Finding {
	site := file + ":" + line
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityWarning,
		Title:     name + " at " + site,
		Summary:   strings.Repeat("prose guidance that the compact form must not carry. ", 20),
		Evidence:  []string{site, checkID, "X=some captured source text"},
		Metadata: map[string]string{
			MetaKeyFile:    file,
			MetaKeyLine:    line,
			MetaKeyCheckID: checkID,
		},
	}
}

// TestCompactLine_AstRowCarriesTheFourColumns pins row class A.
func TestCompactLine_AstRowCarriesTheFourColumns(t *testing.T) {
	f := astSite("cmd/knowledge/internal/tools/manage_checks_run.go", "113", "chk-1", "no naked defer Close")
	cols := strings.Split(CompactLine(f), "\t")
	if len(cols) != 4 {
		t.Fatalf("expected four tab-separated columns, got %d: %q", len(cols), cols)
	}
	if cols[0] != string(foundation.SeverityWarning) {
		t.Errorf("column 1 is the severity, got %q", cols[0])
	}
	if cols[1] != "cmd/knowledge/internal/tools/manage_checks_run.go:113" {
		t.Errorf("column 2 is file:line off the finding's own dedup key, got %q", cols[1])
	}
	if cols[2] != "chk-1" {
		t.Errorf("column 3 is the check id, got %q", cols[2])
	}
	if cols[3] != "no naked defer Close" {
		t.Errorf("column 4 is the check's display name with the site trimmed off, got %q", cols[3])
	}
	// THE PROSE IS THE WHOLE COST AND IT MUST BE GONE. A site finding's Summary
	// is the check's entire description, repeated on every one of its sites.
	if strings.Contains(CompactLine(f), "prose guidance") {
		t.Error("the compact row must not carry the check's description — that repetition is what it exists to drop")
	}
}

// TestCompactLine_AstRowStaysWithinItsBound is requirement 1's per-line bound
// for row class A, WITH THE FALSIFYING CONTROL that makes the pass mean
// something: the fixture's own uncapped row is over the bound, so the row being
// under it is the cap working rather than a fixture too small to reach it.
func TestCompactLine_AstRowStaysWithinItsBound(t *testing.T) {
	// The components are this repository's own measured worst cases: its longest
	// repo-relative path is 137 bytes, its largest file runs to four digits of
	// line number, and a check id is 32 characters. The path below is synthetic
	// at that same 137-byte width, so the row is measured against the widest
	// input the tree can produce rather than against a convenient one.
	longPath := "a/" + strings.Repeat("deeply-nested-package/", 5) + strings.Repeat("q", 16) + "_index.go"
	longID := strings.Repeat("z", 32)
	longName := strings.Repeat("an-unreasonably-long-check-display-name-", 6)
	if len(longPath) != 137 {
		t.Fatalf("the fixture path must measure the tree's longest at 137 bytes, got %d", len(longPath))
	}
	f := astSite(longPath, "654321", longID, longName)

	uncapped := len(string(f.Severity)) + 1 + len(longPath) + 1 + len("654321") + 1 +
		len(longID) + 1 + len(longName)
	if uncapped <= CompactRowBoundAst {
		t.Fatalf("the falsifying control is inert: this fixture's uncapped row measures %d, already under the %d-byte bound, so the assertion below would pass with no cap at all",
			uncapped, CompactRowBoundAst)
	}

	line := CompactLine(f)
	if len(line) > CompactRowBoundAst {
		t.Errorf("an ast row must be at or under the documented %d-byte bound, got %d: %q", CompactRowBoundAst, len(line), line)
	}
	if !strings.HasSuffix(strings.Split(line, "\t")[3], "...") {
		t.Error("a capped display name must SAY it was cut — a silent truncation reads as the check's real name")
	}
}

// TestRenderCompact_LeadFindingsAreNeverCompacted is the ordering rule in the
// render. The refusals and disclosures are what make a bounded result honest, so
// they render in full above the lines rather than as four columns.
func TestRenderCompact_LeadFindingsAreNeverCompacted(t *testing.T) {
	findings := []foundation.Finding{
		llmOnlyDisclosure(nil),
		astSite("pkg/a.go", "7", "chk-1", "no naked defer Close"),
		astSite("pkg/b.go", "9", "chk-1", "no naked defer Close"),
	}
	body, err := RenderCompact(findings)
	if err != nil {
		t.Fatalf("RenderCompact: %v", err)
	}
	if !strings.Contains(body, DisclosureTitleLLMOnly) {
		t.Error("the disclosure must survive the compact render in full — compacting it is how a bounded result stops being honest")
	}
	if !strings.Contains(body, "accepted") {
		t.Error("and it must keep its PROSE, which is the half a compact row would drop")
	}

	lines := 0
	for l := range strings.SplitSeq(body, "\n") {
		if strings.Count(l, "\t") == 3 {
			lines++
		}
	}
	if lines != 2 {
		t.Fatalf("exactly the two site findings compact to a row each, got %d", lines)
	}

	// THE BLOCK BOUND IS THE ROW CLASS'S WIDTH TIMES THE COMPACTED COUNT, and
	// deliberately not a bound on the whole body: the lead block is uncompacted
	// by construction and one lead finding alone outruns one row.
	block := 0
	for _, f := range findings {
		if !IsLeadFinding(f) {
			block += len(CompactLine(f)) + 1
		}
	}
	if want := compactLineCount(findings) * (CompactRowBoundAst + 1); block > want {
		t.Errorf("the compact block must be at or under %d bytes for %d rows, got %d", want, compactLineCount(findings), block)
	}
}

// The hand-listed agreement walk that stood here is RETIRED, not dropped. It
// listed eight of the block's titles, and a title added without an arm in
// ClassifyRun was missing from BOTH the production switch and this walk — the
// one omission hiding the other. The same two taxonomies are now driven over
// EVERY title the declaration holds, with the same falsifying site control, by
// TestVocabulary_EveryLockedTitleIsLeadOnBothTaxonomies in
// locked_titles_census_test.go.

// TestCompactRenderDefault_IsCompactForAFileListAndFullOtherwise pins the
// default both faces resolve through, in all four states.
func TestCompactRenderDefault_IsCompactForAFileListAndFullOtherwise(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name     string
		explicit *bool
		files    []string
		want     bool
	}{
		{"omitted with a file list", nil, []string{"a.go"}, true},
		{"omitted with no file list", nil, nil, false},
		{"explicit false with a file list", &no, []string{"a.go"}, false},
		{"explicit true with no file list", &yes, nil, true},
	} {
		if got := CompactRenderDefault(tc.explicit, tc.files); got != tc.want {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, got)
		}
	}
}
