// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The graph-shaped assertion these tests are written against: "no function may
// call another function". It is chosen because ONE fixture can carry both a
// violator and CONFORMING CONTROLS, which is what makes the zero half of the
// assertion meaningful.
const noOutboundCalls = `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","require":"absent"}`

// callsBad has ONE calling function and TWO that call nothing. The two
// non-callers are the conforming controls: an executor that flagged every node,
// or none, fails against them.
//
// THE CALLEE IS DEFINED IN THE SNIPPET DELIBERATELY. An unresolved external
// symbol is dropped by reference resolution and emits no CALLS edge, so a
// fixture calling into the standard library produces zero edges and the check
// under test would look clean for the wrong reason.
const callsBad = `package p

func helper() int { return 1 }

func caller() int { return helper() }

func lonely() int { return 2 }
`

// callsGood declares the same shape of functions with no call between them.
const callsGood = `package p

func alpha() int { return 1 }

func beta() int { return 2 }
`

// TestCorpusScan_GraphAssertionExecutes drives a graph_assertion check against a
// seeded code graph.
func TestCorpusScan_GraphAssertionExecutes(t *testing.T) {
	gc := newFakeCaller().
		seed("checks", []*knowledgev1.Node{
			checkNode("chk-graph", "no function calls another", "keep the call graph flat",
				graphCheckMeta(corpus.CheckGraphAssertion, noOutboundCalls, "warning", "fx-graph-bad", "fx-graph-good")),
			exampleNode("fx-graph-bad", callsBad),
			exampleNode("fx-graph-good", callsGood),
		}, nil).
		seed("code/repo", []*knowledgev1.Node{
			codeNode("pkg/a.go:caller"),
			codeNode("pkg/a.go:helper"),
			codeNode("pkg/a.go:lonely"),
		}, []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeCalls), FromId: "pkg/a.go:caller", ToId: "pkg/a.go:helper"},
		})

	sites := matchFindings(runScan(t, scanRequest(gc, "repo", t.TempDir())))
	if len(sites) != 1 {
		t.Fatalf("only the calling function violates; the two conforming controls must NOT be flagged, got %d: %+v", len(sites), sites)
	}
	f := sites[0]
	if f.Evidence[0] != "pkg/a.go:caller" {
		t.Errorf("Evidence[0] is the dedup key and must be the violating node id, got %v", f.Evidence)
	}
	if f.Metadata[MetaKeyFile] != "pkg/a.go" {
		t.Errorf("%s must be the id's path component, got %q", MetaKeyFile, f.Metadata[MetaKeyFile])
	}
	if f.Metadata[MetaKeyCheckID] != "chk-graph" {
		t.Errorf("%s must name the check, got %q", MetaKeyCheckID, f.Metadata[MetaKeyCheckID])
	}
	// A code-graph node id yields a file but never a line, so the key is OMITTED
	// rather than written as zero — an absent key is honest, a zero is a false row.
	if _, present := f.Metadata[MetaKeyLine]; present {
		t.Errorf("a graph finding is file-granular and must OMIT %s entirely, got %q", MetaKeyLine, f.Metadata[MetaKeyLine])
	}
	if !strings.HasPrefix(f.Title, "no function calls another") {
		t.Errorf("Title must carry the check node's SymbolName, got %q", f.Title)
	}
}

// TestCorpusScan_GraphAssertionFixtureViaPopulate drives ValidateGraphFixtures,
// which materializes each fixture, runs parser.Populate, and applies the SAME
// evaluate() the scan path uses.
//
// It fails if Populate is not wired, if the evaluator is wrong in either
// direction, if the sentinel wrapping is missing, if the non-empty-facts
// controls are absent, or if the two producers have drifted apart.
func TestCorpusScan_GraphAssertionFixtureViaPopulate(t *testing.T) {
	check := corpus.Check{
		ID: "chk-graph", Type: corpus.CheckGraphAssertion, Severity: "warning",
		Language: "go", Pattern: noOutboundCalls, FixtureBad: "fx-bad", FixtureGood: "fx-good",
	}
	bad := corpus.Fixture{ID: "fx-bad", Content: callsBad}
	good := corpus.Fixture{ID: "fx-good", Content: callsGood}

	// THE EXTERNAL EXPECTATION. Before asserting anything about the validator,
	// establish independently — by calling parser.Populate directly — that the
	// bad fixture really does produce a CALLS edge and the good one does not.
	// Without it, "fires on bad, silent on good" could be satisfied by two
	// fixtures that both produced nothing.
	if calls := populateCallsEdges(t, callsBad); calls != 1 {
		t.Fatalf("external expectation: the bad fixture must produce exactly 1 CALLS edge, got %d", calls)
	}
	if calls := populateCallsEdges(t, callsGood); calls != 0 {
		t.Fatalf("external expectation: the good fixture must produce 0 CALLS edges, got %d", calls)
	}

	if err := ValidateGraphFixtures(context.Background(), check, bad, good); err != nil {
		t.Fatalf("a correctly fixtured graph check must validate, got %v", err)
	}

	t.Run("silent_on_bad_is_refused", func(t *testing.T) {
		// Swapping the fixtures makes the check silent on its bad example.
		err := ValidateGraphFixtures(context.Background(), check, good, bad)
		if !errors.Is(err, corpus.ErrFixtureValidation) {
			t.Fatalf("expected the contract's validation sentinel so classification stays uniform, got %v", err)
		}
	})

	t.Run("vacuous_good_fixture_is_refused", func(t *testing.T) {
		// THE SUBTEST THE NON-EMPTY-FACTS CONTROLS EXIST FOR. A good fixture
		// declaring nothing produces zero graph nodes, so its silence proves
		// nothing — and would otherwise ADMIT an uncalibrated check.
		empty := corpus.Fixture{ID: "fx-empty", Content: "package p\n"}
		err := ValidateGraphFixtures(context.Background(), check, bad, empty)
		if !errors.Is(err, corpus.ErrFixtureValidation) {
			t.Fatalf("a fixture that declares nothing must be a NAMED ERROR, not a pass, got %v", err)
		}
		if !strings.Contains(err.Error(), "fx-empty") {
			t.Errorf("the error must name the offending fixture, got %q", err)
		}
	})
}

// TestCorpusScan_TopologyThresholdExecutes drives the numeric arm of the same
// evaluator, end to end through both producers.
func TestCorpusScan_TopologyThresholdExecutes(t *testing.T) {
	const body = `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","max_degree":1}`
	const overBudget = `package p

func a() int { return 1 }

func b() int { return 2 }

func fan() int { return a() + b() }
`
	const withinBudget = `package p

func a() int { return 1 }

func fan() int { return a() }
`
	check := corpus.Check{
		ID: "chk-thresh", Type: corpus.CheckTopologyThreshold, Severity: "notice",
		Language: "go", Pattern: body, FixtureBad: "fx-bad", FixtureGood: "fx-good",
	}
	// EXTERNAL EXPECTATION, measured through the producer rather than assumed:
	// the bad fixture fans out to two callees and the good one to a single callee.
	if calls := populateCallsEdges(t, overBudget); calls != 2 {
		t.Fatalf("external expectation: the over-budget fixture must produce 2 CALLS edges, got %d", calls)
	}
	if calls := populateCallsEdges(t, withinBudget); calls != 1 {
		t.Fatalf("external expectation: the within-budget fixture must produce 1 CALLS edge, got %d", calls)
	}
	if err := ValidateGraphFixtures(context.Background(), check,
		corpus.Fixture{ID: "fx-bad", Content: overBudget},
		corpus.Fixture{ID: "fx-good", Content: withinBudget}); err != nil {
		t.Fatalf("a correctly fixtured threshold check must validate, got %v", err)
	}

	gc := newFakeCaller().
		seed("checks", []*knowledgev1.Node{
			checkNode("chk-thresh", "keep fan-out at or below one", "split wide functions",
				graphCheckMeta(corpus.CheckTopologyThreshold, body, "notice", "fx-bad", "fx-good")),
			exampleNode("fx-bad", overBudget),
			exampleNode("fx-good", withinBudget),
		}, nil).
		seed("code/repo", []*knowledgev1.Node{
			codeNode("pkg/a.go:fan"), codeNode("pkg/a.go:a"), codeNode("pkg/a.go:b"),
		}, []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeCalls), FromId: "pkg/a.go:fan", ToId: "pkg/a.go:a"},
			{Type: string(kgtypes.EdgeCalls), FromId: "pkg/a.go:fan", ToId: "pkg/a.go:b"},
		})
	sites := matchFindings(runScan(t, scanRequest(gc, "repo", t.TempDir())))
	if len(sites) != 1 {
		t.Fatalf("only the over-budget node violates; the two callees are conforming controls, got %d: %+v", len(sites), sites)
	}
	if sites[0].Evidence[0] != "pkg/a.go:fan" {
		t.Errorf("the violating node must be the one over the threshold, got %v", sites[0].Evidence)
	}
	if sites[0].Metrics["degree"] != 2 {
		t.Errorf("the finding must report the measured degree, got %v", sites[0].Metrics["degree"])
	}
}

// TestCorpusScan_GraphAssertionRefusesEmptyCandidateSet is the scan-side twin of
// the fixture-side non-empty-facts control: an uncollected repo, or a node type
// this graph does not carry, must be a NAMED ERROR rather than a clean zero.
func TestCorpusScan_GraphAssertionRefusesEmptyCandidateSet(t *testing.T) {
	corpusNodes := []*knowledgev1.Node{
		checkNode("chk-graph", "no function calls another", "",
			graphCheckMeta(corpus.CheckGraphAssertion, noOutboundCalls, "warning", "fx-bad", "fx-good")),
		exampleNode("fx-bad", callsBad),
		exampleNode("fx-good", callsGood),
	}
	// CONTROL: with the code graph populated the same check succeeds, so the
	// error below is about the EMPTY graph and not about the check.
	ok := newFakeCaller().
		seed("checks", corpusNodes, nil).
		seed("code/repo", []*knowledgev1.Node{codeNode("pkg/a.go:caller"), codeNode("pkg/a.go:helper")},
			[]*knowledgev1.Edge{{Type: string(kgtypes.EdgeCalls), FromId: "pkg/a.go:caller", ToId: "pkg/a.go:helper"}})
	if err := runScanErr(scanRequest(ok, "repo", t.TempDir())); err != nil {
		t.Fatalf("control: a populated code graph must scan, got %v", err)
	}

	empty := newFakeCaller().seed("checks", corpusNodes, nil)
	err := runScanErr(scanRequest(empty, "repo", t.TempDir()))
	if err == nil {
		t.Fatal("an uncollected code graph must ERROR — zero violations there is indistinguishable from a clean repo")
	}
	if !strings.Contains(err.Error(), "collect") {
		t.Errorf("the error must tell the operator a collect is required, got %q", err)
	}
}

// TestCorpusScan_GraphAssertionBodyIsValidatedStrictly proves the assertion body
// refuses bad input rather than defaulting or ignoring it.
func TestCorpusScan_GraphAssertionBodyIsValidatedStrictly(t *testing.T) {
	base := corpus.Check{ID: "chk-1", Type: corpus.CheckGraphAssertion, Language: "go"}
	// CONTROL: the well-formed body parses, so the refusals below discriminate.
	base.Pattern = noOutboundCalls
	if _, err := parseAssertion(base); err != nil {
		t.Fatalf("control: a well-formed assertion body must parse, got %v", err)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"unknown field", `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","require":"absent","typo":1}`, "typo"},
		{"unknown edge type", `{"node_type":"function_declaration","edge_type":"INVOKES","direction":"out","require":"absent"}`, "edge_type"},
		{"unknown direction", `{"node_type":"function_declaration","edge_type":"CALLS","direction":"sideways","require":"absent"}`, "direction"},
		{"missing node type", `{"edge_type":"CALLS","direction":"out","require":"absent"}`, "node_type"},
		{"threshold field on structural check", `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","require":"absent","max_degree":3}`, "max_degree"},
		{"not json", `defer $X.Close()`, "well-formed"},
	} {
		c := base
		c.Pattern = tc.body
		if _, err := parseAssertion(c); err == nil {
			t.Errorf("%s: must be refused rather than defaulted or ignored", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
	// The exclusivity runs the other way too.
	thr := corpus.Check{ID: "chk-2", Type: corpus.CheckTopologyThreshold, Language: "go",
		Pattern: `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out","require":"absent"}`}
	if _, err := parseAssertion(thr); err == nil || !strings.Contains(err.Error(), "require") {
		t.Errorf("a structural field on a threshold check must be refused, got %v", err)
	}
	noBound := corpus.Check{ID: "chk-3", Type: corpus.CheckTopologyThreshold, Language: "go",
		Pattern: `{"node_type":"function_declaration","edge_type":"CALLS","direction":"out"}`}
	if _, err := parseAssertion(noBound); err == nil {
		t.Error("a threshold check with no threshold asserts nothing and must be refused")
	}
}

// populateCallsEdges is the INDEPENDENT measurement the fixture assertions rest
// on: it runs the fixture snippet through parser.Populate itself and counts the
// CALLS edges, so the expectation comes from outside the validator under test.
func populateCallsEdges(t *testing.T, src string) int {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	res, err := parser.Populate(context.Background(), "corpusscan-probe", dir)
	if err != nil {
		t.Fatalf("parser.Populate: %v", err)
	}
	n := 0
	for _, e := range res.Edges {
		if e.GetType() == string(kgtypes.EdgeCalls) {
			n++
		}
	}
	return n
}

// codeNode builds a code-graph function node.
func codeNode(id string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "function_declaration", SymbolName: id}
}
