// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// Every test here is ALWAYS ON and runs on synthetic findings: no analyzer, no
// network, no daemon, no filesystem.

const (
	shaA = "b86df01e170b39db7009b132e8d2e2c8bc32e638"
	shaB = "391d7e8b0c500b23033474a61c31a9befff9a13f"
)

func siteFinding(checkID, file, line string) foundation.Finding {
	return foundation.Finding{
		Algorithm: "corpus_scan",
		Metadata:  map[string]string{MetaKeyCheckID: checkID, MetaKeyFile: file, MetaKeyLine: line},
	}
}

// fileFinding OMITS the line key entirely, which is what file-granularity means.
func fileFinding(checkID, file string) foundation.Finding {
	return foundation.Finding{
		Algorithm: "corpus_scan",
		Metadata:  map[string]string{MetaKeyCheckID: checkID, MetaKeyFile: file},
	}
}

func nonSiteFinding(checkID string, sev foundation.Severity) foundation.Finding {
	return foundation.Finding{
		Algorithm: "corpus_scan",
		Severity:  sev,
		Metadata:  map[string]string{MetaKeyCheckID: checkID},
	}
}

// runLevelNotice carries NO Metadata at all — the fourth contract shape.
func runLevelNotice(sev foundation.Severity) foundation.Finding {
	return foundation.Finding{Algorithm: "corpus_scan", Severity: sev}
}

func alert(ruleID, sha, path string, start, end int) AlertSite {
	return AlertSite{
		RuleID: ruleID, CommitSHA: sha, MirrorPath: path,
		StartLine: start, EndLine: end, Tool: toolCodeQL,
	}
}

// TestScore_RejectsInternalCoordinates carries BOTH halves: an internal-only
// path must error, and identity-mapped prefixes must NOT, because they are
// equally valid in both spaces and rejecting them would be a false positive.
func TestScore_RejectsInternalCoordinates(t *testing.T) {
	const internalPath = "cmd/knowledge/internal/tools/tools_logs_search.go"
	_, err := Score(nil, []foundation.Finding{siteFinding("c1", internalPath, "12")}, shaA, nil)
	if err == nil {
		t.Fatal("an internal-coordinate finding must be refused, not silently remapped")
	}
	if !strings.Contains(err.Error(), internalPath) {
		t.Fatalf("the error must name the offending value, got: %v", err)
	}

	// KNOWN-NEGATIVE: identity-mapped prefixes are valid in both spaces.
	for _, ok := range []string{"gen/knowledge/v1/node.go", "docs/guides/concepts.md"} {
		if _, err := Score(nil, []foundation.Finding{siteFinding("c1", ok, "12")}, shaA, nil); err != nil {
			t.Fatalf("%s is identity-mapped and must not be refused: %v", ok, err)
		}
	}
}

// TestScore_ReportsUndefinedRecall pins that ground truth is COMMIT-SCOPED: an
// alert at another commit is not a miss, it is not this run's truth at all.
func TestScore_ReportsUndefinedRecall(t *testing.T) {
	alerts := []AlertSite{alert("go/allocation-size-overflow", shaB, "internal/x.go", 10, 10)}
	r, err := Score(alerts, []foundation.Finding{siteFinding("c1", "internal/x.go", "10")}, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	for _, rs := range r.Rules {
		if rs.RecallDefined || rs.FileRecallDefined {
			t.Fatalf("recall must be undefined with no ground truth at this commit, got %+v", rs)
		}
		if rs.Recall != 0 || rs.FileRecall != 0 {
			t.Fatalf("an undefined recall must not carry a value, got %+v", rs)
		}
	}
}

// TestScore_CountsExtraFindings is the fingerprint-not-population ratio: three
// claims, one ground-truth site, one match.
func TestScore_CountsExtraFindings(t *testing.T) {
	alerts := []AlertSite{alert("go/allocation-size-overflow", shaA, "internal/x.go", 10, 10)}
	findings := []foundation.Finding{
		siteFinding("c1", "internal/x.go", "10"),
		siteFinding("c1", "internal/x.go", "40"),
		siteFinding("c1", "internal/y.go", "7"),
	}
	r, err := Score(alerts, findings, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(r.Checks) != 1 {
		t.Fatalf("expected one check score, got %+v", r.Checks)
	}
	c := r.Checks[0]
	if c.SiteMatched != 1 || c.SiteClaims != 3 {
		t.Fatalf("expected 1 of 3 matched, got %+v", c)
	}
	if want := 1.0 / 3.0; c.Precision != want {
		t.Fatalf("precision = %v, want %v", c.Precision, want)
	}
	if len(r.Extra) != 2 {
		t.Fatalf("expected 2 extra site claims, got %d", len(r.Extra))
	}
	if len(r.Unmatched) != 0 {
		t.Fatalf("the single alert was matched, so nothing is unmatched: %+v", r.Unmatched)
	}
	if r.JoinedZero {
		t.Fatal("a run with a join must not set JoinedZero")
	}
}

// TestScore_DisclosesJoinedZero pairs the positive with TWO negatives, because a
// flag that is always true satisfies a one-sided test.
func TestScore_DisclosesJoinedZero(t *testing.T) {
	alerts := []AlertSite{alert("go/allocation-size-overflow", shaA, "internal/x.go", 10, 10)}

	r, err := Score(alerts, []foundation.Finding{siteFinding("c1", "internal/other.go", "3")}, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !r.JoinedZero {
		t.Fatal("claims present, truth present, nothing joined — JoinedZero must be set")
	}

	joined, err := Score(alerts, []foundation.Finding{siteFinding("c2", "internal/x.go", "10")}, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if joined.JoinedZero {
		t.Fatal("one joining claim must clear JoinedZero")
	}

	refusedOnly, err := Score(alerts, []foundation.Finding{nonSiteFinding("c2", foundation.SeverityWarning)}, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if refusedOnly.JoinedZero {
		t.Fatal("a run of pure refusals is a refused run, not a zero join — the ledger already says so")
	}
}

// TestScore_ReportsInternalCounterpart proves the reporting half of the path map
// is actually populated, which is the field the renderer's R4 reads.
func TestScore_ReportsInternalCounterpart(t *testing.T) {
	// Both real rule ids, so the fixture mirrors the actual two-rule corpus.
	alerts := []AlertSite{
		alert("go/allocation-size-overflow", shaA, "internal/tools/tools_logs_search.go", 276, 276),
		alert("go/incorrect-integer-conversion", shaA, "README.md", 1, 1),
	}
	r, err := Score(alerts, nil, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(r.Unmatched) != 2 {
		t.Fatalf("expected both alerts unmatched, got %+v", r.Unmatched)
	}
	var mapped, mirrorOnly *AlertSite
	for i := range r.Unmatched {
		if r.Unmatched[i].MirrorPath == "README.md" {
			mirrorOnly = &r.Unmatched[i]
			continue
		}
		mapped = &r.Unmatched[i]
	}
	if mapped == nil || mapped.InternalPath != "cmd/knowledge/internal/tools/tools_logs_search.go" {
		t.Fatalf("the mapped alert must carry its exact internal counterpart, got %+v", mapped)
	}
	if mirrorOnly == nil || mirrorOnly.InternalClass != PathMirrorOnly {
		t.Fatalf("the mirror-only alert must classify mirror-only, got %+v", mirrorOnly)
	}
}

// TestScore_ExcludesNonSiteFromDenominators is the defamation guard: a refusal
// counted as imprecision would punish a scan that correctly refused a check.
func TestScore_ExcludesNonSiteFromDenominators(t *testing.T) {
	alerts := []AlertSite{alert("go/allocation-size-overflow", shaA, "internal/x.go", 10, 10)}
	findings := []foundation.Finding{
		nonSiteFinding("c1", foundation.SeverityWarning),
		runLevelNotice(foundation.SeverityInfo),
		siteFinding("c1", "internal/x.go", "10"),
		siteFinding("c1", "internal/x.go", "99"),
	}
	r, err := Score(alerts, findings, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v (a run-level notice carries no metadata and must not error)", err)
	}
	if len(r.Checks) != 1 {
		t.Fatalf("expected one check score, got %+v", r.Checks)
	}
	c := r.Checks[0]
	if c.SiteClaims != 2 {
		t.Fatalf("SiteClaims = %d, want 2 — non-site findings must not enter a denominator", c.SiteClaims)
	}
	if c.Precision != 0.5 {
		t.Fatalf("precision = %v, want 0.5 (not 0.25 or 0.33)", c.Precision)
	}
	if len(r.NonSite) != 2 {
		t.Fatalf("both non-site findings must be reported, got %d", len(r.NonSite))
	}
}

// TestScore_ScoresFileClaimsSeparately proves the two granularities never blend.
func TestScore_ScoresFileClaimsSeparately(t *testing.T) {
	const fileA, fileB = "internal/a.go", "internal/b.go"
	alerts := []AlertSite{alert("go/allocation-size-overflow", shaA, fileA, 100, 100)}
	findings := []foundation.Finding{
		fileFinding("c1", fileA),
		siteFinding("c1", fileA, "500"),
		fileFinding("c1", fileB),
	}
	r, err := Score(alerts, findings, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	c := r.Checks[0]
	if c.FileMatched != 1 || c.SiteMatched != 0 {
		t.Fatalf("file hit and site miss must be distinct, got %+v", c)
	}
	if c.FileClaims != 2 || c.SiteClaims != 1 {
		t.Fatalf("claim counts must stay separate, got %+v", c)
	}
	if c.Precision != 0 || !c.PrecisionDefined {
		t.Fatalf("precision must reflect only the site claim, got %+v", c)
	}
	rs := r.Rules[0]
	if rs.Recall == rs.FileRecall {
		t.Fatalf("line and file recall must differ here, both %v", rs.Recall)
	}
	if len(r.ExtraFile) != 1 || len(r.Extra) != 1 {
		t.Fatalf("the unmatched file claim belongs in ExtraFile and the unmatched site claim in Extra, got Extra=%d ExtraFile=%d", len(r.Extra), len(r.ExtraFile))
	}
}

// TestScore_NormalizesCheckKinds catches an implementer who helpfully defaults
// an undeclared scope, which would be a fabricated coverage claim.
func TestScore_NormalizesCheckKinds(t *testing.T) {
	r, err := Score(nil, nil, shaA, []string{"flow_model", "ast_pattern", "ast_pattern"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(r.CheckKinds) != 2 || r.CheckKinds[0] != "ast_pattern" || r.CheckKinds[1] != "flow_model" {
		t.Fatalf("CheckKinds must be deduplicated and sorted, got %v", r.CheckKinds)
	}

	undeclared, err := Score(nil, nil, shaA, nil)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(undeclared.CheckKinds) != 0 {
		t.Fatalf("an undeclared scope must stay undeclared, got %v", undeclared.CheckKinds)
	}
}
