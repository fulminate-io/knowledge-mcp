// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestCompileSearch_KnowledgeSingleQuery(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"x","graph":"knowledge"}`))
	require.True(t, ok)
	require.NotNil(t, req)
	q := req.GetQuery()
	require.NotNil(t, q)
	assert.Equal(t, []string{"x"}, q.GetQueries())
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_SEARCH, q.GetReturnMode())
	// graph=knowledge maps to the knowledge default (target.Graph == "knowledge"
	// — buildTarget passes the value through; "knowledge" is non-empty so the
	// target is set but the engine treats "knowledge" as the default graph).
	assert.Equal(t, "knowledge", req.GetTarget().GetGraph())
}

func TestCompileSearch_DefaultGraphEmptyTarget(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"x"}`))
	require.True(t, ok)
	// graph absent → buildTarget returns nil → engine reads it as knowledge.
	assert.Nil(t, req.GetTarget())
	assert.Empty(t, req.GetTarget().GetGraph())
}

func TestCompileSearch_MultiQueryOnePlan(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"a","queries":["b","c","a"],"graph":"knowledge"}`))
	require.True(t, ok)
	q := req.GetQuery()
	// ONE plan with repeated, order-preserving, deduped queries — NOT N plans.
	assert.Equal(t, []string{"a", "b", "c"}, q.GetQueries())
}

func TestCompileSearch_QueryVectorOneEntry(t *testing.T) {
	vec := make([]byte, 32)
	for i := range vec {
		vec[i] = byte(i)
	}
	enc := base64.StdEncoding.EncodeToString(vec)
	args := map[string]any{"query": "x", "graph": "knowledge", "query_vector": enc}
	raw, err := json.Marshal(args)
	require.NoError(t, err)

	req, ok := compileSearch(raw)
	require.True(t, ok)
	q := req.GetQuery()
	require.Len(t, q.GetQueryVecs(), 1, "one decoded vector")
	assert.Equal(t, vec, q.GetQueryVecs()[0])
}

func TestCompileSearch_SingleTypeFilter(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"x","graph":"knowledge","types":["finding"]}`))
	require.True(t, ok)
	assert.Equal(t, []string{"finding"}, req.GetQuery().GetSelection().GetNodeTypes())
}

// TestCompileSearch_MultiTypeFilter asserts a multi-type search now COMPILES
// (T2.4c): both types ride the node_types carrier; the engine post-filters.
func TestCompileSearch_MultiTypeFilter(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"x","graph":"knowledge","types":["finding","decision"]}`))
	require.True(t, ok, "multi-type search is reducible (T2.4c)")
	assert.Equal(t, []string{"finding", "decision"}, req.GetQuery().GetSelection().GetNodeTypes())
}

// TestCompileSearch_ResourceTypeFilter asserts a cloud resource_type search
// COMPILES (still reducible) but carries NO resource_type signal on the plan:
// the resource_type carrier was removed (an OP_PREFIX predicate is
// inert on a QSearch post-rank), so the client post-filters the
// rendered SearchList instead. The compiled plan is a plain QSearch; the
// resource_type prefix is applied in renderSearchTool, not on the wire.
func TestCompileSearch_ResourceTypeFilter(t *testing.T) {
	req, ok := compileSearch(json.RawMessage(`{"query":"x","graph":"cloud","account":"acct","resource_type":"ec2"}`))
	require.True(t, ok, "cloud resource_type search is reducible (routing unchanged)")
	assert.Equal(t, []string{"x"}, req.GetQuery().GetQueries(), "the query rides the plan as a QSearch")
	// No resource_type metadata predicate is emitted (it would be inert on a search).
	for _, mp := range req.GetQuery().GetSelection().GetMetadataPredicates() {
		assert.NotEqual(t, "resource_type", mp.GetKey(),
			"no resource_type predicate is emitted into the QSearch plan")
	}
}

func TestCompileSearch_DenyCases(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"code graph", `{"query":"x","graph":"code"}`},
		{"empty query", `{"graph":"knowledge"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileSearch(json.RawMessage(tc.args))
			assert.False(t, ok, "%s must fall through to legacy", tc.name)
			assert.Nil(t, req)
		})
	}
}

func TestCompileSearch_RerankTriState(t *testing.T) {
	t.Run("absent → nil", func(t *testing.T) {
		req, ok := compileSearch(json.RawMessage(`{"query":"x"}`))
		require.True(t, ok)
		assert.Nil(t, req.GetQuery().Rerank)
	})
	t.Run("explicit false", func(t *testing.T) {
		req, ok := compileSearch(json.RawMessage(`{"query":"x","rerank":false}`))
		require.True(t, ok)
		require.NotNil(t, req.GetQuery().Rerank)
		assert.False(t, req.GetQuery().GetRerank())
	})
}

func TestCompileSearch_OtherReducibleGraphs(t *testing.T) {
	for _, g := range []string{"practice", "cloud", "cicd", "linkage", "web", "pdf"} {
		t.Run(g, func(t *testing.T) {
			args := map[string]any{"query": "x", "graph": g}
			if g == "practice" {
				args["language"] = "go"
			}
			if g == "cloud" || g == "cicd" {
				args["account"] = "acct"
			}
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			req, ok := compileSearch(raw)
			require.True(t, ok, "%s search is reducible", g)
			assert.Equal(t, g, req.GetTarget().GetGraph())
		})
	}
}
