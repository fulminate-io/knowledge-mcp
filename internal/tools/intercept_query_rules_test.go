// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// rulesFakeGc answers the query(type:"rule") read over the Execute carrier seam
// with a scripted set of rule nodes via the nodes_json carrier.
type rulesFakeGc struct {
	rules []*knowledgev1.Node
}

// Call satisfies the interface; the rules intercept routes through Execute.
func (g *rulesFakeGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (g *rulesFakeGc) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	resp := enginetest.ResponseWithNodes(g.rules...)
	resp.Total = int64(len(g.rules))
	return resp, nil
}

// seedRulesFixture returns the rules in the SAME order the captured
// goldens use (reverse-creation: gamma, beta, alpha). That matches
// the store's natural iteration order.
func seedRulesFixture() *rulesFakeGc {
	mkRule := func(id, name, desc, scope, enforcement string) *knowledgev1.Node {
		n := &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeRule), SymbolName: name,
			Source: "test", Status: "active",
			Description: desc, Summary: name + " rule summary",
		}
		kgtypes.SetValue(n, "scope", scope)
		kgtypes.SetValue(n, "enforcement", enforcement)
		return n
	}
	return &rulesFakeGc{
		rules: []*knowledgev1.Node{
			mkRule("000000000000000000000000000000a3", "rule-gamma", "rule c desc", "commits", "policy"),
			mkRule("000000000000000000000000000000a2", "rule-beta", "rule b desc", "pkg/", "review"),
			mkRule("000000000000000000000000000000a1", "rule-alpha", "rule a desc", "*.go", "lint"),
		},
	}
}

func TestInterceptQueryRules_Populated(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "rules")
	assert.Equal(t, want, got)
}

func TestInterceptQueryRules_Filtered(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule", "scope": "*.go"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "rules_filtered")
	assert.Equal(t, want, got)
}

func TestInterceptQueryRules_Empty(t *testing.T) {
	gc := &rulesFakeGc{rules: nil}
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "rules_empty")
	assert.Equal(t, want, got)
}

func TestInterceptQueryRules_NoMatch(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule", "scope": "zzz_nothing"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "rules_no_match")
	assert.Equal(t, want, got)
}

// TestInterceptQueryRules_JSON asserts the format:"json" branch returns the
// {graph, type, results, total} browse envelope (agent graph-explorer contract),
// while the default (no-format) caller still receives the human markdown. Reuses
// browseJSONEnvelope defined in intercept_query_decisions_test.go (same package).
func TestInterceptQueryRules_JSON(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule", "format": "json"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	var env browseJSONEnvelope
	require.NoError(t, json.Unmarshal([]byte(extractText(res)), &env),
		"format:json must return parseable browse JSON, got: %s", extractText(res))
	assert.Equal(t, "knowledge", env.Graph)
	assert.Equal(t, "rule", env.Type)
	assert.Equal(t, 3, env.Total)
	require.Len(t, env.Results, 3)
	// Fixture order: gamma, beta, alpha.
	assert.Equal(t, "000000000000000000000000000000a3", env.Results[0].ID)
	assert.Equal(t, "rule-gamma", env.Results[0].Name)
	assert.Equal(t, "rule", env.Results[0].Type)
	assert.Equal(t, "active", env.Results[0].Status)
	assert.Equal(t, "rule-alpha", env.Results[2].Name)

	// Default (no-format) caller MUST still receive the human markdown.
	mdArgs := mustMarshal(t, map[string]any{"type": "rule"})
	handledMD, resMD := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: mdArgs})
	require.True(t, handledMD)
	require.False(t, resMD.IsError)
	assert.Contains(t, extractText(resMD), "Rules (")
}

// TestInterceptQueryRules_JSON_Empty asserts the JSON branch serializes an empty
// result set as results:[] (total 0) rather than the "No rules recorded" prose —
// the empty markdown text would break the caller's JSON.parse.
func TestInterceptQueryRules_JSON_Empty(t *testing.T) {
	gc := &rulesFakeGc{rules: nil}
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule", "format": "json"})

	handled, res := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	var env browseJSONEnvelope
	require.NoError(t, json.Unmarshal([]byte(extractText(res)), &env),
		"empty result must still be parseable JSON, got: %s", extractText(res))
	assert.Equal(t, "rule", env.Type)
	assert.Equal(t, 0, env.Total)
	assert.Empty(t, env.Results)
}

func TestInterceptQueryRules_WrongType_FallsThrough(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "finding"})
	handled, _ := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryRules_WrongTool_FallsThrough(t *testing.T) {
	gc := seedRulesFixture()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "rule"})
	handled, _ := InterceptQueryRules(deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}
