// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
)

// The fixture bodies every check test in this package shares. deferCloseBad
// calls Close in a defer; deferCloseGood does not, so a `defer $X.Close()`
// pattern FIRES on the first and is SILENT on the second.
const (
	deferCloseBad = `package p

type c struct{}

func (c) Close() error { return nil }

func use(x c) {
	defer x.Close()
}
`
	deferCloseGood = `package p

type c struct{}

func (c) Close() error { return nil }

func use(x c) {
	_ = x
}
`
)

// TestCorpusScan_ReadsChecksCorpus is the EXECUTED PROOF of the checks read
// path. It asserts on the CAPTURED requests, not just on what came back: a fake
// that ignored the target entirely would satisfy a return-value-only assertion
// while proving nothing about where the read was addressed.
//
// This is the POSITIVE leg of the retarget. Its negative twin,
// TestCorpusScan_DoesNotReadThePracticeGraph, is what makes the pair
// discriminating — without it, a reader that accepted BOTH graphs would pass
// here and look identical to a correct one.
func TestCorpusScan_ReadsChecksCorpus(t *testing.T) {
	gc := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("chk-1", "no naked defer Close", "close errors must be handled",
			astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}, nil)

	set, err := fetchCorpus(context.Background(), scanRequest(gc, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("fetchCorpus: %v", err)
	}
	if len(set.Checks) != 1 || set.Checks[0].Check.ID != "chk-1" {
		t.Fatalf("expected the one seeded check, got %+v", set.Checks)
	}
	if set.Checks[0].Node.GetSymbolName() != "no naked defer Close" {
		t.Error("the source node must be RETAINED — corpus.Check carries no prose and a finding cannot name a check without it")
	}
	if len(set.Fixtures) != 2 {
		t.Errorf("expected both example nodes indexed as fixtures, got %d", len(set.Fixtures))
	}

	types := gc.requestedNodeTypes("checks")
	var sawFinding, sawExample bool
	for _, ty := range types {
		switch ty {
		case "finding":
			sawFinding = true
		case "example":
			sawExample = true
		}
	}
	if !sawFinding || !sawExample {
		t.Errorf("both node types must be requested against the checks graph, saw %v", types)
	}
	if len(gc.reqs) == 0 {
		t.Fatal("no reads were captured at all — the assertions below would pass vacuously over an empty slice")
	}
	for _, r := range gc.reqs {
		if g := r.GetTarget().GetGraph(); g != "checks" {
			t.Errorf("every corpus read must target the checks graph, got %q", g)
		}
		// The selector carries NO language: checks is a singleton, so language
		// travels as a metadata predicate on the plan instead. Asserting the
		// old target-language field here would now be asserting the absence of
		// the scoping rather than its presence.
		if l := r.GetTarget().GetLanguage(); l != "" {
			t.Errorf("the checks selector must not carry a language — it is a singleton, got %q", l)
		}
		var scoped bool
		for _, mp := range r.GetQuery().GetSelection().GetMetadataPredicates() {
			if mp.GetKey() == corpus.MetaLanguage && mp.GetValue() == "go" {
				scoped = true
			}
		}
		if !scoped {
			t.Error("every corpus read must carry the language metadata predicate that scopes it")
		}
	}
}

// TestCorpusScan_DoesNotReadThePracticeGraph is the NEGATIVE leg of the
// retarget, and the pair is only discriminating with it present: a reader that
// consulted BOTH graphs would satisfy the positive test above identically.
//
// A corpus seeded ONLY into the OLD location must yield NO checks, and the
// analyzer must never address practice/go. The known-positive control runs
// first against the same helper with the same nodes seeded into the NEW
// location, so an empty result here cannot be explained by a fixture the fake
// never accepted or a helper that seeds nothing.
func TestCorpusScan_DoesNotReadThePracticeGraph(t *testing.T) {
	corpusNodes := []*knowledgev1.Node{
		checkNode("chk-1", "no naked defer Close", "close errors must be handled",
			astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}

	// KNOWN-POSITIVE CONTROL: the very same nodes in the NEW location DO decode.
	control := newFakeCaller().seed("checks", corpusNodes, nil)
	controlSet, err := fetchCorpus(context.Background(), scanRequest(control, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("control: seeding the checks graph must decode, got %v", err)
	}
	if len(controlSet.Checks) != 1 {
		t.Fatalf("control: expected exactly 1 check from the checks graph, got %d — the negative assertion below would be vacuous", len(controlSet.Checks))
	}

	// THE ACTUAL ASSERTION: the identical corpus in the OLD location is invisible.
	stale := newFakeCaller().seed("practice/go", corpusNodes, nil)
	staleSet, err := fetchCorpus(context.Background(), scanRequest(stale, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("reading a corpus that lives only in practice/go must return an empty set, not an error, got %v", err)
	}
	if len(staleSet.Checks) != 0 {
		t.Errorf("a check authored into practice/go must NOT be picked up, got %d check(s) — the old location is still being consulted", len(staleSet.Checks))
	}
	if len(staleSet.Fixtures) != 0 {
		t.Errorf("fixtures in practice/go must NOT be picked up, got %d", len(staleSet.Fixtures))
	}
	if len(stale.reqs) == 0 {
		t.Fatal("no reads captured — the target assertion below would pass vacuously")
	}
	for _, r := range stale.reqs {
		if g := r.GetTarget().GetGraph(); g == "practice" {
			t.Error("the analyzer addressed practice — the old location must not be consulted at all, not even as a fallback")
		}
	}
}

// TestCorpusScan_DecodeErrorsOnBadMetadata covers contract return row 4 (a
// malformed check ERRORS) and row 3 in BOTH of its concrete forms — a bare prose
// node and a MODEL node are each skipped silently, with no error and no finding.
//
// The model case is not padding: this reader keys its skip on the ABSENCE of
// check_type, never on any model value, so a reader who meets a model node in a
// real corpus has a test telling them the silence is correct.
func TestCorpusScan_DecodeErrorsOnBadMetadata(t *testing.T) {
	// CONTROL first: a well-formed corpus decodes without error, so the failures
	// below cannot come from a decoder that rejects everything.
	good := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("chk-1", "ok", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}, nil)
	if _, err := fetchCorpus(context.Background(), scanRequest(good, "repo", t.TempDir()), nil); err != nil {
		t.Fatalf("control: a well-formed corpus must decode, got %v", err)
	}

	for _, tc := range []struct {
		name string
		md   map[string]string
		want string
	}{
		{"unknown check type", map[string]string{
			corpus.MetaCheckType: "wishful", corpus.MetaSeverity: "warning", corpus.MetaLanguage: "go",
			corpus.MetaDSLPattern: "x", corpus.MetaFixtureBad: "fx-bad", corpus.MetaFixtureGood: "fx-good",
		}, "check type"},
		{"missing fixtures", map[string]string{
			corpus.MetaCheckType: string(corpus.CheckAstPattern), corpus.MetaSeverity: "warning",
			corpus.MetaLanguage: "go", corpus.MetaDSLPattern: "defer $X.Close()",
		}, corpus.MetaFixtureBad},
		{"bad severity", map[string]string{
			corpus.MetaCheckType: string(corpus.CheckAstPattern), corpus.MetaSeverity: "catastrophic",
			corpus.MetaLanguage: "go", corpus.MetaDSLPattern: "defer $X.Close()",
			corpus.MetaFixtureBad: "fx-bad", corpus.MetaFixtureGood: "fx-good",
		}, corpus.MetaSeverity},
	} {
		gc := newFakeCaller().seed("checks", []*knowledgev1.Node{
			checkNode("chk-bad", "malformed", "", tc.md),
		}, nil)
		_, err := fetchCorpus(context.Background(), scanRequest(gc, "repo", t.TempDir()), nil)
		if err == nil {
			t.Errorf("%s: a corpus the contract rejects must error, not decode silently", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
		if !strings.Contains(err.Error(), "chk-bad") {
			t.Errorf("%s: error %q does not name the offending check", tc.name, err)
		}
	}

	// ROW 3, BOTH FORMS. A bare prose node and a model entry — the model carries
	// model_slug and model_kind and deliberately NO check_type — are each skipped
	// with no error and no membership in either lane.
	skips := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("prose-1", "guidance", "prose with no machine half", map[string]string{}),
		checkNode("model-1", "a sink entry", "", map[string]string{
			"model_slug": "os-exec", "model_kind": "sink", "model_shape": "call",
		}),
	}, nil)
	set, err := fetchCorpus(context.Background(), scanRequest(skips, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("row-3 nodes must be skipped silently, got %v", err)
	}
	if len(set.Checks) != 0 || len(set.LLMOnly) != 0 {
		t.Errorf("a prose node and a model node belong to NEITHER lane, got checks=%d llm_only=%d", len(set.Checks), len(set.LLMOnly))
	}
}

// TestCorpusScan_SubsetIDMatchingNothingErrors: an id naming no check is an
// error, never a silent narrowing.
func TestCorpusScan_SubsetIDMatchingNothingErrors(t *testing.T) {
	gc := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("chk-1", "one", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}, nil)
	req := scanRequest(gc, "repo", t.TempDir())

	// CONTROL: the id that DOES exist resolves, so the failure below is about the
	// missing id and not about subsetting being broken outright.
	if _, err := fetchCorpus(context.Background(), req, []string{"chk-1"}); err != nil {
		t.Fatalf("control: a resolvable subset must succeed, got %v", err)
	}
	_, err := fetchCorpus(context.Background(), req, []string{"chk-1", "chk-typo"})
	if err == nil {
		t.Fatal("an unresolvable subset id must error rather than silently narrowing the scan")
	}
	if !strings.Contains(err.Error(), "chk-typo") {
		t.Errorf("the error must name the unresolved id, got %q", err)
	}
}

// TestCorpusScan_ChecksSubsetExecutesOnlyTheNamed is the subset param's ONLY
// POSITIVE gate. Three checks, a subset naming two, and the third is given a
// pattern that WOULD match the seeded tree — so a count of two cannot be
// satisfied by an implementation that ran all three and got lucky.
func TestCorpusScan_ChecksSubsetExecutesOnlyTheNamed(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	nodes := []*knowledgev1.Node{
		checkNode("chk-1", "check one", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		checkNode("chk-2", "check two", "", astCheckMeta("defer $X.Close()", "notice", "fx-bad", "fx-good")),
		// chk-3 shares the same pattern, so it WOULD match the seeded tree.
		checkNode("chk-3", "check three", "", astCheckMeta("defer $X.Close()", "critical", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}
	req := scanRequest(newFakeCaller().seed("checks", nodes, nil), "repo", root)
	req.Extra = map[string]string{ExtraKeyChecks: "chk-1,chk-2"}

	byCheck := map[string]int{}
	for _, f := range matchFindings(runScan(t, req)) {
		byCheck[f.Metadata[MetaKeyCheckID]]++
	}
	if byCheck["chk-1"] == 0 || byCheck["chk-2"] == 0 {
		t.Errorf("both named checks must execute, got %v", byCheck)
	}
	if byCheck["chk-3"] != 0 {
		t.Errorf("the excluded check must produce NOTHING even though its pattern matches the tree, got %d findings", byCheck["chk-3"])
	}
}

// TestCorpusScan_LLMOnlyLaneRetainedNotSkipped is the catcher for the
// row-2/row-3 collapse — a one-line `if !isCheck { continue }` that no source
// grep distinguishes from correct code.
//
// The bare prose node is the DISCRIMINATING CONTROL: a reader that put every
// isCheck=false node into LLMOnly would pass a test that only counted the
// llm_only node.
func TestCorpusScan_LLMOnlyLaneRetainedNotSkipped(t *testing.T) {
	gc := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("llm-1", "needs judgment", "prose with no deterministic expression",
			map[string]string{corpus.MetaLLMOnly: "true", corpus.MetaLanguage: "go"}),
		checkNode("prose-1", "plain guidance", "no machine half at all", map[string]string{}),
		checkNode("chk-1", "executable", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
	}, nil)

	set, err := fetchCorpus(context.Background(), scanRequest(gc, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("fetchCorpus: %v", err)
	}
	if len(set.LLMOnly) != 1 || set.LLMOnly[0].ID != "llm-1" {
		t.Fatalf("the accepted llm_only node must survive the read in its own lane, got %+v", set.LLMOnly)
	}
	if len(set.Checks) != 1 || set.Checks[0].Check.ID != "chk-1" {
		t.Fatalf("the executable check must land in Checks, got %+v", set.Checks)
	}
	for _, c := range set.LLMOnly {
		if c.ID == "prose-1" {
			t.Error("a bare prose node must appear in NEITHER lane — it is row 3, not row 2")
		}
	}
}

// seedRepo writes files into a fresh temp directory and returns its path.
func seedRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root
}

// TestCorpusScan_LanguageScopingExcludesOtherLanguages is the gate the
// single-graph reshape made necessary, and its negative leg is the whole point.
//
// WHY THIS MATTERS MORE NOW THAN IT DID BEFORE. With a graph per language, wrong
// scoping returned NOTHING and announced itself immediately. With ONE graph,
// wrong scoping returns EVERY language's checks — a Go scan silently executing
// Python checks against Go source, which looks like a working scan producing
// puzzling findings rather than like a bug.
//
// Both languages are seeded in the SAME graph, which is the only arrangement
// that can tell scoping from graph selection: if the filter were dropped
// entirely, the Go read would return both checks and the count assertion fails.
func TestCorpusScan_LanguageScopingExcludesOtherLanguages(t *testing.T) {
	pyMeta := astCheckMeta("defer $X.Close()", "warning", "fx-py-bad", "fx-py-good")
	pyMeta[corpus.MetaLanguage] = "python"

	gc := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("go:no-naked-defer", "go check", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", deferCloseBad),
		exampleNode("fx-good", deferCloseGood),
		checkNode("python:no-naked-defer", "python check", "", pyMeta),
		exampleNodeLang("fx-py-bad", "x = 1\n", "python"),
		exampleNodeLang("fx-py-good", "y = 2\n", "python"),
	}, nil)

	set, err := fetchCorpus(context.Background(), scanRequest(gc, "repo", t.TempDir()), nil)
	if err != nil {
		t.Fatalf("fetchCorpus for go: %v", err)
	}

	// KNOWN-POSITIVE: the requested language's check IS returned. Without this,
	// a fetch that returned nothing at all would satisfy the exclusion below.
	if len(set.Checks) != 1 {
		t.Fatalf("expected exactly the ONE go check, got %d — either scoping dropped everything "+
			"or it returned both languages", len(set.Checks))
	}
	if got := set.Checks[0].Check.ID; got != "go:no-naked-defer" {
		t.Errorf("the returned check must be the go one, got %q", got)
	}

	// THE NEGATIVE LEG: the python check must NOT appear.
	for _, e := range set.Checks {
		if e.Check.Language != "go" {
			t.Errorf("a %s check leaked into a go scan (id %q) — language scoping is not filtering",
				e.Check.Language, e.Check.ID)
		}
	}

	// Fixtures are scoped too. A python fixture reachable from a go scan is the
	// same leak one level down, and it is what would let a go check bind to
	// python source once ids collide.
	for id := range set.Fixtures {
		if strings.HasPrefix(id, "fx-py-") {
			t.Errorf("python fixture %q is visible to a go scan — the fixture read is not language-scoped", id)
		}
	}
	if len(set.Fixtures) != 2 {
		t.Errorf("expected exactly the 2 go fixtures, got %d", len(set.Fixtures))
	}

	// The filter must be expressed SERVER-SIDE as a metadata predicate, not by
	// draining everything and filtering in Go — otherwise every scoped scan reads
	// the whole graph and the narrowing stops bounding anything as it grows.
	var sawLangPredicate bool
	for _, r := range gc.reqs {
		for _, p := range r.GetQuery().GetSelection().GetMetadataPredicates() {
			if p.GetKey() == corpus.MetaLanguage && p.GetValue() == "go" {
				sawLangPredicate = true
			}
		}
	}
	if !sawLangPredicate {
		t.Error("no language metadata predicate was sent — the corpus read is draining the whole " +
			"graph and narrowing client-side")
	}
}
