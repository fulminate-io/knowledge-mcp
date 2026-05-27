// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestCompileQuery_PlainIDByID(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"id":"node-1"}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, "node-1", q.GetById())
	assert.Empty(t, q.GetIds())
}

// TestCompileQuery_IDIncludeEdgesNoCarrier pins T-GTB2 site (d): the
// include_edges / include_cross_links absorption flags are GONE from the proto,
// so compileQuery lowers a query(id, include_edges) to a PLAIN by-id plan with
// no absorption carrier. The absorption is composed in dispatchQueryByID BEFORE
// Compile is reached (see dispatch_byid.go + the dispatch tests); this test
// asserts the compile-time plan carries no removed flag and is just the by-id
// node read.
func TestCompileQuery_IDIncludeEdgesNoCarrier(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"id":"node-1","include_edges":true}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, "node-1", q.GetById(), "include_edges lowers to a plain by-id plan (no carrier)")
}

func TestCompileQuery_IDIncludeCrossLinksNoCarrier(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"id":"node-1","include_cross_links":true}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, "node-1", q.GetById(), "include_cross_links lowers to a plain by-id plan (no carrier)")
}

func TestCompileQuery_IDsBulkOnePlan(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"ids":["a","b","c"]}`))
	require.True(t, ok)
	q := req.GetQuery()
	// ids[]-bulk → QueryPlan.Ids (store.ByIDs, renders {nodes:[]}), ONE plan.
	assert.Equal(t, []string{"a", "b", "c"}, q.GetIds())
	assert.Empty(t, q.GetById(), "bulk uses Ids, not ById")
}

func TestCompileQuery_GraphReachPPR(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"mode":"graph_reach","text":"hub","limit":5}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, []string{"hub"}, q.GetQueries())
	assert.Equal(t, knowledgev1.SearchMode_SEARCH_MODE_PPR, q.GetSearchMode())
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_SEARCH, q.GetReturnMode())
	assert.Equal(t, int32(5), q.GetLimit())
}

func TestCompileQuery_RecentTemporal(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"mode":"recent","text":"fresh"}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL, q.GetSearchMode())
	assert.InEpsilon(t, recentHalfLifeDays, q.GetHalfLife(), 0.0001, "half-life 30 reproduces legacy Temporal(30)")
}

func TestCompileQuery_TextMode(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"mode":"text","text":"q"}`))
	require.True(t, ok)
	q := req.GetQuery()
	assert.Equal(t, []string{"q"}, q.GetQueries())
	assert.Equal(t, knowledgev1.SearchMode_SEARCH_MODE_UNSPECIFIED, q.GetSearchMode(), "plain text → hybrid default")
}

func TestCompileQuery_TypeBrowse(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"type":"finding","limit":10,"offset":5}`))
	require.True(t, ok)
	q := req.GetQuery()
	sel := q.GetSelection()
	require.NotNil(t, sel)
	assert.Equal(t, "finding", sel.GetNodeType())
	assert.Equal(t, int32(10), q.GetLimit())
	assert.Equal(t, int32(5), q.GetOffset())
	assert.Empty(t, q.GetQueries(), "browse is not a search")
}

func TestCompileQuery_MetaPredicates(t *testing.T) {
	t.Run("exists sentinel", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"type":"finding","meta":{"dsl_pattern":"*"}}`))
		require.True(t, ok)
		preds := req.GetQuery().GetSelection().GetMetadataPredicates()
		require.Len(t, preds, 1)
		assert.Equal(t, "dsl_pattern", preds[0].GetKey())
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EXISTS, preds[0].GetOp())
	})
	t.Run("eq value", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"type":"finding","meta":{"severity":"high"}}`))
		require.True(t, ok)
		preds := req.GetQuery().GetSelection().GetMetadataPredicates()
		require.Len(t, preds, 1)
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, preds[0].GetOp())
		assert.Equal(t, "high", preds[0].GetValue())
	})
}

func TestCompileQuery_MetaOnlyEnumeration(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"meta":{"dsl_pattern":"*"}}`))
	require.True(t, ok)
	sel := req.GetQuery().GetSelection()
	require.NotNil(t, sel)
	assert.Empty(t, sel.GetNodeType(), "meta-only → Match(\"\")")
	require.Len(t, sel.GetMetadataPredicates(), 1)
}

func TestCompileQuery_GenericCrossGraph(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"id":"x","graph":"cloud","account":"acct"}`))
	require.True(t, ok)
	assert.Equal(t, "cloud", req.GetTarget().GetGraph())
	assert.Equal(t, "acct", req.GetTarget().GetAccount())
	assert.Equal(t, "x", req.GetQuery().GetById())
}

func TestCompileQuery_DenyCases(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"mode stats", `{"mode":"stats"}`},
		{"mode topology", `{"mode":"topology","algorithm":"pagerank"}`},
		{"mode examine", `{"mode":"examine","id":"x"}`},
		{"mode lineage", `{"mode":"lineage","id":"x"}`},
		{"mode plan_tree", `{"mode":"plan_tree","id":"x"}`},
		{"mode evidence", `{"mode":"evidence","id":"x"}`},
		{"mode file_symbols", `{"mode":"file_symbols"}`},
		{"mode personality", `{"mode":"personality"}`},
		{"mode tensions", `{"mode":"tensions"}`},
		{"mode clusters", `{"mode":"clusters"}`},
		{"graph code id", `{"id":"x","graph":"code"}`},
		{"graph logs", `{"graph":"logs","name":"q1"}`},
		{"thought filter valence", `{"valence_min":0.5}`},
		{"thought filter session", `{"session":"design"}`},
		{"thought filter connected_to", `{"connected_to":"node-x"}`},
		{"empty no shape", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := compileQuery(json.RawMessage(tc.args))
			assert.False(t, ok, "%s must fall through to legacy", tc.name)
			assert.Nil(t, req)
		})
	}
}

// TestCompileQuery_BrowseDefaultsLimit is the FUL-302 Option-A regression
// guard. After the server-side default-injection was removed (planToQ now
// honors limit==0 = no cap so internal Match-all helpers fetch everything), the
// LLM-facing browse default-10 MUST be applied CLIENT-SIDE at compile time.
// Without it, a no-limit query-tool browse would send limit==0 → unbounded →
// the entire graph. This test pins that every BROWSE arm the query tool
// compiles (singular type-browse, plural-types browse, meta-only browse)
// defaults to browseDefaultLimit (10) when the caller supplies no positive
// limit, and that an explicit positive limit still overrides verbatim.
func TestCompileQuery_BrowseDefaultsLimit(t *testing.T) {
	t.Run("type-browse no limit → 10", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"type":"finding"}`))
		require.True(t, ok)
		assert.Equal(t, int32(browseDefaultLimit), req.GetQuery().GetLimit(),
			"a no-limit type-browse must cap at 10 client-side, NOT send limit=0=unbounded")
	})

	t.Run("plural-types browse no limit → 10", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"types":["finding","decision"]}`))
		require.True(t, ok)
		assert.Equal(t, int32(browseDefaultLimit), req.GetQuery().GetLimit(),
			"a no-limit plural-types browse must cap at 10 client-side")
	})

	t.Run("meta-only browse no limit → 10", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"meta":{"dsl_pattern":"*"}}`))
		require.True(t, ok)
		assert.Equal(t, int32(browseDefaultLimit), req.GetQuery().GetLimit(),
			"a no-limit meta-only browse must cap at 10 client-side")
	})

	t.Run("explicit positive limit overrides the default", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"type":"finding","limit":50}`))
		require.True(t, ok)
		assert.Equal(t, int32(50), req.GetQuery().GetLimit(),
			"an explicit limit is honored verbatim, not the default")
	})

	t.Run("search arms do NOT get the browse default (compositor self-defaults)", func(t *testing.T) {
		// A no-limit text search must stay limit=0 at the plan level — the
		// server-side compositor self-defaults to 10. Applying the browse default
		// here would be over-application (the bug the search/browse split avoids).
		req, ok := compileQuery(json.RawMessage(`{"mode":"text","text":"q"}`))
		require.True(t, ok)
		assert.Equal(t, int32(0), req.GetQuery().GetLimit(),
			"search plan stays limit=0; the compositor owns the search default")
	})

	t.Run("ids[] bulk hydrate is NOT browse-defaulted", func(t *testing.T) {
		// A bulk ids[] hydrate must return EVERY requested id, never the first 10.
		req, ok := compileQuery(json.RawMessage(`{"ids":["a","b","c"]}`))
		require.True(t, ok)
		assert.Equal(t, int32(0), req.GetQuery().GetLimit(),
			"ids[] must not be capped — it returns all requested ids")
	})

	t.Run("by-id is NOT browse-defaulted", func(t *testing.T) {
		req, ok := compileQuery(json.RawMessage(`{"id":"node-1"}`))
		require.True(t, ok)
		assert.Equal(t, int32(0), req.GetQuery().GetLimit(),
			"by-id returns one node; no cap applied")
	})
}

// TestApplyBrowseLimitOffset pins the browse limit/offset helper directly: a
// non-positive limit defaults to browseDefaultLimit (10); a positive limit is
// honored; offset rides only when positive.
func TestApplyBrowseLimitOffset(t *testing.T) {
	t.Run("zero limit defaults to 10", func(t *testing.T) {
		p := &knowledgev1.QueryPlan{}
		applyBrowseLimitOffset(p, 0, 0)
		assert.Equal(t, int32(browseDefaultLimit), p.GetLimit())
		assert.Equal(t, int32(0), p.GetOffset())
	})
	t.Run("negative limit defaults to 10", func(t *testing.T) {
		p := &knowledgev1.QueryPlan{}
		applyBrowseLimitOffset(p, -1, 0)
		assert.Equal(t, int32(browseDefaultLimit), p.GetLimit())
	})
	t.Run("positive limit + offset are honored", func(t *testing.T) {
		p := &knowledgev1.QueryPlan{}
		applyBrowseLimitOffset(p, 25, 7)
		assert.Equal(t, int32(25), p.GetLimit())
		assert.Equal(t, int32(7), p.GetOffset())
	})
}

// TestCompileQuery_ModulesMode asserts query(mode:modules) compiles to a
// RETURN_MODE_GRAPH_NAMES plan (T-GTB1e list-graphs catalog enumeration): no
// Selection, no queries — the target GraphSelector carries the GraphType.
func TestCompileQuery_ModulesMode(t *testing.T) {
	req, ok := compileQuery(json.RawMessage(`{"mode":"modules","graph":"practice","language":"go"}`))
	require.True(t, ok, "modules mode must compile to Execute")
	require.NotNil(t, req.GetQuery())
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES, req.GetQuery().GetReturnMode())
	assert.Nil(t, req.GetQuery().GetSelection(), "modules carries no Selection")
	assert.Empty(t, req.GetQuery().GetQueries(), "modules carries no queries")
	assert.Equal(t, "practice", req.GetTarget().GetGraph())
	assert.Equal(t, "go", req.GetTarget().GetLanguage())
}
