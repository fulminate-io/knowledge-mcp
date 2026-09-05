// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// seededGraph is one addressable graph instance the fake serves.
type seededGraph struct {
	nodes []*knowledgev1.Node
	edges []*knowledgev1.Edge
}

// fakeCaller is a scripted foundation.GraphCaller that serves seeded graphs and
// RECORDS every request. Assertions are made on the captured requests rather
// than only on what came back: a fake that ignored the target entirely would
// satisfy a return-value-only assertion while proving nothing about the read.
type fakeCaller struct {
	graphs map[string]*seededGraph
	reqs   []*knowledgev1.ExecuteRequest
}

func newFakeCaller() *fakeCaller {
	return &fakeCaller{graphs: map[string]*seededGraph{}}
}

// seed installs nodes (and optionally edges) under one graph address.
func (f *fakeCaller) seed(scope string, nodes []*knowledgev1.Node, edges []*knowledgev1.Edge) *fakeCaller {
	f.graphs[scope] = &seededGraph{nodes: nodes, edges: edges}
	return f
}

// refuseUnconsumedSelectorFields makes the fake REFUSE a selector field the
// target family does not consume, exactly as the real server does.
//
// IGNORING SUCH A FIELD IS THE FAILURE MODE, NOT A CONVENIENCE. scopeKey below
// reads Language, Repo and Name and is blind to Account and Branch, so a
// selector carrying a field its family cannot honor still resolved to a seeded
// graph and every test stayed green — while the same request against the real
// server is REFUSED. That is a fake that agrees with a broken client, which is
// worse than no fake: it is precisely how a language leaked onto the checks
// selector through a suite that had an assertion for it.
//
// The consumed field is DERIVED from graphsel, the same switch the production
// builders use, so this cannot drift from what the client actually emits.
func refuseUnconsumedSelectorFields(t *knowledgev1.GraphSelector) error {
	if t == nil {
		return nil
	}
	gt := kgtypes.GraphType(t.GetGraph())
	consumed := ""
	if !graphsel.AddressesOneGraph(gt) {
		switch graphsel.InstanceField(gt) {
		case graphsel.FieldRepo:
			consumed = "repo"
		case graphsel.FieldAccount:
			consumed = "account"
		case graphsel.FieldLanguage:
			consumed = "language"
		default:
			consumed = "name"
		}
	}
	for _, f := range []struct{ name, val string }{
		{"repo", t.GetRepo()},
		{"account", t.GetAccount()},
		{"name", t.GetName()},
		{"language", t.GetLanguage()},
		{"branch", t.GetBranch()},
	} {
		if f.val == "" || f.name == consumed {
			continue
		}
		// code carries its branch alongside its repo; every other family consumes
		// exactly one instance field.
		if f.name == "branch" && gt == kgtypes.GraphCode {
			continue
		}
		return fmt.Errorf("fake: graph=%q does not consume %s=%q — the real server refuses this selector", gt, f.name, f.val)
	}
	return nil
}

// scopeKey renders a request's target the same way seed() addresses it.
func scopeKey(t *knowledgev1.GraphSelector) string {
	switch {
	case t.GetLanguage() != "":
		return t.GetGraph() + "/" + t.GetLanguage()
	case t.GetRepo() != "":
		return t.GetGraph() + "/" + t.GetRepo()
	case t.GetName() != "":
		return t.GetGraph() + "/" + t.GetName()
	default:
		return t.GetGraph()
	}
}

func (f *fakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.reqs = append(f.reqs, req)
	if err := refuseUnconsumedSelectorFields(req.GetTarget()); err != nil {
		return nil, err
	}
	g := f.graphs[scopeKey(req.GetTarget())]
	resp := &knowledgev1.ExecuteResponse{}
	if g == nil {
		return resp, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		want := map[string]bool{}
		for _, id := range q.GetIds() {
			want[id] = true
		}
		for i := range g.edges {
			e := g.edges[i]
			if want[e.GetFromId()] || want[e.GetToId()] {
				resp.Edges = append(resp.Edges, e)
			}
		}
		return resp, nil
	}
	resp.Nodes = g.browse(q)
	return resp, nil
}

// matchesMetaPredicates models the server's MetadataPredicate evaluation: OP_EXISTS
// tests key presence, anything else tests equality on the value. Predicates are
// AND-ed, and an empty predicate list matches everything.
//
// THE FAKE MUST ACTUALLY FILTER, and this is the one place where a lenient fake
// would be worse than no fake at all. Language scoping in the single checks graph
// IS a metadata predicate — a fake that ignored predicates would hand every
// language's checks back to every scan, so the language-scoping test would be
// asserting against a reader the server would never produce, and the negative leg
// would be measuring the fake rather than the code.
func matchesMetaPredicates(n *knowledgev1.Node, preds []*knowledgev1.MetadataPredicate) bool {
	for _, p := range preds {
		got, ok := n.GetMetadata()[p.GetKey()]
		if !ok {
			return false
		}
		if p.GetOp() == knowledgev1.MetadataPredicate_OP_EXISTS {
			continue
		}
		if got != p.GetValue() {
			return false
		}
	}
	return true
}

// browse models the server's type-index browse plus the keyset cursor closely
// enough that a capped read is observable — a fake returning every seeded node
// verbatim would be green whether or not the drain paged correctly.
func (g *seededGraph) browse(q *knowledgev1.QueryPlan) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		if t := q.GetSelection().GetNodeType(); t != "" && n.GetType() != t {
			continue
		}
		if !matchesMetaPredicates(n, q.GetSelection().GetMetadataPredicates()) {
			continue
		}
		out = append(out, n)
	}
	if q.AfterId != nil {
		sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
		if cursor := q.GetAfterId(); cursor != "" {
			kept := out[:0]
			for _, n := range out {
				if n.GetId() > cursor {
					kept = append(kept, n)
				}
			}
			out = kept
		}
	}
	if lim := int(q.GetLimit()); lim > 0 && len(out) > lim {
		out = out[:lim]
	}
	return out
}

// requestedNodeTypes lists the Selection.NodeType of every recorded browse whose
// target matches scope, in request order.
func (f *fakeCaller) requestedNodeTypes(scope string) []string {
	var out []string
	for _, r := range f.reqs {
		if scopeKey(r.GetTarget()) != scope {
			continue
		}
		if t := r.GetQuery().GetSelection().GetNodeType(); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- seeding helpers -------------------------------------------------------

// checkNode builds a checks-graph finding node carrying check metadata.
func checkNode(id, symbolName, description string, md map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:          id,
		Type:        string(kgtypes.NodeFinding),
		SymbolName:  symbolName,
		Description: description,
		Metadata:    md,
	}
}

// exampleNode builds a checks-graph example node carrying fixture source,
// labeled for Go (the language every other helper in this file defaults to).
//
// THE LANGUAGE LABEL IS NOT DECORATION. In the single checks graph the corpus
// read narrows BOTH node types by a language metadata predicate, so a fixture
// with no label is invisible to every scan and its check fails to resolve it.
// Use exampleNodeLang to build a fixture for any other language.
func exampleNode(id, content string) *knowledgev1.Node {
	return exampleNodeLang(id, content, "go")
}

// exampleNodeLang builds a checks-graph example node labeled for a specific
// language, for the cross-language scoping tests.
func exampleNodeLang(id, content, language string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:       id,
		Type:     string(kgtypes.NodeExample),
		Content:  content,
		Metadata: map[string]string{corpus.MetaLanguage: language},
	}
}

// astCheckMeta is a well-formed ast_pattern check's metadata.
func astCheckMeta(pattern, severity, badID, goodID string) map[string]string {
	return map[string]string{
		corpus.MetaCheckType:   string(corpus.CheckAstPattern),
		corpus.MetaSeverity:    severity,
		corpus.MetaLanguage:    "go",
		corpus.MetaDSLPattern:  pattern,
		corpus.MetaFixtureBad:  badID,
		corpus.MetaFixtureGood: goodID,
	}
}

// graphCheckMeta is a well-formed graph-shaped check's metadata.
func graphCheckMeta(checkType corpus.CheckType, body, severity, badID, goodID string) map[string]string {
	return map[string]string{
		corpus.MetaCheckType:   string(checkType),
		corpus.MetaSeverity:    severity,
		corpus.MetaLanguage:    "go",
		corpus.MetaDSLPattern:  body,
		corpus.MetaFixtureBad:  badID,
		corpus.MetaFixtureGood: goodID,
	}
}

// scanRequest builds a well-formed Request against the given caller and repo
// root. Tests override individual fields as needed.
func scanRequest(gc foundation.GraphCaller, repo, root string) foundation.Request {
	return foundation.Request{
		Caller:   gc,
		Graph:    kgtypes.GraphCode,
		Name:     repo,
		RepoRoot: root,
		Language: "go",
	}
}

// findingsByTitlePrefix counts findings whose Title starts with prefix.
func findingsByTitlePrefix(findings []foundation.Finding, prefix string) []foundation.Finding {
	var out []foundation.Finding
	for _, f := range findings {
		if len(f.Title) >= len(prefix) && f.Title[:len(prefix)] == prefix {
			out = append(out, f)
		}
	}
	return out
}

// matchFindings returns the findings that flag a site — everything carrying a
// check_id whose title is not one of this analyzer's own disclosures.
func matchFindings(findings []foundation.Finding) []foundation.Finding {
	var out []foundation.Finding
	for _, f := range findings {
		if f.Severity == foundation.SeverityCritical && len(findingsByTitlePrefix([]foundation.Finding{f}, RefusalPrefixUnvalidated)) > 0 {
			continue
		}
		if len(findingsByTitlePrefix([]foundation.Finding{f}, RefusalPrefixEnvironment)) > 0 {
			continue
		}
		if len(findingsByTitlePrefix([]foundation.Finding{f}, TruncationPrefixCheck)) > 0 {
			continue
		}
		if f.Title == TruncationTitleRun || f.Title == DisclosureTitleLLMOnly || f.Title == DisclosureTitleTestFiles {
			continue
		}
		out = append(out, f)
	}
	return out
}

// runScan drives the analyzer end to end and fails the test on an unexpected
// error, returning the findings.
func runScan(t *testing.T, req foundation.Request) []foundation.Finding {
	t.Helper()
	findings, err := CorpusScanAnalyzer{}.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	return findings
}

// runScanErr drives the analyzer and returns only its error, for the cases that
// assert on refusal rather than on findings.
func runScanErr(req foundation.Request) error {
	_, err := CorpusScanAnalyzer{}.Run(context.Background(), req)
	return err
}

// TestFakeRefusesUnconsumedSelectorFields pins the fake's own guard, because a
// guard nothing exercises is indistinguishable from no guard.
//
// IT CANNOT BE PINNED THROUGH THE READ PATH. Injecting a name into the corpus
// read's selector proves nothing: graphsel already refuses to put a name on a
// FieldNone family, so the value never reaches the target and the fake is never
// asked. The guard exists for the fields graphsel WOULD carry if a future
// builder set them directly — Account and Branch, which scopeKey is blind to —
// so it is asserted against hand-built selectors, which is the only shape that
// reaches it.
func TestFakeRefusesUnconsumedSelectorFields(t *testing.T) {
	// CONTROL: the shape the real read path builds is ACCEPTED. Without it, a
	// guard that refused everything would satisfy every rejection below.
	if err := refuseUnconsumedSelectorFields(
		&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphChecks)}); err != nil {
		t.Fatalf("control: the selector a real checks read builds must be accepted, got %v", err)
	}
	if err := refuseUnconsumedSelectorFields(
		&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: "r", Branch: "b"}); err != nil {
		t.Fatalf("control: code consumes both repo and branch, got %v", err)
	}

	for _, tc := range []struct {
		name string
		sel  *knowledgev1.GraphSelector
	}{
		{"checks with a language", &knowledgev1.GraphSelector{Graph: "checks", Language: "go"}},
		{"checks with a name", &knowledgev1.GraphSelector{Graph: "checks", Name: "go"}},
		{"checks with an account", &knowledgev1.GraphSelector{Graph: "checks", Account: "acct"}},
		// The two scopeKey is blind to, which is why the guard exists at all.
		{"practice with a branch", &knowledgev1.GraphSelector{Graph: "practice", Language: "go", Branch: "b"}},
		{"practice with an account", &knowledgev1.GraphSelector{Graph: "practice", Language: "go", Account: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := refuseUnconsumedSelectorFields(tc.sel); err == nil {
				t.Error("a selector field the family does not consume must be REFUSED — the real server refuses it, " +
					"and a fake that serves it anyway agrees with a broken client")
			}
		})
	}
}
