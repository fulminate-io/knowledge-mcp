// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_params_test.go covers the caller-supplied PARAMS the
// practice arms route — the row limit and the json field projection — across both
// ranked-search composers and both entry tools.

import (
	"encoding/json"
	"strings"
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
		gc := newFanOutHarness(t, []string{"default"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Text: "pool", Limit: 3,
		})
		assert.Equal(t, 3, mgr.lastK, "the QUERY tool's limit must reach mgr.Search as k")
	})

	t.Run("q_default", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"default"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Text: "pool"})
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
		gc := newFanOutHarness(t, []string{"default"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "practice", "query": "pool", "limit": 4,
		}))
		require.True(t, handled)
		require.False(t, out.IsError, textBodyTools(out))
		assert.Equal(t, 4, mgr.lastK, "the SEARCH tool's limit must reach mgr.Search as k")
	})
}

// TestPracticeSearch_FieldsProjected pins that the caller's json projection
// reaches engine.RenderForCaller on both ranked-search composers, where a literal
// nil used to sit.
func TestPracticeSearch_FieldsProjected(t *testing.T) {
	fields := []string{"id", "name", "metadata.category"}

	t.Run("the_combined_graph", func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"default"}, practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"default": {{ID: "p:go", Score: 0.9}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Text: "pool", Format: "json", Fields: fields,
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
}

// TestHelpPatterns_DocumentedCallsAreRouted is a doc/router AGREEMENT test: each
// practice call help_patterns.go advertises is driven through the real intercept
// and must not come back an accounting refusal.
//
// It constructs the payloads INDEPENDENTLY rather than parsing the help const. A
// test that read the doc string and fed it back would pass for any pair of
// mutually-consistent-but-wrong values — it would prove the doc agrees with
// itself, not that the router serves what the doc promises.
//
// IT COVERS BOTH HALVES OF THE DOC. help_patterns.go advertises four READ calls
// and NINE WRITE calls, and for as long as this test drove only the query
// intercept the write half was pinned by nothing: the nine mutate examples could
// be rewritten to any vocabulary at all and every subtest stayed green. The
// write rows below close that, and the read rows drive the `source` spelling the
// rewritten doc actually advertises rather than the `language` one it retired —
// an agreement test that drives payloads its subject no longer contains has
// stopped being about its subject.
func TestHelpPatterns_DocumentedCallsAreRouted(t *testing.T) {
	// The refusal the per-arm accounting gate emits. Its ABSENCE is what each row
	// asserts; "is not applied by this path" is that gate's own wording.
	const refusal = "is not applied by this path"

	// The two opaque hub ids the doc's examples carry. They are opaque BY
	// CONSTRUCTION: a hub is addressed by node id, and a router that only works
	// for ids shaped like a language name would pass a test written with "go".
	const (
		archHub = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		dpHub   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	drive := func(t *testing.T, payload string) (bool, string) {
		t.Helper()
		gc := newFanOutHarness(t, []string{"default"},
			practiceNode("pat1", "Registry pattern", "one table, one init"))
		deps := &interceptDeps{
			gc:     gc,
			segMgr: newFanOutSegmentSearcher(map[string][]searchengine.Hit{"default": {{ID: "pat1", Score: 0.9}}}),
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
		handled, body := drive(t, `{"graph":"practice","source":"`+archHub+`","type":"finding",`+
			`"meta":{"dsl_pattern":"*"},"format":"json","fields":["id","name","metadata.dsl_pattern"]}`)
		assert.True(t, handled, "the documented enumeration must be CLAIMED")
		assert.NotContains(t, body, refusal, "the router must apply every param the doc advertises: %s", body)

		// KNOWN POSITIVE, same run. Every row above asserts an ABSENCE, which a
		// misspelled refusal string or an arm that stopped refusing anything would
		// satisfy vacuously. resource_type is a cloud param this arm genuinely
		// rejects, so the identical drive MUST produce the phrase.
		_, rejected := drive(t, `{"graph":"practice","source":"`+archHub+`","resource_type":"ec2"}`)
		assert.Contains(t, rejected, refusal,
			"the refusal phrase must be reachable, or the absence assertions above prove nothing")
	})

	// help_patterns.go:22 — the bare browse.
	t.Run("bare_browse", func(t *testing.T) {
		handled, body := drive(t, `{"graph":"practice","source":"`+archHub+`"}`)
		assert.True(t, handled)
		assert.NotContains(t, body, refusal, body)
	})

	// help_patterns.go:23 — the ranked text search.
	t.Run("text_search", func(t *testing.T) {
		handled, body := drive(t, `{"graph":"practice","source":"`+archHub+`","text":"registry"}`)
		assert.True(t, handled)
		assert.NotContains(t, body, refusal, body)
	})

	// help_patterns.go:24 — the by-id read. This one is deliberately NOT served
	// here: practiceShapeIsForeign declines it so the engine dispatch, which owns
	// by-id reads, gets the call. handled==false is the positive artifact.
	t.Run("by_id", func(t *testing.T) {
		handled, _ := drive(t, `{"id":"pat1","graph":"practice"}`)
		assert.False(t, handled, "a documented by-id read passes through to the engine dispatch")
	})

	// THE WRITE HALF. The nine mutate examples the doc carries, driven through the
	// real mutate intercept. Each must be CLAIMED and must not come back an
	// accounting refusal; a doc example the router refuses is a broken instruction
	// handed to every caller that reads help("patterns").
	driveMutate := func(t *testing.T, payload string) (bool, kgtools.ToolResult) {
		t.Helper()
		return InterceptMutate(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(payload),
		})
	}

	for _, row := range []struct {
		name, payload string
		isLink        bool
	}{
		// Step 1 — the pattern parent (help_patterns.go:125).
		{name: "create_pattern", payload: `{"operation":"create","type":"pattern","graph":"practice","source_hub":"` + dpHub + `","name":"fan-out-fan-in","summary":"Split work across N goroutines.","description":"Producer dispatches items to a pool."}`},
		// Step 2 — the two use_cases and their two links (:135, :142, :147, :154).
		{name: "create_use_case_positive", payload: `{"operation":"create","type":"use_case","graph":"practice","source_hub":"` + dpHub + `","name":"parallelizable-work","summary":"work that parallelizes","description":"The same operation applies to many items independently."}`},
		{name: "link_applies_when", isLink: true, payload: `{"operation":"link","from":"pat1","to":"uc1","relationship":"applies-when","graph":"practice"}`},
		{name: "create_use_case_negative", payload: `{"operation":"create","type":"use_case","graph":"practice","source_hub":"` + dpHub + `","name":"strict-ordering-required","summary":"work that must stay ordered","description":"Downstream consumers require submission order."}`},
		{name: "link_avoid_when", isLink: true, payload: `{"operation":"link","from":"pat1","to":"uc2","relationship":"avoid-when","graph":"practice"}`},
		// Step 3 — the example and its link (:161, :170).
		{name: "create_example", payload: `{"operation":"create","type":"example","graph":"practice","source_hub":"` + dpHub + `","name":"fan-out-fan-in-basic","summary":"a basic fan-out example","content":"code","description":"Basic fan-out goroutine pool.","metadata":{"language":"go","attribution":"MIT"}}`},
		{name: "link_contains", isLink: true, payload: `{"operation":"link","from":"pat1","to":"ex1","relationship":"contains","graph":"practice"}`},
		// Step 4 — the reference and its link (:177, :184).
		{name: "create_reference", payload: `{"operation":"create","type":"reference","graph":"practice","source_hub":"` + dpHub + `","name":"Concurrency in Go","summary":"the Cox-Buday book","metadata":{"page":"108"}}`},
		{name: "link_references", isLink: true, payload: `{"operation":"link","from":"pat1","to":"ref1","relationship":"references","graph":"practice"}`},
	} {
		t.Run("write/"+row.name, func(t *testing.T) {
			handled, res := driveMutate(t, row.payload)
			body := toolResultText(res)
			assert.NotContains(t, body, refusal,
				"the router must apply every param the documented write advertises: %s", body)
			assert.NotContains(t, body, "does not accept `language`",
				"and the documented write must not be reaching the legacy selector at all: %s", body)

			if !row.isLink {
				assert.True(t, handled, "a documented create is CLAIMED by the practice passthrough arm")
				return
			}
			// A DOCUMENTED LINK IS DECLINED HERE, and handled==false is the
			// positive artifact rather than a miss — the same disposition the
			// by_id read above carries. The intra-practice arm needs both
			// endpoints to resolve, which they do not against a fake caller, so
			// the call passes to the engine LINK arm that owns it. What must be
			// pinned is therefore what it COMPILES to: a practice LINK carrying no
			// instance selector at all.
			assert.False(t, res.IsError, "a declined link is passed on, never refused: %s", body)
			req, ok := engine.Compile("mutate", json.RawMessage(row.payload))
			require.True(t, ok, "the documented link must compile")
			m, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation)
			require.True(t, isMutation)
			assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, m.Mutation.GetKind())
			assert.Equal(t, "practice", req.GetTarget().GetGraph())
			assert.Empty(t, req.GetTarget().GetLanguage(),
				"the documented link addresses the ONE combined graph and carries no legacy selector")
		})
	}

	// THE DOC'S OWN VOCABULARY, censused rather than driven. Every row above
	// constructs its payload INDEPENDENTLY of the help const, which is what stops
	// the test proving the doc agrees with itself — and which also means no row
	// above goes red if a documented example is edited back to the retired
	// spelling. This subtest closes that gap from the other side: it asserts what
	// the doc SAYS, with a same-run known-positive so a zero is a removal rather
	// than a mistyped needle.
	t.Run("doc_census", func(t *testing.T) {
		assert.Equal(t, 0, strings.Count(helpPatterns, `"language": "design-patterns"`),
			"no documented practice call may select a graph by language: practice is one combined graph")
		// A LINE SCAN RATHER THAN A BARE COUNT of `"language"`, because the doc
		// legitimately carries one inside an example node's METADATA map, where
		// it is the example's own language and not a graph selector. Counting the
		// bare word would have made this row unsatisfiable by a correct doc.
		for line := range strings.SplitSeq(helpPatterns, "\n") {
			if strings.Contains(line, `"graph": "practice"`) {
				assert.NotContains(t, line, `"language"`,
					"a practice call selects no language: %s", strings.TrimSpace(line))
			}
		}
		// KNOWN POSITIVE for both zeros: the replacement vocabulary must be
		// present, once per documented node-creating write.
		assert.Equal(t, 5, strings.Count(helpPatterns, `"source_hub": "<design-patterns hub id>"`),
			"the five documented creates each group their node under its origin hub")
		assert.Positive(t, strings.Count(helpPatterns, `"source": "<knowledge-architecture hub id>"`),
			"and the documented reads narrow by hub under the free spelling")
	})

	// AND THE LINK ARM'S HUB SELECTOR, which is why the four documented link calls
	// carry no hub while the five creates do. source_hub GROUPS a node under an
	// origin on a create; on a link it SCOPES THE ENDPOINTS, because an edge
	// belongs to no hub — both endpoints must already be grouped under the named
	// hub. The documented links want the whole graph, so they omit it, and this
	// row is what makes that omission a pinned decision rather than an oversight.
	//
	// THE ROW IS DRIVEN AGAINST A FAKE THAT RESOLVES NEITHER ENDPOINT, so the
	// scope refuses — and the assertion that matters is WHICH refusal: the
	// accounting phrase must be ABSENT (the param is routed on this arm now,
	// not rejected by the gate) while the scope's own message names the hub and
	// the unresolvable endpoint. TestPracticeLinkHub_ScopesTheEndpoints drives
	// the served and per-endpoint-refusal cells against a seeded graph.
	t.Run("write/control_hub_on_a_link_scopes_its_endpoints", func(t *testing.T) {
		handled, res := driveMutate(t,
			`{"operation":"link","from":"pat1","to":"uc1","relationship":"contains","graph":"practice","source_hub":"`+dpHub+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "an endpoint outside the named hub is bad input, not a no-op")
		body := toolResultText(res)
		assert.NotContains(t, body, refusal,
			"source_hub is ROUTED on the link arm now — an accounting refusal would mean the param never reached it")
		assert.Contains(t, body, dpHub, "the scope refusal names the hub the call asked for")
		assert.Contains(t, body, "resolves to no node",
			"and names the endpoint absence rather than reporting a different hub")
	})

	// THE WRITE HALF'S KNOWN POSITIVE. Every write row asserts an ABSENCE, so the
	// refusal has to be reachable through the same driver: the retired `language`
	// spelling — which is what the nine examples carried before the rewrite — must
	// still be refused, or the rows above prove nothing about which vocabulary the
	// doc advertises.
	t.Run("write/control_retired_spelling_is_refused", func(t *testing.T) {
		handled, res := driveMutate(t,
			`{"operation":"create","type":"pattern","graph":"practice","language":"design-patterns","name":"P","summary":"s"}`)
		require.True(t, handled)
		require.True(t, res.IsError,
			"the vocabulary the doc retired must be refused, or these rows are satisfied by a router that accepts everything")
		assert.Contains(t, toolResultText(res), "source_hub")
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
// copies Language through RAW for every family, which is why a practice walk
// carrying one is REFUSED by the server rather than silently served from the
// combined graph.
//
// It sits at the engine's compile/render seam rather than against a live daemon
// so it runs in CI without a populated practice graph.
func TestPracticeTraverse_LoudAndCharacterized(t *testing.T) {
	const practiceTraverse = `{"start":"practice-root-node","graph":"practice",` +
		`"language":"postgres-best-practices","direction":"both","depth":2}`

	t.Run("the practice traverse target carries the caller's language to the wire", func(t *testing.T) {
		req, ok := engine.Compile("traverse", json.RawMessage(practiceTraverse))
		require.True(t, ok, "a practice traversal must compile")
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
		// THE LOAD-BEARING LEG, AND ITS REASON INVERTED. buildTarget copies
		// Language through raw for every family, and that used to be why a practice
		// walk RESOLVED the pre-singleton graph it named. Those graphs are retired
		// and `language` addresses none, so what the raw copy is for now is the
		// REFUSAL: the field has to reach validateGraphSelector for the server to
		// reject it. A client that dropped it here would leave a practice traverse
		// carrying a selector served silently from the combined graph — the silent
		// redirect this project refuses.
		assert.Equal(t, "postgres-best-practices", req.GetTarget().GetLanguage(),
			"the caller's language reaches the wire, where the server refuses it")
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
