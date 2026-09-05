// SPDX-License-Identifier: Apache-2.0

// thought_test.go — unit tests for the client-side InterceptThoughts
// dispatch. Asserts:
//
//   - name-filtering: only "thoughts" / "query" tool calls are considered
//   - operation/mode routing: recognized ops/modes return (true, _); an
//     unrecognized `thoughts` operation TERMINATES with the canonical
//     unknown-operation diagnostic, while an unrecognized query mode still
//     falls through (false, zero) to the arms behind this one
//   - malformed JSON: claimed for `thoughts`, still fallen through for `query`
//
// The handlers themselves require a live *graphclient.GraphClient (they
// issue real wire calls), so the tests cover only the dispatch shape.
// Wire-mode tests live alongside the reflective package itself.

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// contentText concatenates a ToolResult's text content blocks.
func contentText(res kgtools.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// thoughtTestDeps satisfies ClientDeps for dispatch-only tests. All
// accessors return zero values — the handlers reaching the
// GraphClient-using code path return an explicit "graph client
// unavailable" error, which the tests assert.
type thoughtTestDeps struct{}

func (thoughtTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (thoughtTestDeps) Sink() collector.Sink                         { return nil }
func (thoughtTestDeps) SubgraphFetcher() CloudSubgraphFetcher        { return nil }
func (thoughtTestDeps) RootDir() string                              { return "" }
func (thoughtTestDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (thoughtTestDeps) PropReady() bool                              { return true }
func (thoughtTestDeps) PipelineReady() bool                          { return true }
func (thoughtTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (thoughtTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (thoughtTestDeps) BackendResolver() BackendResolver             { return nil }
func (thoughtTestDeps) GraphCaller() GraphCaller                     { return nil }
func (thoughtTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (thoughtTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (thoughtTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (thoughtTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (thoughtTestDeps) SegmentPruner() SegmentPruner                 { return nil }

func (thoughtTestDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (thoughtTestDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (thoughtTestDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (thoughtTestDeps) PipelineScanner() PipelineScanner         { return nil }
func (thoughtTestDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (thoughtTestDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (thoughtTestDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (thoughtTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (thoughtTestDeps) ClusterProvider() ClusterProvider     { return nil }
func (thoughtTestDeps) TensionsProvider() TensionsProvider   { return nil }

// TestInterceptThoughts_NameFiltering pins that non-thoughts /
// non-query tool calls fall through unchanged so the intercept chain's
// other handlers see them.
func TestInterceptThoughts_NameFiltering(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	for _, name := range []string{"ast", "collect", "manage", "search", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptThoughts(opCtx(), deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptThoughts", name)
		assert.Empty(t, res.Content, "non-thoughts/query call must return zero ToolResult")
	}
}

// TestInterceptThoughts_UnknownOpTerminalError pins that an UNRECOGNIZED
// thoughts op TERMINATES here with the canonical diagnostic. It used to fall
// through so the server could surface its own error; post-cutover there is no
// server fallback, so falling through only reached the engine's tool-level
// deny — which reports a missing intercept for a tool that has one.
//
// Every documented op — think, charge, recall, trace, propagate,
// similarity_report, adjacency, charges_for — is claimed client-side; that
// none of them can reach this arm is asserted by
// TestInterceptThoughts_DeclaredOperationsStillRoute.
func TestInterceptThoughts_UnknownOpTerminalError(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	params := kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"unknown-op"}`),
	}
	handled, res := InterceptThoughts(opCtx(), deps, params)
	require.True(t, handled, "an unknown thoughts op must be answered here, not deferred")
	assert.True(t, res.IsError)
	assert.Contains(t, contentText(res), `thoughts: unknown operation "unknown-op" — valid operations:`)
}

// TestInterceptThoughts_ThoughtsRecognizedOpsHandled pins the
// reflective ops InterceptThoughts owns: propagate + recall(clusters).
func TestInterceptThoughts_ThoughtsRecognizedOpsHandled(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	cases := []string{
		`{"operation":"propagate"}`,
		`{"operation":"recall","mode":"clusters"}`,
		`{"operation":"recall","mode":"clusters","all_types":true}`,
	}
	for _, args := range cases {
		params := kgtools.CallToolParams{Name: "thoughts", Arguments: json.RawMessage(args)}
		handled, res := InterceptThoughts(opCtx(), deps, params)
		assert.True(t, handled, "thoughts op %q must be handled client-side", args)
		// nil GraphClient surfaces an "unavailable" error result — proves
		// dispatch reached the per-op handler.
		assert.True(t, res.IsError, "nil graph client must surface error result for %q", args)
	}
}

// TestInterceptThoughts_QueryFallthroughUnknownMode pins that
// non-reflective query modes fall through unchanged.
func TestInterceptThoughts_QueryFallthroughUnknownMode(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	cases := []string{
		`{"mode":""}`,
		`{"mode":"search"}`,
		`{"mode":"examine","id":"x"}`,
		`{"mode":"stats"}`,
		`{"mode":"file_symbols"}`,
		`{"mode":"modules"}`,
		`{"mode":"topology"}`,
		`{"type":"decision"}`,
	}
	for _, args := range cases {
		params := kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(args)}
		handled, _ := InterceptThoughts(opCtx(), deps, params)
		assert.False(t, handled, "query call %q must fall through to server", args)
	}
}

// TestInterceptThoughts_QueryReflectiveModesHandled pins that the
// reflective query modes route through InterceptThoughts. Each call
// returns (true, _) — modes that need a GraphClient surface an
// IsError result; the cache-served modes return a non-error cold/loop-not-running
// message on the nil-provider thoughtTestDeps fixture.
//
// blind_spots, tensions, summary, and personality are NOT in the requiresGC error
// group: they are served from the reflection-loop cache (BlindSpotProvider /
// TensionsProvider / ClusterProvider), so a nil provider returns a clear non-error
// "loop not running" message rather than an IsError. blind_spots is asserted
// separately below. influence + evolution + clusters still hit gc / fetchClusterContext
// / DetectPersistedClusters directly and error on a nil gc.
func TestInterceptThoughts_QueryReflectiveModesHandled(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	requiresGC := map[string]bool{
		"influence": true,
		"evolution": true,
		"clusters":  true,
		// personality, tensions, and summary are cache-served via their providers —
		// a nil provider returns a non-error cold/loop-not-running message, not an error.
		"personality": false,
		"tensions":    false,
		"summary":     false,
	}
	// Each mode gets ONLY the params its own arm routes. The cluster pair used to
	// ride every payload, which the per-arm accounting gate now rejects on the six
	// arms that do not route it — correctly: query(mode:"personality",
	// cluster_a:...) was a silent drop before the gate. evolution is the one arm
	// that consumes the pair, and it needs it to reach its nil-gc error rather
	// than its own missing-cluster refusal.
	modeArgs := map[string]string{
		"evolution": `{"mode":"evolution","cluster_a":"A","cluster_b":"B"}`,
	}
	for mode, wantErr := range requiresGC {
		args, ok := modeArgs[mode]
		if !ok {
			args = `{"mode":"` + mode + `"}`
		}
		params := kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(args)}
		handled, res := InterceptThoughts(opCtx(), deps, params)
		assert.True(t, handled, "query mode %q must be handled client-side", mode)
		if wantErr {
			assert.True(t, res.IsError, "nil graph client must surface error for %q", mode)
		} else {
			assert.False(t, res.IsError, "pure mode %q must succeed with empty graph", mode)
		}
	}
}

// TestInterceptThoughts_BlindSpotsCacheServed pins the cache-serve contract for
// blind_spots: it is handled client-side and, with no reflection loop wired (nil
// BlindSpotProvider), returns a NON-error "loop not running" message — never an
// IsError and never a synchronous recompute.
func TestInterceptThoughts_BlindSpotsCacheServed(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{} // BlindSpotProvider() returns nil.
	params := kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"blind_spots"}`)}
	handled, res := InterceptThoughts(opCtx(), deps, params)
	assert.True(t, handled, "blind_spots is handled client-side")
	assert.False(t, res.IsError, "nil provider returns a non-error cold/loop-not-running message, not an error")
	assert.Contains(t, res.Content[0].Text, "reflection loop is not running",
		"the message names the not-running reflection loop")
}

// TestInterceptThoughts_MalformedArgs pins the two entry points diverging on a
// JSON parse error, which they now do deliberately.
//
// `thoughts` CLAIMS it: this intercept is the terminal claimer for that tool,
// so nothing downstream would produce a better message than the engine's
// tool-level deny. `query` still DECLINES: the reflective arm is one claimant
// among many for that tool name, and claiming every malformed query payload
// here would starve the arms behind it.
//
// THE MESSAGE NOW COMES FROM PARAM ACCOUNTING, not from the decode. The
// accounting gate runs ahead of the decode, and an unparseable payload means it
// could not read the supplied keys — which it reports rather than passing
// through, since a payload it cannot read is one it cannot account for. The
// property this test pins is unchanged: thoughts answers a malformed payload
// itself, naming the tool, and query still falls through to the arms behind it.
func TestInterceptThoughts_MalformedArgs(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}

	handled, res := InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
		Name: "thoughts", Arguments: json.RawMessage(`{not json`),
	})
	require.True(t, handled, "a malformed thoughts payload is answered here")
	assert.True(t, res.IsError)
	assert.Contains(t, contentText(res), "thoughts: param accounting could not read the supplied params")

	handled, _ = InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
		Name: "query", Arguments: json.RawMessage(`{not json`),
	})
	assert.False(t, handled, "a malformed query payload must still reach the arms behind this one")
}

// TestInterceptThoughts_StatusOnTypedBrowse_NotClaimed pins the routing rule a
// status filter obeys: `status` is a thought-graph property only when the query
// is ABOUT thoughts. A typed browse that happens to carry a status filter —
// query(type:"step", status:"completed") — is a knowledge-graph browse, and
// claiming it here answers it from the thought corpus instead: wrong rows, no
// error, the worst failure shape available to a dispatch predicate.
//
// The two reproduction cases assert the claim is released. The four fence cases
// assert the narrowing stopped where it should: a bare status filter, the two
// explicit thought-corpus type spellings, and — the one that keeps the fix from
// being written as "type disables recall" — a typed query carrying a genuine
// six-term thought field, which must still route to recall regardless of type.
func TestInterceptThoughts_StatusOnTypedBrowse_NotClaimed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		args    string
		claimed bool
	}{
		// The live reproductions: a typed browse is not a thought query.
		{"typed_step_with_status", `{"type":"step","status":"completed"}`, false},
		{"typed_finding_with_status", `{"type":"finding","status":"open"}`, false},
		// Scope fences against over-narrowing.
		{"status_only", `{"status":"completed"}`, true},
		{"type_thought", `{"type":"thought","status":"validated"}`, true},
		{"type_all", `{"type":"all","status":"anything"}`, true},
		{"typed_step_with_session", `{"type":"step","session":"s"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := interceptTestDeps{gc: &fakeGraphCaller{}}
			handled, _ := InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
				Name:      "query",
				Arguments: json.RawMessage(tc.args),
			})
			if tc.claimed {
				assert.True(t, handled,
					"%s is a thought-corpus query and must still route to recall: %s", tc.name, tc.args)
				return
			}
			assert.False(t, handled,
				"%s is a knowledge-graph browse — claiming it answers from the thought corpus: %s",
				tc.name, tc.args)
		})
	}
}

// TestThoughtFilterCoreTermsMatchKnowledgeSearchSibling is the anti-drift
// catcher for the three copies of "does this query concern thoughts". The
// status misrouting existed precisely because one copy grew a term the others
// never got, and nothing in the suite could see the copies disagree.
//
// Each case feeds the SAME payload bytes to both readers — InterceptThoughts
// for the thought.go core, hasThoughtQueryFilter for the knowledge-search
// sibling — and asserts they reach the same verdict. queryReflectArgs and
// queryArgs are distinct structs with distinct field sets, so the agreement is
// asserted through the two readers rather than factored into a shared
// predicate. A seventh term added to one copy alone fails on that term's case.
func TestThoughtFilterCoreTermsMatchKnowledgeSearchSibling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args string
		// sibling is the verdict hasThoughtQueryFilter must reach. It matches
		// the thought.go core on the six shared terms and diverges on status.
		sibling bool
	}{
		{"valence_min", `{"valence_min":0.5}`, true},
		{"valence_max", `{"valence_max":0.9}`, true},
		{"magnitude_min", `{"magnitude_min":1.0}`, true},
		{"consistency_max", `{"consistency_max":0.5}`, true},
		{"session", `{"session":"s"}`, true},
		{"connected_to", `{"connected_to":"node-1"}`, true},
		// DELIBERATE DIVERGENCE, not drift. A bare status routes to recall on
		// the thought.go side because an absent type means the thought corpus
		// there; the sibling never treats status as a thought signal because
		// every node type has one. The type guard in interceptQueryReflect is
		// what makes the thought.go side safe — do not "fix" this to agree.
		{"status_divergence_by_design", `{"status":"validated"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := interceptTestDeps{gc: &fakeGraphCaller{}}
			handled, _ := InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
				Name:      "query",
				Arguments: json.RawMessage(tc.args),
			})
			assert.True(t, handled,
				"the thought.go core must treat %s as thought-flavored: %s", tc.name, tc.args)

			var a queryArgs
			require.NoError(t, json.Unmarshal([]byte(tc.args), &a))
			assert.Equal(t, tc.sibling, hasThoughtQueryFilter(a),
				"hasThoughtQueryFilter disagrees with the thought.go core on %s — the copies have "+
					"drifted apart, which is the defect this catcher exists for: %s", tc.name, tc.args)
		})
	}
}

// TestInterceptThoughts_EvolutionRequiresClusters pins the cluster_a /
// cluster_b validation that mirrors the former server-side check.
func TestInterceptThoughts_EvolutionRequiresClusters(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	params := kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"evolution"}`),
	}
	handled, res := InterceptThoughts(opCtx(), deps, params)
	assert.True(t, handled, "evolution mode is claimed even when validation fails")
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "cluster_a")
}
