// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_list_test.go pins the two properties of the inventory that no
// source grep can see: that the contract's four return rows stay four distinct
// lanes, and that the render walks fixtures as well as checks.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// listChecksBody drives manage_checks(list) and returns the rendering.
func listChecksBody(t *testing.T, nodes ...*knowledgev1.Node) string {
	t.Helper()
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: newChecksGraphFake(nodes...)}
	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: json.RawMessage(`{"operation":"list"}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "list must render rather than refuse: %s", res.Content[0].Text)
	return res.Content[0].Text
}

// listFixtureNode builds an example node in the checks graph.
func listFixtureNode(id, name, content string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeExample),
		SymbolName: name,
		Content:    content,
		Metadata:   map[string]string{corpus.MetaLanguage: "go"},
	}
}

// TestManageChecks_ListRendersBindingsAndUnboundFixtures asserts the render walks
// BOTH node types: it names each bound pair AND the example node no check binds.
//
// THE UNBOUND ARM IS THE DISCRIMINATING ONE. A renderer that walked only checks
// satisfies every binding assertion while leaving orphaned fixtures invisible,
// which is the state this operation exists to expose — no executor reaches them,
// and no ranked search will either.
func TestManageChecks_ListRendersBindingsAndUnboundFixtures(t *testing.T) {
	check := &knowledgev1.Node{
		Id:         "go:bound-check",
		Type:       string(kgtypes.NodeFinding),
		SymbolName: "a-bound-check",
		Metadata: map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckAstPattern),
			corpus.MetaSeverity:    string(foundation.SeverityWarning),
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "fmt.Println($X)",
			corpus.MetaFixtureBad:  "go:bound-bad",
			corpus.MetaFixtureGood: "go:bound-good",
		},
	}
	bad := listFixtureNode("go:bound-bad", "the-bad-example", "package p\n\nfunc f() { fmt.Println(1) }\n")
	good := listFixtureNode("go:bound-good", "the-good-example", "package p\n\nfunc f() {}\n")
	orphan := listFixtureNode("go:orphan-fixture", "the-orphan-example", "package p\n")

	body := listChecksBody(t, check, bad, good, orphan)

	// The bound pair is rendered with BOTH fixture ids AND their resolved names,
	// asserted per fixture rather than as a count of two — a renderer that emitted
	// the same fixture twice would satisfy a count.
	assert.Contains(t, body, "go:bound-bad", "the bad fixture binding must be rendered")
	assert.Contains(t, body, "the-bad-example", "the bad fixture's name must be resolved, not left as a bare id")
	assert.Contains(t, body, "go:bound-good", "the good fixture binding must be rendered")
	assert.Contains(t, body, "the-good-example", "the good fixture's name must be resolved, not left as a bare id")

	// The orphan appears in the unbound lane and NOT among the bound ones.
	assert.Contains(t, body, laneUnboundFixt+": 1",
		"exactly one example node is bound by nothing, and the lane must say so")
	assert.Contains(t, body, "the-orphan-example", "the unbound fixture must be named")

	// KNOWN POSITIVE for the unbound count: the two bound fixtures must NOT be
	// counted as unbound. Without this leg a renderer that listed every example as
	// unbound would satisfy the assertions above.
	assert.NotContains(t, body, laneUnboundFixt+": 3",
		"a bound fixture must never be reported as bound by no check")
}

// TestManageChecks_ListKeepsTheLLMOnlyAndUnauthoredLanesDistinct is the row-2 /
// row-3 collapse guard the contract warns about.
//
// NO SOURCE GREP DISTINGUISHES THE COLLAPSE FROM CORRECT CODE: a reader that
// writes `if !isCheck { continue }` merges the accepted-llm_only lane into the
// silent skip, and the resulting file still parses, still compiles and still
// renders every executable check. Only driving both node shapes through and
// requiring two DIFFERENT lanes catches it.
func TestManageChecks_ListKeepsTheLLMOnlyAndUnauthoredLanesDistinct(t *testing.T) {
	llmOnly := &knowledgev1.Node{
		Id:         "go:llm-only-entry",
		Type:       string(kgtypes.NodeFinding),
		SymbolName: "prose-needing-judgment",
		Metadata: map[string]string{
			corpus.MetaLLMOnly:  "true",
			corpus.MetaLanguage: "go",
		},
	}
	unauthored := &knowledgev1.Node{
		Id:         "go:unauthored-entry",
		Type:       string(kgtypes.NodeFinding),
		SymbolName: "neither-key-present",
		Metadata:   map[string]string{corpus.MetaLanguage: "go"},
	}

	body := listChecksBody(t, llmOnly, unauthored)

	assert.Contains(t, body, laneLLMOnly+": 1",
		"an accepted llm_only node is its own reportable lane, never folded into the skip branch")
	assert.Contains(t, body, "go:llm-only-entry")
	assert.Contains(t, body, laneUnauthored+": 1",
		"a node carrying neither contract key is incompletely authored, and distinct from llm_only")
	assert.Contains(t, body, "go:unauthored-entry")

	// THE COLLAPSE THIS CATCHES, asserted directly: the two must not land in one
	// lane. A merge would show one lane at 2 and the other at 0.
	assert.NotContains(t, body, laneLLMOnly+": 2", "the two lanes must not be merged")
	assert.NotContains(t, body, laneUnauthored+": 2", "the two lanes must not be merged")
	assert.NotContains(t, body, laneLLMOnly+": 0", "the llm_only lane must not be emptied into the skip")
}

// TestManageChecks_ListReportsAContractRefusalVerbatim covers row 4 — the lane the
// other two tests do not reach.
//
// A node the contract REFUSES is neither a check nor prose nor incompletely
// authored: it is a corpus an operator must fix, and the fix is only actionable
// if the contract's own message survives to the reader.
func TestManageChecks_ListReportsAContractRefusalVerbatim(t *testing.T) {
	// llm_only alongside a check key is the contract's coerced-check refusal.
	refused := &knowledgev1.Node{
		Id:         "go:coerced-entry",
		Type:       string(kgtypes.NodeFinding),
		SymbolName: "prose-coerced-into-a-check",
		Metadata: map[string]string{
			corpus.MetaLLMOnly:    "true",
			corpus.MetaLanguage:   "go",
			corpus.MetaCheckType:  string(corpus.CheckAstPattern),
			corpus.MetaDSLPattern: "fmt.Println($X)",
		},
	}
	body := listChecksBody(t, refused)

	assert.Contains(t, body, laneUnadmitted+": 1", "a node the contract refuses is its own lane")
	assert.Contains(t, body, "go:coerced-entry")
	// The contract's own wording, relayed rather than replaced by a generic
	// "invalid" — it names the key that makes the node inadmissible.
	assert.Contains(t, body, corpus.MetaCheckType,
		"the contract's message must survive verbatim, or the operator cannot act on it")
}
