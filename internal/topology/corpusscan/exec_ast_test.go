// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// knownNegative has no defer at all, so a `defer $X.Close()` check must be
// SILENT on it. It is the other half of every positive assertion below: a
// subtest asserting only the positive half is satisfied by a pattern that is
// wrong in the other direction.
const knownNegative = `package q

type d struct{}

func (d) Close() error { return nil }

func plain(x d) {
	_ = x.Close()
}
`

// fixtureNodes are the two example nodes every ast check in this file binds to.
func fixtureNodes() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}
}

// astCorpus seeds a practice graph holding the given check nodes plus the shared
// fixtures.
func astCorpus(checks ...*knowledgev1.Node) *fakeCaller {
	return newFakeCaller().seed("checks", append(checks, fixtureNodes()...), nil)
}

func TestCorpusScan_AstPatternEmitsPerSiteFindings(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"hit/hit.go":   deferCloseBad,
		"miss/miss.go": knownNegative,
	})
	gc := astCorpus(checkNode("chk-1", "no naked defer Close", "handle the error Close returns",
		astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	sites := matchFindings(runScan(t, scanRequest(gc, "repo", root)))
	if len(sites) != 1 {
		t.Fatalf("expected exactly the one matching site (the known-negative file must not match), got %d: %+v", len(sites), sites)
	}
	f := sites[0]
	if f.Metadata[MetaKeyFile] != "hit/hit.go" {
		t.Errorf("%s must carry the repo-relative path, got %q", MetaKeyFile, f.Metadata[MetaKeyFile])
	}
	line, err := strconv.Atoi(f.Metadata[MetaKeyLine])
	if err != nil || line <= 0 {
		t.Errorf("%s must carry a positive 1-based decimal line, got %q", MetaKeyLine, f.Metadata[MetaKeyLine])
	}
	if f.Metadata[MetaKeyCheckID] != "chk-1" {
		t.Errorf("%s must carry the check id, got %q", MetaKeyCheckID, f.Metadata[MetaKeyCheckID])
	}
	if want := "hit/hit.go:" + strconv.Itoa(line); len(f.Evidence) == 0 || f.Evidence[0] != want {
		t.Errorf("Evidence[0] is the dedup key and must be file:line %q, got %v", want, f.Evidence)
	}
	// THE CATCHER for an implementation that drops the source node and titles the
	// finding with the check id: that would still populate every metadata key and
	// pass every other assertion here.
	if !strings.HasPrefix(f.Title, "no naked defer Close") {
		t.Errorf("Title must carry the check node's SymbolName, got %q", f.Title)
	}
	if f.Summary != "handle the error Close returns" {
		t.Errorf("Summary must carry the check node's prose guidance, got %q", f.Summary)
	}
	if f.Algorithm != AnalyzerName {
		t.Errorf("Algorithm must be %q, got %q", AnalyzerName, f.Algorithm)
	}
}

func TestCorpusScan_SeverityCarriedFromCheck(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	gc := astCorpus(
		checkNode("chk-warn", "warn check", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		checkNode("chk-crit", "crit check", "", astCheckMeta("defer $X.Close()", "critical", "fx-bad", "fx-good")),
	)
	got := map[string]foundation.Severity{}
	for _, f := range matchFindings(runScan(t, scanRequest(gc, "repo", root))) {
		got[f.Metadata[MetaKeyCheckID]] = f.Severity
	}
	// TWO severities in ONE run: a single severity is satisfied by a hardcoded
	// constant.
	if got["chk-warn"] != foundation.SeverityWarning {
		t.Errorf("chk-warn must carry its own severity, got %q", got["chk-warn"])
	}
	if got["chk-crit"] != foundation.SeverityCritical {
		t.Errorf("chk-crit must carry its own severity, got %q", got["chk-crit"])
	}

	// An unmappable severity is a typed error, never a default — defaulting
	// would silently relabel a critical finding as info.
	_, err := checkSeverity(corpus.Check{ID: "chk-x", Severity: "catastrophic"})
	if err == nil {
		t.Fatal("an unmappable severity must error rather than defaulting")
	}
	if !strings.Contains(err.Error(), "chk-x") {
		t.Errorf("the error must name the check, got %q", err)
	}
}

func TestCorpusScan_PathPrefixNarrowsTheWalk(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"inside/x.go":  deferCloseBad,
		"outside/y.go": deferCloseBad,
	})
	gc := astCorpus(checkNode("chk-1", "check", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	// CONTROL: unnarrowed, BOTH files match. Without it a narrowed count of one
	// cannot be told from a walk that only ever sees one file.
	if n := len(matchFindings(runScan(t, scanRequest(gc, "repo", root)))); n != 2 {
		t.Fatalf("control: the unnarrowed walk must see both files, got %d", n)
	}
	req := scanRequest(gc, "repo", root)
	req.PathPrefix = "inside"
	sites := matchFindings(runScan(t, req))
	if len(sites) != 1 {
		t.Fatalf("path_prefix must narrow the walk to one file, got %d: %+v", len(sites), sites)
	}
	if f := sites[0].Metadata[MetaKeyFile]; !strings.HasPrefix(f, "inside/") {
		t.Errorf("the surviving match must be the one inside the prefix, got %q", f)
	}
}

// TestCorpusScan_CheckWhereNarrowsMatches is the ONLY gate that can catch a
// where-tree parsed, validated and then dropped on the way to ast.Match — an
// omission nothing about the file's identifiers reveals.
//
// TWO checks, the SAME pattern, the SAME tree: the where-tree is the only
// variable, so an implementation passing nil yields EQUAL counts and fails.
func TestCorpusScan_CheckWhereNarrowsMatches(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": `package p

type c struct{}

func (c) Close() error { return nil }

func handleFoo(x c) { defer x.Close() }
func handleBar(x c) { defer x.Close() }
func other(x c)     { defer x.Close() }
`})
	where := `{"inside_pattern":{"of":"$match","pattern":"func handleFoo($$$_) { $$$_ }"}}`
	wide := astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")
	// The narrowed check needs its OWN good fixture. The contract's calibration
	// probe re-runs the good example with the where DROPPED and requires it to
	// FIRE, proving it sits inside the check's shape population and is excluded
	// by the narrowing rather than by irrelevance — so a good example carrying no
	// defer at all is correctly REFUSED for a where-carrying check.
	narrow := astCheckMeta("defer $X.Close()", "warning", "fx-narrow-bad", "fx-narrow-good")
	narrow[corpus.MetaCheckWhere] = where

	gc := newFakeCaller().seed("checks", append([]*knowledgev1.Node{
		checkNode("chk-wide", "wide", "", wide),
		checkNode("chk-narrow", "narrow", "", narrow),
		exampleNode("fx-narrow-bad", `package p

type c struct{}

func (c) Close() error { return nil }

func handleFoo(x c) { defer x.Close() }
`),
		exampleNode("fx-narrow-good", `package p

type c struct{}

func (c) Close() error { return nil }

func elsewhere(x c) { defer x.Close() }
`),
	}, fixtureNodes()...), nil)
	counts := map[string]int{}
	for _, f := range matchFindings(runScan(t, scanRequest(gc, "repo", root))) {
		counts[f.Metadata[MetaKeyCheckID]]++
	}
	if counts["chk-narrow"] == 0 {
		t.Fatal("the narrowed check must still match something — a where-tree that matches nothing would satisfy 'strictly fewer' for the wrong reason")
	}
	if counts["chk-narrow"] >= counts["chk-wide"] {
		t.Fatalf("the where-carrying check must report STRICTLY FEWER matches than its twin; wide=%d narrow=%d — equal counts mean the parsed where-tree never reached the walk",
			counts["chk-wide"], counts["chk-narrow"])
	}
}

func TestCorpusScan_PerCheckCeilingDisclosesTruncation(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": manyDefers(MaxFindingsPerCheck + 7)})
	gc := astCorpus(checkNode("chk-1", "check", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	findings := runScan(t, scanRequest(gc, "repo", root))
	sites := matchFindings(findings)
	if len(sites) != MaxFindingsPerCheck {
		t.Fatalf("the per-check ceiling must clip to %d rendered sites, got %d", MaxFindingsPerCheck, len(sites))
	}
	notices := findingsByTitlePrefix(findings, TruncationPrefixCheck)
	if len(notices) != 1 {
		t.Fatalf("exactly one per-check truncation notice must be emitted, got %d", len(notices))
	}
	n := notices[0]
	if n.Metrics["matches_total"] != float64(MaxFindingsPerCheck+7) {
		t.Errorf("the notice must report the TRUE total, got %v", n.Metrics["matches_total"])
	}
	if n.Metrics["matches_rendered"] != float64(MaxFindingsPerCheck) {
		t.Errorf("the notice must report what was rendered, got %v", n.Metrics["matches_rendered"])
	}
	if n.Metadata[MetaKeyCheckID] != "chk-1" {
		t.Errorf("the notice must name its check, got %q", n.Metadata[MetaKeyCheckID])
	}
	if _, ok := n.Metadata[MetaKeyFile]; ok {
		t.Error("a truncation notice flags no site and must carry no file key")
	}
}

func TestCorpusScan_RunCeilingDisclosesTruncation(t *testing.T) {
	// Three checks each matching 40 sites drives the RUN past MaxFindingsTotal
	// while staying under the per-check ceiling, so the run counter is the one
	// that moves. A counter with no case that moves it cannot be told apart from
	// a counter never wired.
	root := seedRepo(t, map[string]string{"a/a.go": manyDefers(40)})
	pat := astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")
	gc := astCorpus(
		checkNode("chk-1", "one", "", pat),
		checkNode("chk-2", "two", "", pat),
		checkNode("chk-3", "three", "", pat),
	)
	findings := runScan(t, scanRequest(gc, "repo", root))
	if n := len(matchFindings(findings)); n != MaxFindingsTotal {
		t.Fatalf("the run ceiling must clip to %d rendered sites, got %d", MaxFindingsTotal, n)
	}
	runNotice := findingsByTitlePrefix(findings, TruncationTitleRun)
	if len(runNotice) != 1 {
		t.Fatalf("exactly one run-level truncation notice must be emitted, got %d", len(runNotice))
	}
	if runNotice[0].Metrics["findings_total"] != 120 {
		t.Errorf("the run notice must report the TRUE total, got %v", runNotice[0].Metrics["findings_total"])
	}
	if _, ok := runNotice[0].Metadata[MetaKeyCheckID]; ok {
		t.Error("the run notice describes the run, not a check, and must carry no check_id")
	}

	assertSmallTopKKeepsDisclosures(t)
}

// assertSmallTopKKeepsDisclosures is THE ORDERING CATCHER.
// foundation.TruncateTopK keeps the FIRST k findings, so an implementation that
// appended its refusals and truncation notices AFTER the match findings they
// describe would have a small positive TopK clip away every disclosure — a
// silent cap. The corpus below deliberately produces FOUR lead findings (three
// per-check truncation notices and one refusal) and the assertion is that a
// TopK just above that count returns all four.
func assertSmallTopKKeepsDisclosures(t *testing.T) {
	t.Helper()
	root := seedRepo(t, map[string]string{"a/a.go": manyDefers(MaxFindingsPerCheck + 5)})
	pat := astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")
	gc := astCorpus(
		checkNode("chk-1", "one", "", pat),
		checkNode("chk-2", "two", "", pat),
		checkNode("chk-3", "three", "", pat),
		checkNode("chk-4-flow", "flow model", "", map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckFlowModel),
			corpus.MetaSeverity:    "warning",
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "{}",
			corpus.MetaFixtureBad:  "fx-bad",
			corpus.MetaFixtureGood: "fx-good",
		}),
	)
	// CONTROL: unbounded, the four disclosures and the match findings all come
	// back, so the bounded assertion below is about the CAP and not about a run
	// that produced no disclosures.
	full := runScan(t, scanRequest(gc, "repo", root))
	if got := len(findingsByTitlePrefix(full, TruncationPrefixCheck)); got != 3 {
		t.Fatalf("control: expected 3 per-check truncation notices, got %d", got)
	}
	if got := len(findingsByTitlePrefix(full, RefusalPrefixUnvalidated)); got != 1 {
		t.Fatalf("control: expected 1 refusal, got %d", got)
	}
	if len(matchFindings(full)) == 0 {
		t.Fatal("control: the unbounded run must also carry match findings for the cap to be able to clip anything")
	}

	req := scanRequest(gc, "repo", root)
	req.TopK = 5
	bounded := runScan(t, req)
	if len(bounded) != 5 {
		t.Fatalf("TopK must bound the result, got %d", len(bounded))
	}
	if got := len(findingsByTitlePrefix(bounded, TruncationPrefixCheck)); got != 3 {
		t.Errorf("a small TopK must keep all 3 truncation notices, got %d", got)
	}
	if got := len(findingsByTitlePrefix(bounded, RefusalPrefixUnvalidated)); got != 1 {
		t.Errorf("a small TopK must keep the refusal, got %d", got)
	}
}

func TestCorpusScan_UnexecutableCheckTypeRefusedLoudly(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	gc := astCorpus(
		// The KNOWN-POSITIVE CONTROL: an executable sibling in the SAME run.
		checkNode("chk-ok", "executes", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		checkNode("chk-flow", "flow model", "", map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckFlowModel),
			corpus.MetaSeverity:    "warning",
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "{}",
			corpus.MetaFixtureBad:  "fx-bad",
			corpus.MetaFixtureGood: "fx-good",
		}),
	)
	findings := runScan(t, scanRequest(gc, "repo", root))
	refusals := findingsByTitlePrefix(findings, RefusalPrefixUnvalidated)
	if len(refusals) != 1 {
		t.Fatalf("the unexecutable check type must produce exactly one loud refusal, got %d", len(refusals))
	}
	r := refusals[0]
	if !strings.Contains(r.Title, "chk-flow") {
		t.Errorf("the refusal must name the check, got %q", r.Title)
	}
	if r.Severity != foundation.SeverityCritical {
		t.Errorf("a refusal is critical, got %q", r.Severity)
	}
	if !strings.Contains(r.Summary, classifyNoExecutor) {
		t.Errorf("the refusal must classify as %q, got %q", classifyNoExecutor, r.Summary)
	}
	if len(matchFindings(findings)) == 0 {
		t.Error("the executable sibling must still run — a gate that refused everything would pass the assertion above")
	}
}

// TestCorpusScan_AstDeniedLanguageRefusesTheRun proves a grammar the ast engine
// denies is refused rather than walked to a guaranteed-empty result.
func TestCorpusScan_AstDeniedLanguageRefusesTheRun(t *testing.T) {
	// CONTROL: go is not denied, so the guard discriminates.
	if _, _, err := executeAstCheck(context.Background(), scanRequest(newFakeCaller(), "repo", t.TempDir()),
		corpusEntry{Check: corpus.Check{ID: "chk-go", Severity: foundation.SeverityWarning, Language: "go", Pattern: "defer $X.Close()"}, Node: &knowledgev1.Node{}}, scanOptions{}); err != nil {
		t.Fatalf("control: a non-denied language must execute, got %v", err)
	}
	_, _, err := executeAstCheck(context.Background(), scanRequest(newFakeCaller(), "repo", t.TempDir()),
		corpusEntry{Check: corpus.Check{ID: "chk-json", Severity: foundation.SeverityWarning, Language: "json", Pattern: "$X"}, Node: &knowledgev1.Node{}}, scanOptions{})
	if err == nil {
		t.Fatal("a denied grammar must refuse the check rather than report clean")
	}
	if !strings.Contains(err.Error(), "chk-json") {
		t.Errorf("the refusal must name the check, got %q", err)
	}
}

// manyDefers renders a Go file carrying n distinct `defer x.Close()` sites.
func manyDefers(n int) string {
	var b strings.Builder
	b.WriteString("package p\n\ntype c struct{}\n\nfunc (c) Close() error { return nil }\n\n")
	for i := range n {
		fmt.Fprintf(&b, "func use%d(x c) {\n\tdefer x.Close()\n}\n\n", i)
	}
	return b.String()
}
