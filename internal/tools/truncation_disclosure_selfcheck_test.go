// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"go/parser"
	"go/token"
	"testing"
)

// truncation_disclosure_selfcheck_test.go proves the census CLASSIFIER fires,
// rather than being green because it matches nothing. It parses synthetic
// in-memory source — never a scratch edit of real files — and asserts the
// verdict on each shape.
//
// THE APERTURE IS DELIBERATELY DIFFERENT FROM THE GATE'S. TestTruncationDisclosureCensus
// asks whether the scan is CLEAN; this asks whether it can ever be DIRTY. A
// census that cannot report a violation is a formality, and the way a census
// stops being able to report one is a member rule that quietly narrows.
//
// A CASE PER CLAUSE IS MANDATORY. An earlier design of this rule had only clause
// (a1), and that is exactly how the plan_tree arm went missing: it holds no
// ExecuteResponse, so an (a1)-only rule could not see it. A self-check
// exercising only (a1) would have stayed green over that hole. Both spellings of
// clause (a2) therefore get their own case, and so do the negative controls that
// keep the rule from matching everything.

func TestTruncationDisclosureCensus_SelfCheck(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSite   bool
		wantClause string
	}{{
		name: "(a1) an ExecuteResponse parameter",
		body: `func renderThing(resp *knowledgev1.ExecuteResponse) kgtools.ToolResult {
	return kgtools.TextResult("x")
}`,
		wantSite:   true,
		wantClause: clauseA1,
	}, {
		name: "(a1) a response from an ExecuteFn parameter",
		body: `func armThing(ctx context.Context, exec engine.ExecuteFn) kgtools.ToolResult {
	resp, err := exec(ctx, nil)
	if err != nil {
		return errorResult("boom")
	}
	return kgtools.TextResult(resp.String())
}`,
		wantSite:   true,
		wantClause: clauseA1,
	}, {
		name: "(a1) a response from a GraphCaller Execute",
		body: `func armThing(ctx context.Context, gc GraphCaller) kgtools.ToolResult {
	resp, _ := gc.Execute(ctx, nil)
	return kgtools.TextResult(resp.String())
}`,
		wantSite:   true,
		wantClause: clauseA1,
	}, {
		name: "(a2)(i) a verdict bound in a multi-assign off a call — the plan_tree shape",
		body: `func armThing(ctx context.Context, gc GraphCaller) (bool, kgtools.ToolResult) {
	nodes, edges, truncated, err := TraverseDescendantsWithEdges(ctx, gc, "id", "contains", 10)
	_, _, _ = nodes, edges, err
	return true, withTruncationNotice(kgtools.TextResult("x"), truncated, 0)
}`,
		wantSite:   true,
		wantClause: clauseA2,
	}, {
		name: "(a2)(ii) a verdict read off a struct a helper returned — the examine shape",
		body: `func armThing(ctx context.Context) (bool, kgtools.ToolResult) {
	data, found, err := composeInspectData(ctx, nil, "id")
	if err != nil || !found {
		return true, errorResult("nope")
	}
	return true, engine.WithTruncationNoticeFor(jsonResult(data), data.EdgesTruncated, 0)
}`,
		wantSite:   true,
		wantClause: clauseA2,
	}, {
		name: "NOT a site: the verdict arrives as a PARAMETER, not from a call",
		body: `func renderEnvelope(nodes []string, truncated bool) kgtools.ToolResult {
	return jsonResult(map[string]any{"nodes": nodes, "truncated": truncated})
}`,
		wantSite: false,
	}, {
		name: "NOT a site: holds a response but returns no ToolResult",
		body: `func decodeThing(resp *knowledgev1.ExecuteResponse) ([]string, bool, error) {
	return nil, resp.GetTruncated(), nil
}`,
		wantSite: false,
	}, {
		name: "NOT a site: renders a ToolResult but never sees a response or a verdict",
		body: `func renderHelp() kgtools.ToolResult {
	return kgtools.TextResult("help")
}`,
		wantSite: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := classifySnippet(t, tc.body)
			if len(facts) != 1 {
				t.Fatalf("synthetic snippet produced %d function facts, want 1", len(facts))
			}
			got := facts[0]
			if got.isSite != tc.wantSite {
				t.Fatalf("isSite = %v, want %v for body:\n%s", got.isSite, tc.wantSite, tc.body)
			}
			if tc.wantSite && got.clause != tc.wantClause {
				t.Errorf("clause = %q, want %q for body:\n%s", got.clause, tc.wantClause, tc.body)
			}
		})
	}
}

// TestTruncationDisclosureCensus_EmissionSelfCheck proves the json_carriers
// analysis can tell READING a verdict from EMITTING it. That distinction is the
// whole gate: renderTraversalResponse read resp.GetTruncated() for years while
// its JSON payload carried no key, and an analysis that scored a read as an
// emission would have reported that live gap clean.
func TestTruncationDisclosureCensus_EmissionSelfCheck(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantEmits      bool
		wantReadsVerd  bool
		taggedEnvelope map[string]bool
	}{{
		name: "reads the verdict but emits no key",
		body: `func renderThing(resp *knowledgev1.ExecuteResponse) kgtools.ToolResult {
	if resp.GetTruncated() {
		return errorResult("partial")
	}
	return jsonResult(map[string]any{"nodes": nil})
}`,
		wantEmits:     false,
		wantReadsVerd: true,
	}, {
		name: "emits the key as a map literal",
		body: `func renderThing(resp *knowledgev1.ExecuteResponse) kgtools.ToolResult {
	return jsonResult(map[string]any{"nodes": nil, "truncated": resp.GetTruncated()})
}`,
		wantEmits:     true,
		wantReadsVerd: true,
	}, {
		name: "emits the key as a locally declared struct tag",
		body: `func renderThing(truncated bool) kgtools.ToolResult {
	type envelope struct {
		Nodes     []string ` + "`json:\"nodes\"`" + `
		Truncated bool     ` + "`json:\"truncated\"`" + `
	}
	return jsonResult(envelope{Truncated: truncated})
}`,
		wantEmits:     true,
		wantReadsVerd: true,
	}, {
		name: "emits the key by composite-literalling a file-scope tagged envelope",
		body: `func renderThing(truncated bool) kgtools.ToolResult {
	return jsonResult(SearchJSONResponse{Truncated: truncated})
}`,
		wantEmits:      true,
		wantReadsVerd:  true,
		taggedEnvelope: map[string]bool{"SearchJSONResponse": true},
	}, {
		name: "CALLING a disclosure helper is not READING a verdict",
		body: `func armThing(resp *knowledgev1.ExecuteResponse) kgtools.ToolResult {
	return engine.WithTruncationNotice(jsonResult(map[string]any{"nodes": nil}), resp)
}`,
		// THE MECHANISM THIS PINS. ast.Inspect reaches a qualified callee's .Sel as
		// a bare Ident carrying the FUNCTION's name, so before calleeExprs marked it
		// too, `engine.WithTruncationNotice(...)` scored as a verdict READ. Every
		// disclosing site therefore looked like a reader, and json_carriers could
		// never DEMAND the CONSTANT BY CONSTRUCTION phrase of anyone — the escape
		// was unreachable rather than unused, which is a gate that cannot fail.
		wantEmits:     false,
		wantReadsVerd: false,
	}, {
		name: "a NEAR-MISS key is not the key",
		body: `func renderThing(resp *knowledgev1.ExecuteResponse) kgtools.ToolResult {
	return jsonResult(map[string]any{"excluded_truncated": true})
}`,
		wantEmits:     false,
		wantReadsVerd: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package p\n" + tc.body + "\n"
			file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
			if err != nil {
				t.Fatalf("parse synthetic snippet: %v", err)
			}
			tagged := tc.taggedEnvelope
			if tagged == nil {
				tagged = map[string]bool{}
			}
			facts := scanFileForDisclosureSites(fset, file, "synthetic.go", tagged)
			if len(facts) != 1 {
				t.Fatalf("synthetic snippet produced %d function facts, want 1", len(facts))
			}
			if facts[0].emitsKey != tc.wantEmits {
				t.Errorf("emitsKey = %v, want %v for body:\n%s", facts[0].emitsKey, tc.wantEmits, tc.body)
			}
			if facts[0].readsVerd != tc.wantReadsVerd {
				t.Errorf("readsVerd = %v, want %v for body:\n%s", facts[0].readsVerd, tc.wantReadsVerd, tc.body)
			}
		})
	}
}

// classifySnippet parses one synthetic function and returns the scanner's facts.
func classifySnippet(t *testing.T, body string) []fnFacts {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", "package p\n"+body+"\n", 0)
	if err != nil {
		t.Fatalf("parse synthetic snippet: %v", err)
	}
	return scanFileForDisclosureSites(fset, file, "synthetic.go", map[string]bool{})
}
