// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	goldenSubjectID  = "svc/handler.go:Serve"
	testCallsGroupKy = "svc/handler_test.go:41:TEST_CALLS:Run"
)

// analyzeZeroTestCallsFixture is the CHARACTERIZATION fixture: plain callers, a
// three-candidate ambiguous group, and a callee — every render limb the analyze
// arm has — with NOT ONE test-call edge. Its golden was captured from the
// handler BEFORE this change and must stay byte-identical after it.
func analyzeZeroTestCallsFixture() *analyzeFake {
	const ambSource = "svc/handler.go:Dispatch"
	const groupKey = "svc/handler.go:88:CALLS:Run"
	return &analyzeFake{
		subject: knowledgev1.Node{
			Id: goldenSubjectID, SymbolName: "Serve", Type: "function",
			FilePath: "svc/handler.go", StartLine: 10, EndLine: 40, Signature: "func Serve() error",
		},
		callers: []knowledgev1.Node{
			{Id: "svc/main.go:Main", SymbolName: "Main", Type: "function", FilePath: "svc/main.go", StartLine: 3},
			{Id: "p/a.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/a.go", StartLine: 11, Signature: "func Run() error"},
			{Id: "p/b.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/b.go", StartLine: 22, Signature: "func Run(n int)"},
			{Id: "p/c.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/c.go", StartLine: 33, Signature: "func Run(s string)"},
		},
		callerEdges: []knowledgev1.Edge{
			{FromId: "svc/main.go:Main", ToId: goldenSubjectID, Type: string(kgtypes.EdgeCalls)},
			{FromId: ambSource, ToId: "p/a.go:Run", Type: string(kgtypes.EdgeCalls), Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
			{FromId: ambSource, ToId: "p/b.go:Run", Type: string(kgtypes.EdgeCalls), Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
			{FromId: ambSource, ToId: "p/c.go:Run", Type: string(kgtypes.EdgeCalls), Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 1.0 / 3.0},
		},
		callees: []knowledgev1.Node{
			{Id: "svc/store.go:Load", SymbolName: "Load", Type: "function", FilePath: "svc/store.go", StartLine: 60},
		},
		calleeEdges: []knowledgev1.Edge{
			{FromId: goldenSubjectID, ToId: "svc/store.go:Load", Type: string(kgtypes.EdgeCalls)},
		},
	}
}

// TestComposeAnalyzeNode_ZeroTestCallsIsByteIdentical is the characterization
// guard. The golden was produced by the handler as it stood before analyze read
// any test-call traffic, so a diff here means the opt-in changed what a user
// sees on a graph that carries no TEST_CALLS edge — which is every graph until
// the collector's output is re-collected.
func TestComposeAnalyzeNode_ZeroTestCallsIsByteIdentical(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "analyze_zero_test_calls.golden"))
	require.NoError(t, err, "the pre-change golden is checked in")

	f := analyzeZeroTestCallsFixture()
	got := textBodyTools(composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: goldenSubjectID, Repo: "knowledge"}))

	assert.Equal(t, string(want), got,
		"a graph with no TEST_CALLS edge renders exactly as it did before analyze opted in")
}

// analyzeBothEdgeTypesFixture carries production AND test call traffic over the
// same subject, with DISTINCT symbols on each side so a merge of one list into
// the other is visible rather than plausible.
func analyzeBothEdgeTypesFixture() *analyzeFake {
	f := &analyzeFake{
		subject: knowledgev1.Node{
			Id: goldenSubjectID, SymbolName: "Serve", Type: "function",
			FilePath: "svc/handler.go", StartLine: 10, EndLine: 40, Signature: "func Serve() error",
		},
		callers: []knowledgev1.Node{
			{Id: "svc/main.go:Main", SymbolName: "Main", Type: "function", FilePath: "svc/main.go", StartLine: 3},
		},
		callerEdges: []knowledgev1.Edge{
			{FromId: "svc/main.go:Main", ToId: goldenSubjectID, Type: string(kgtypes.EdgeCalls)},
		},
		callees: []knowledgev1.Node{
			{Id: "svc/store.go:Load", SymbolName: "Load", Type: "function", FilePath: "svc/store.go", StartLine: 60},
		},
		calleeEdges: []knowledgev1.Edge{
			{FromId: goldenSubjectID, ToId: "svc/store.go:Load", Type: string(kgtypes.EdgeCalls)},
		},
		testCallers: []knowledgev1.Node{
			{Id: "svc/handler_test.go:TestServe", SymbolName: "TestServe", Type: "test_block", FilePath: "svc/handler_test.go", StartLine: 12},
		},
		testCallerEdges: []knowledgev1.Edge{
			{FromId: "svc/handler_test.go:TestServe", ToId: goldenSubjectID, Type: string(kgtypes.EdgeTestCalls)},
		},
		testCallees: []knowledgev1.Node{
			{Id: "svc/fixture.go:Seed", SymbolName: "Seed", Type: "function", FilePath: "svc/fixture.go", StartLine: 90},
		},
		testCalleeEdges: []knowledgev1.Edge{
			{FromId: goldenSubjectID, ToId: "svc/fixture.go:Seed", Type: string(kgtypes.EdgeTestCalls)},
		},
	}
	return f
}

// TestComposeAnalyzeNode_TestCallsRenderSeparately is the opt-in's own test: the
// production lists keep exactly what they had, the test traffic lands in its own
// carriers, and both render as their own labeled sections.
func TestComposeAnalyzeNode_TestCallsRenderSeparately(t *testing.T) {
	f := analyzeBothEdgeTypesFixture()
	res := composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: goldenSubjectID, Repo: "knowledge"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)

	t.Run("analyze_requests_the_test_calls_edge_type", func(t *testing.T) {
		var sawCalls, sawTestCalls bool
		for _, types := range f.requestedEdgeTypes {
			if slices.Contains(types, string(kgtypes.EdgeCalls)) {
				sawCalls = true
			}
			if slices.Contains(types, string(kgtypes.EdgeTestCalls)) {
				sawTestCalls = true
			}
		}
		assert.True(t, sawCalls, "KNOWN POSITIVE: the production CALLS fetch still happens")
		assert.True(t, sawTestCalls, "analyze fetches the TEST_CALLS edge type")
	})

	t.Run("production_sections_are_unchanged", func(t *testing.T) {
		assert.Contains(t, body, "## Callers (1)", "the production count counts production callers only")
		assert.Contains(t, body, "### Main (function) — svc/main.go:3")
		assert.Contains(t, body, "## Callees (1)")
		assert.Contains(t, body, "### Load (function) — svc/store.go:60")
		// THE MERGE CATCHER: a test caller folded into the production list would
		// move these counts to 2 and put the test symbol above the test section.
		assert.NotContains(t, body, "## Callers (2)")
		assert.NotContains(t, body, "## Callees (2)")
	})

	t.Run("test_sections_render_separately", func(t *testing.T) {
		assert.Contains(t, body, "## Test Callers (1)")
		assert.Contains(t, body, "### TestServe (test_block) — svc/handler_test.go:12")
		assert.Contains(t, body, "## Test Callees (1)")
		assert.Contains(t, body, "### Seed (function) — svc/fixture.go:90")
	})

	t.Run("test_symbols_appear_only_under_the_test_sections", func(t *testing.T) {
		// Ordering proof: each test symbol occurs AFTER its section header and
		// never before it, so "separately" is positional, not just present.
		testCallerHdr := strings.Index(body, "## Test Callers (")
		require.GreaterOrEqual(t, testCallerHdr, 0)
		assert.Equal(t, 1, strings.Count(body, "TestServe"), "the test caller is listed exactly once")
		assert.Greater(t, strings.Index(body, "TestServe"), testCallerHdr,
			"the test caller is listed under the test-callers section, not in the production list")

		testCalleeHdr := strings.Index(body, "## Test Callees (")
		require.GreaterOrEqual(t, testCalleeHdr, 0)
		assert.Equal(t, 1, strings.Count(body, "Seed"), "the test callee is listed exactly once")
		assert.Greater(t, strings.Index(body, "Seed"), testCalleeHdr)
	})
}

// TestComposeAnalyzeNode_TestCallsGroupsRenderInTheTestSection pins that the
// test side gets the SAME multi-candidate treatment the production side has: an
// ambiguous group renders as one block and its candidates are not also listed as
// plain test callers.
func TestComposeAnalyzeNode_TestCallsGroupsRenderInTheTestSection(t *testing.T) {
	const ambSource = "svc/handler_test.go:TestAmb"
	f := analyzeBothEdgeTypesFixture()
	f.testCallers = append(f.testCallers,
		knowledgev1.Node{Id: "t/a.go:Run", SymbolName: "Run", Type: "function", FilePath: "t/a.go", StartLine: 11},
		knowledgev1.Node{Id: "t/b.go:Run", SymbolName: "Run", Type: "function", FilePath: "t/b.go", StartLine: 22},
	)
	f.testCallerEdges = append(f.testCallerEdges,
		knowledgev1.Edge{FromId: ambSource, ToId: "t/a.go:Run", Type: string(kgtypes.EdgeTestCalls), Method: kgtypes.EdgeMethodAmbiguousName, Evidence: testCallsGroupKy, Confidence: 0.5},
		knowledgev1.Edge{FromId: ambSource, ToId: "t/b.go:Run", Type: string(kgtypes.EdgeTestCalls), Method: kgtypes.EdgeMethodAmbiguousName, Evidence: testCallsGroupKy, Confidence: 0.5},
	)

	body := textBodyTools(composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: goldenSubjectID, Repo: "knowledge"}))

	assert.Equal(t, 1, strings.Count(body, "one of 2 candidates"), "the test-side group renders as ONE block")
	assert.Contains(t, body, "## Test Callers (1)",
		"the test-callers count counts the entries it lists, not those plus the group's candidates")
	for _, cand := range []string{"t/a.go", "t/b.go"} {
		assert.NotContains(t, body, "### Run (function) — "+cand,
			"candidate %s renders inside its group block, never as a plain test caller", cand)
	}
	// THE PRODUCTION SIDE IS UNTOUCHED: a test-side group must not suppress or
	// re-count anything in the production sections.
	assert.Contains(t, body, "## Callers (1)")
	assert.Contains(t, body, "### Main (function) — svc/main.go:3")
}

// TestComposeAnalyzeNode_NoTestSectionsWhenNoTestTraffic states the empty-state
// convention explicitly: the test sections are OMITTED, not rendered with a
// zero count. Until a repo is re-collected no TEST_CALLS edge exists at all, so
// "No test callers found." would report an absence the graph cannot yet know.
func TestComposeAnalyzeNode_NoTestSectionsWhenNoTestTraffic(t *testing.T) {
	f := analyzeZeroTestCallsFixture()
	body := textBodyTools(composeAnalyzeNode(context.Background(), f.exec, analyzeNodeArgs{Graph: "code", ID: goldenSubjectID, Repo: "knowledge"}))

	assert.NotContains(t, body, "## Test Callers")
	assert.NotContains(t, body, "## Test Callees")
	// KNOWN POSITIVE: the production sections DID render, so the two assertions
	// above are about the test sections' absence and not about an empty render.
	assert.Contains(t, body, "## Callers (1)")
	assert.Contains(t, body, "## Callees (1)")
}
