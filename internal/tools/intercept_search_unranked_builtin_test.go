// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestInterceptSearch_TransformersRefused is the regression for the builtin graph
// type that no client search arm claimed: transformers.
//
// IT COVERED TWO GRAPHS AND NOW COVERS ONE. checks was the other, and it is now
// SERVED — its check findings carry segments — so its rows moved to the served-arm
// test rather than being dropped. The property this pins is unchanged for the
// graph that still refuses.
//
// THE DEFECT IT PINS was silent in the way that matters most. Both types are
// builtin, so interceptSearchReducibleGraph's custom-graph default branch ejected
// them (`kgtypes.IsBuiltinGraphType(graph)` → return false), and the switch above
// it listed neither — so the call fell out of every claim and compiled to a server
// RETURN_MODE_SEARCH the server treats as informational. The caller got rows, or
// zero rows, with no error and no disclosure that ranking never happened, and any
// query_vector was discarded with the plan.
//
// BOTH EMBED STATES ARE DRIVEN, and before the fix they failed DIFFERENTLY —
// which is exactly why one row would not have covered the other. The guard at
// search.go's `!hasRewrite && !didEmbed && !claimKnowledge` sits AFTER
// embedKnowledgeQuery, so:
//   - NO embed: didEmbed is false, the guard fires, InterceptSearch returned
//     handled=FALSE and the bare server call downstream took the payload.
//   - EMBED RESOLVED: didEmbed is true, the guard does NOT fire, and execution
//     continued into the knowledge-arm tail carrying graph="transformers"/"checks"
//     — which dispatched a server RETURN_MODE_SEARCH from inside the interceptor.
//
// WHICH BRANCH RUNS IS NOT "IS AN EMBEDDER CONFIGURED". embedKnowledgeQuery
// resolves its embedder through knowledgeQueryEmbedder, which reads the identity
// recorded by the KNOWLEDGE/default graph — never the requested graph — so a
// wired embedder alone still yields didEmbed=false when the catalog records no
// identity. The second row therefore supplies BOTH a stub embedder and a catalog
// identity (cannedEmbeddedNodesResp); with only the embedder the two rows collapse
// onto the same branch and the coverage is imaginary.
//
// Both are now the same refusal, and NEITHER touches the wire.
func TestInterceptSearch_TransformersRefused(t *testing.T) {
	for _, graph := range []string{"transformers"} {
		for _, emb := range []struct {
			name    string
			withEmb bool
		}{{"no-embed-identity", false}, {"embed-identity-resolved", true}} {
			t.Run(graph+"/"+emb.name, func(t *testing.T) {
				var execHits, embedCalls atomic.Int64
				resp := &knowledgev1.ExecuteResponse{}
				if emb.withEmb {
					resp = cannedEmbeddedNodesResp()
				}
				gc, handler := newInterceptHarnessWithHandler(t, &execHits, resp)
				mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
				deps := &interceptDeps{gc: gc, segMgr: mgr}
				if emb.withEmb {
					deps.emb = stubEmbedder{calls: &embedCalls}
				}

				handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
					"graph": graph, "query": "x",
				}))
				body := engine.FirstTextContent(out)
				t.Logf("observed: handled=%v isError=%v serverSearch=%v rpcs=%d body=%q",
					handled, out.IsError, dispatchedAServerSearch(handler.recordedReqs()),
					len(handler.recordedReqs()), body)

				require.True(t, handled,
					"%s search must be CLAIMED by the client, not left to fall through to the server no-op", graph)
				require.False(t, out.IsError,
					"the refusal is a plain result, not a tool error (matching the linkage precedent): %s", body)

				// The refusal is SELF-DESCRIBING: it names the graph, says ranked
				// search is unavailable, and hands the caller a path that works.
				assert.Contains(t, body, graph, "the refusal names the graph")
				assert.Contains(t, body, "not available", "the refusal states ranked search is unavailable")
				assert.Contains(t, body, "query(graph:\""+graph+"\"",
					"the refusal names the working query access path for %s", graph)

				// Nothing reached the wire and nothing reached the index.
				require.Equal(t, int64(0), mgr.calls.Load(), "%s refusal drives no client segment engine", graph)
				require.Empty(t, handler.recordedReqs(), "%s refusal dispatches no RPC at all", graph)
				require.Zero(t, execHits.Load(), "%s refusal costs no read", graph)
			})
		}
	}
}

// TestInterceptSearch_TransformersRefusalStatesWhatTheGraphIs pins the CONTENT of
// the transformers refusal, not merely its existence. A message that named the
// graph and stopped would leave the caller exactly as stuck as the silent zero did.
//
// Every claim asserted here was read in current source this session:
// transformers holds DSL transformer bodies as recipe nodes in the single
// "recipes" bucket (recipe.RecipesBucketName), written through mutate CRUD, and
// gets ZERO client search segments because bm25ArmEnabledFor gates on
// kgtypes.HasRebuildableSegments, which excludes it.
func TestInterceptSearch_TransformersRefusalStatesWhatTheGraphIs(t *testing.T) {
	body := transformersSearchUnavailableResultBody(t)

	assert.Contains(t, body, "recipe", "the message says WHAT the graph is: the recipe store")
	assert.Contains(t, body, "mutate", "the message names the CRUD surface recipes are authored through")
	assert.Contains(t, body, `query(graph:"transformers", name:"recipes", type:"recipe")`,
		"the message names the browse that actually works — the same (graph, name, type) triple the recipe loader drives")
	assert.NotContains(t, body, "retired",
		"transformers ranked search was never offered and then withdrawn; 'retired' would misdescribe it")
}

// TestUnrankedBuiltinRefusal_SameWordingOnBothRails is the ONE-SOURCE-OF-TRUTH
// pin: a caller who reaches for `search` and a caller who reaches for `query` are
// told the same thing about the same graph, byte for byte.
//
// IT IS AN EQUALITY, NOT TWO SUBSTRING CHECKS, and that is the point. Substring
// assertions on each rail would both stay green while the two messages drifted
// into naming DIFFERENT access paths — the specific rot that makes a refusal
// worse than useless, because a caller following the stale half is sent somewhere
// that does not work. The equality fails the moment either wording is edited
// without the other, which is exactly when a reader needs to be stopped.
//
// The query rail is driven through its real entry point rather than by calling
// the helper directly, so this also pins that the query arm ANSWERS with the
// shared helper instead of having quietly grown a copy of its own.
//
// IT IS NARROWED TO transformers RATHER THAN DELETED. checks was the second graph
// and is now served on both rails, but the both-rails wording property is still
// real for the graph that still refuses, and dropping the test with the graph
// would have retired a live invariant along with a dead row.
func TestUnrankedBuiltinRefusal_SameWordingOnBothRails(t *testing.T) {
	for _, graph := range []string{"transformers"} {
		t.Run(graph, func(t *testing.T) {
			searchBody := unrankedBuiltinRefusalBody(t, graph)

			args := map[string]any{"graph": graph, "mode": "text", "text": "x"}
			if graph == "transformers" {
				args["name"] = "recipes"
			}
			handled, out := InterceptQueryUnrankedBuiltin(opCtx(), &interceptDeps{}, queryParams(t, args))
			require.True(t, handled, "the %s query arm must claim a text search", graph)
			require.False(t, out.IsError, "the refusal is a plain result on the query rail too")
			queryBody := engine.FirstTextContent(out)

			require.Equal(t, searchBody, queryBody,
				"the %s refusal must be ONE string serving both rails — a divergence here means "+
					"the two tools have started giving different advice about the same graph", graph)
			require.NotEmpty(t, searchBody, "an empty body would make the equality above vacuous")
		})
	}
}

// TestInterceptQueryUnrankedBuiltin_NonSearchShapesDecline pins the DECLINE half
// of the query arm at the arm itself, where the reason is visible.
//
// Every row here is a shape one of the two refusals NAMES as the way forward. If
// the arm claimed them, the message would route its reader back into the message
// — a loop with no exit, which is a worse outcome than the silent zero the
// refusal replaced. The bootstrap parity rows observe the same property end to
// end; this observes it at the gate, so a failure says which clause let go.
func TestInterceptQueryUnrankedBuiltin_NonSearchShapesDecline(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"transformers recipe browse", map[string]any{
			"graph": "transformers", "name": "recipes", "type": "recipe"}},
		{"transformers stats", map[string]any{
			"graph": "transformers", "name": "recipes", "mode": "stats"}},
		{"a thought-filter shape stays on the recall surface", map[string]any{
			"graph": "transformers", "name": "recipes", "text": "x", "session": "s"}},
		{"checks is no longer claimed by the refusal arm at all", map[string]any{
			"graph": "checks", "text": "x"}},
		{"another graph entirely", map[string]any{"graph": "practice", "text": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled, _ := InterceptQueryUnrankedBuiltin(opCtx(), &interceptDeps{}, queryParams(t, tc.args))
			assert.False(t, handled, "%s must fall through to the arm that serves it", tc.name)
		})
	}
}

// transformersSearchUnavailableResultBody drives the real InterceptSearch and
// returns the rendered transformers refusal, so the content assertions above read
// the string a CALLER would see rather than a helper's return value in isolation.
func transformersSearchUnavailableResultBody(t *testing.T) string {
	t.Helper()
	return unrankedBuiltinRefusalBody(t, "transformers")
}

func unrankedBuiltinRefusalBody(t *testing.T, graph string) string {
	t.Helper()
	var execHits atomic.Int64
	gc, _ := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
	deps := &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}}
	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": graph, "query": "x",
	}))
	require.True(t, handled, "%s search must be claimed", graph)
	return engine.FirstTextContent(out)
}

// TestInterceptSearch_ClaimedFamiliesUnchangedByTheNewRefusals is the CONTROL
// set: one representative of every other claimed family, driven through the same
// entry point, proving the two new switch cases changed nobody else's arm.
//
// WHY A SHARED MARKER RATHER THAN PER-FAMILY BODY ASSERTIONS: each family already
// owns a test asserting what it DOES (cloud/cicd, practice fan-out, web/pdf BM25,
// linkage retired, registered custom graph). What none of them can see is a new
// refusal leaking sideways, because none of them knows the refusal exists. The
// marker is what makes that visible from here.
//
// The linkage row is the TEMPLATE these two refusals were built on, so it is
// asserted positively — it must still fire, with its own wording, not the new one.
func TestInterceptSearch_ClaimedFamiliesUnchangedByTheNewRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want func(t *testing.T, body string)
	}{
		{
			name: "knowledge-default",
			args: map[string]any{"graph": "knowledge", "query": "x"},
		},
		{
			name: "cloud",
			args: map[string]any{"graph": "cloud", "account": "acct", "query": "x"},
		},
		{
			name: "practice",
			args: map[string]any{"graph": "practice", "query": "x"},
		},
		{
			name: "web",
			args: map[string]any{"graph": "web", "name": "doc-slug", "query": "x"},
		},
		{
			name: "linkage",
			args: map[string]any{"graph": "linkage", "query": "x"},
			want: func(t *testing.T, body string) {
				t.Helper()
				assert.Contains(t, body, "retired",
					"the linkage refusal — the template for the two new ones — must still fire with its OWN wording")
				assert.Contains(t, body, "linkage")
			},
		},
		{
			name: "registered-custom-graph",
			args: map[string]any{"graph": "hellograph", "name": "demo", "query": "x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var execHits, embedCalls atomic.Int64
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
				&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "ControlHit", Content: "x"},
			))
			handler.graphNames = []string{"demo"}
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
			deps := &interceptDeps{
				gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr,
				gtCRUD: registeredGraphTypes("hellograph"),
			}

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, tc.args))
			require.True(t, handled, "%s stays claimed by its own arm", tc.name)
			body := engine.FirstTextContent(out)

			for _, leaked := range []string{"transformers", "checks"} {
				assert.NotContainsf(t, strings.ToLower(body), leaked,
					"%s must not be answered by the %s refusal", tc.name, leaked)
			}
			if tc.want != nil {
				tc.want(t, body)
			}
		})
	}
}
