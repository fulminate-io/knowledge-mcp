// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_params_test.go covers the caller-supplied PARAMS the
// practice arms route — the row limit and the json field projection — across both
// ranked-search composers and both entry tools.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// projectedSearchRows unmarshals a projected search render and returns its rows.
func projectedSearchRows(t *testing.T, body string) []map[string]any {
	t.Helper()
	var payload struct {
		Total   int              `json:"total"`
		Results []map[string]any `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload), "format:json must emit valid JSON: %s", body)
	return payload.Results
}

// TestPracticeSearch_LimitRouted pins that the caller's limit reaches the segment
// engine on both composers and both tools, that an absent limit still resolves to
// the shared default, and that the fan-out applies it at the MERGE end too.
func TestPracticeSearch_LimitRouted(t *testing.T) {
	t.Run("q_limit", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "go", Text: "pool", Limit: 3,
		})
		assert.Equal(t, 3, mgr.lastK, "the QUERY tool's limit must reach mgr.Search as k")
	})

	t.Run("q_default", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "go", Text: "pool"})
		// The KNOWN-POSITIVE half of q_limit: an absent limit must resolve to the
		// shared default, not to zero. Without this, "limit is routed" would be
		// satisfied by a composer that passed the caller's value straight through
		// and searched for nothing when the caller omitted it.
		assert.Equal(t, knowledgeSearchDefaultLimit, mgr.lastK,
			"an absent limit resolves to knowledgeSearchDefaultLimit, preserving prior behaviour")
	})

	t.Run("s_limit", func(t *testing.T) {
		// The SEARCH tool routes through searchReducibleArgs, whose Limit field is
		// this step's addition; the QUERY tool's queryArgs already carried one, so
		// only this subtest exercises the new struct field.
		gc := newFanOutHarness(t, []string{"go"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "query": "pool", "limit": 4,
		}))
		require.True(t, handled)
		require.False(t, out.IsError, textBodyTools(out))
		assert.Equal(t, 4, mgr.lastK, "the SEARCH tool's limit must reach mgr.Search as k")
	})

	t.Run("fanout_cap", func(t *testing.T) {
		// The fan-out applies the limit at BOTH ends. Two graphs return two hits
		// each, so a limit of 2 must trim the MERGED set — a composer that passed
		// the limit only to the per-graph Search would render all four.
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go1", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:go2", "GoErrgroup", "errgroup"),
			practiceNode("p:py1", "PyThreadPool", "thread pool executor"),
			practiceNode("p:py2", "PyAsyncio", "asyncio"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go1", Score: 0.95}, {ID: "p:go2", Score: 0.85}},
			"python": {{ID: "p:py1", Score: 0.75}, {ID: "p:py2", Score: 0.65}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "all", Text: "pool", Format: "json", Limit: 2,
		})
		body := textBodyTools(res)
		var env struct {
			Total   int              `json:"total"`
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &env), "fan-out json must parse: %s", body)
		assert.Equal(t, 2, env.Total, "the merged set is capped at the caller's limit, not the default")
		require.Len(t, env.Results, 2)
		// Score-desc: the cap keeps the TOP rows, so the two go hits survive.
		assert.Equal(t, "p:go1", env.Results[0]["id"])
		assert.Equal(t, "p:go2", env.Results[1]["id"])
	})
}

// TestPracticeSearch_FieldsProjected pins that the caller's json projection
// reaches engine.RenderForCaller on both ranked-search composers, where a literal
// nil used to sit.
func TestPracticeSearch_FieldsProjected(t *testing.T) {
	fields := []string{"id", "name", "metadata.category"}

	t.Run("per_language", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go": {{ID: "p:go", Score: 0.9}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "go", Text: "pool", Format: "json", Fields: fields,
		})
		rows := projectedSearchRows(t, textBodyTools(res))
		require.Len(t, rows, 1)
		assert.Equal(t, "p:go", rows[0]["id"])
		assert.Equal(t, "GoWorkerPool", rows[0]["name"])
		assert.Equal(t, "concurrency", rows[0]["metadata.category"])
		// PROJECTS, not merely renders: an unrequested key must not ride along. The
		// fixture node carries importance too, and the unprojected envelope would
		// carry content, score and status as well.
		assert.NotContains(t, rows[0], "metadata.importance")
		assert.NotContains(t, rows[0], "content")
		assert.NotContains(t, rows[0], "score")
	})

	t.Run("fanout", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "all", Text: "pool", Format: "json", Fields: fields,
		})
		rows := projectedSearchRows(t, textBodyTools(res))
		require.Len(t, rows, 2)
		for _, row := range rows {
			assert.Contains(t, row, "id")
			assert.Equal(t, "concurrency", row["metadata.category"])
			assert.NotContains(t, row, "content")
		}
	})
}

// TestHelpPatterns_DocumentedCallsAreRouted is a doc/router AGREEMENT test: each
// practice call help_patterns.go advertises is driven through the real intercept
// and must not come back an accounting refusal.
//
// It constructs the payloads INDEPENDENTLY rather than parsing the help const. A
// test that read the doc string and fed it back would pass for any pair of
// mutually-consistent-but-wrong values — it would prove the doc agrees with
// itself, not that the router serves what the doc promises.
func TestHelpPatterns_DocumentedCallsAreRouted(t *testing.T) {
	// The refusal the per-arm accounting gate emits. Its ABSENCE is what each row
	// asserts; "is not applied by this path" is that gate's own wording.
	const refusal = "is not applied by this path"

	drive := func(t *testing.T, payload string) (bool, string) {
		t.Helper()
		gc := newFanOutHarness(t, []string{"knowledge-architecture"},
			practiceNode("pat1", "Registry pattern", "one table, one init"))
		deps := &interceptDeps{
			gc:     gc,
			segMgr: newFanOutSegmentSearcher(map[string][]searchengine.Hit{"knowledge-architecture": {{ID: "pat1", Score: 0.9}}}),
		}
		handled, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{
			Name: "query", Arguments: json.RawMessage(payload),
		})
		return handled, textBodyTools(res)
	}

	// help_patterns.go:16 — the scanner worker's enumeration. `format` is the half
	// this step added: renderBrowseResponse reads Fields ONLY inside its json arm,
	// so the documented projection was inert without it.
	t.Run("enum_fields", func(t *testing.T) {
		handled, body := drive(t, `{"graph":"practice","language":"go","type":"finding",`+
			`"meta":{"dsl_pattern":"*"},"format":"json","fields":["id","name","metadata.dsl_pattern"]}`)
		assert.True(t, handled, "the documented enumeration must be CLAIMED")
		assert.NotContains(t, body, refusal, "the router must apply every param the doc advertises: %s", body)

		// KNOWN POSITIVE, same run. Every row above asserts an ABSENCE, which a
		// misspelled refusal string or an arm that stopped refusing anything would
		// satisfy vacuously. resource_type is a cloud param this arm genuinely
		// rejects, so the identical drive MUST produce the phrase.
		_, rejected := drive(t, `{"graph":"practice","language":"go","resource_type":"ec2"}`)
		assert.Contains(t, rejected, refusal,
			"the refusal phrase must be reachable, or the absence assertions above prove nothing")
	})

	// help_patterns.go:22 — the bare browse.
	t.Run("bare_browse", func(t *testing.T) {
		handled, body := drive(t, `{"graph":"practice","language":"knowledge-architecture"}`)
		assert.True(t, handled)
		assert.NotContains(t, body, refusal, body)
	})

	// help_patterns.go:23 — the ranked text search.
	t.Run("text_search", func(t *testing.T) {
		handled, body := drive(t, `{"graph":"practice","language":"knowledge-architecture","text":"registry"}`)
		assert.True(t, handled)
		assert.NotContains(t, body, refusal, body)
	})

	// help_patterns.go:24 — the by-id read. This one is deliberately NOT served
	// here: practiceShapeIsForeign declines it so the engine dispatch, which owns
	// by-id reads, gets the call. handled==false is the positive artifact.
	t.Run("by_id", func(t *testing.T) {
		handled, _ := drive(t, `{"id":"pat1","graph":"practice","language":"knowledge-architecture"}`)
		assert.False(t, handled, "a documented by-id read passes through to the engine dispatch")
	})
}

// TestPracticeTraverse_LoudAndCharacterized is a CHARACTERIZATION GUARD, green
// before and after — stated plainly rather than dressed as red-first, because the
// behaviour it pins was already correct.
//
// It records the disposition of an earlier audit claim — that a traverse from a
// practice node returns zero SILENTLY — which was re-probed live on 2026-08-24
// and NOT reproduced. Observed instead: a missing root errors loudly in both
// graphs ("traversal root ... not found"), a real practice node with edges
// traverses correctly, and a root with no edges renders "No nodes reached." —
// which names the outcome rather than hiding it. compileTraverse's buildTarget
// carries Language for the practice family, which is why the walk resolves.
//
// It sits at the engine's compile/render seam rather than against a live daemon
// so it runs in CI without a populated practice graph.
func TestPracticeTraverse_LoudAndCharacterized(t *testing.T) {
	const practiceTraverse = `{"start":"practice-root-node","graph":"practice",` +
		`"language":"postgres-best-practices","direction":"both","depth":2}`

	t.Run("practice selector carries language", func(t *testing.T) {
		req, ok := engine.Compile("traverse", json.RawMessage(practiceTraverse))
		require.True(t, ok, "a practice traversal must compile")
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
		// THE LOAD-BEARING LEG. buildTarget carries Language for the practice
		// family, and that is precisely why the practice walk resolves rather than
		// silently addressing another graph.
		assert.Equal(t, "postgres-best-practices", req.GetTarget().GetLanguage(),
			"the practice traverse target is keyed on language")
		assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL, req.GetQuery().GetReturnMode())
	})

	t.Run("zero-result traversal names the outcome", func(t *testing.T) {
		// The zero the prior finding reported as SILENT. It is not: the render says
		// so in words. TestRenderTraversal_Empty pins this generically in package
		// engine; this row pins it for the practice SELECTOR, which nothing covered.
		out, err := engine.Render("traverse", json.RawMessage(practiceTraverse), &knowledgev1.ExecuteResponse{})
		require.NoError(t, err)
		require.NotEmpty(t, out.Content)
		assert.Contains(t, out.Content[0].Text, "No nodes reached.",
			"an empty practice traversal NAMES the outcome rather than rendering an empty body")
	})
}
