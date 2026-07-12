// SPDX-License-Identifier: Apache-2.0

// thought_test.go — unit tests for the client-side InterceptThoughts
// dispatch. Asserts:
//
//   - name-filtering: only "thoughts" / "query" tool calls are considered
//   - operation/mode routing: recognized ops/modes return (true, _);
//     unrecognized ones fall through (false, zero)
//   - malformed JSON falls through unchanged
//
// The handlers themselves require a live *graphclient.GraphClient (they
// issue real wire calls), so the tests cover only the dispatch shape.
// Wire-mode tests live alongside the reflective package itself.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// thoughtTestDeps satisfies ClientDeps for dispatch-only tests. All
// accessors return zero values — the handlers reaching the
// GraphClient-using code path return an explicit "graph client
// unavailable" error, which the tests assert.
type thoughtTestDeps struct{}

func (thoughtTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (thoughtTestDeps) Sink() collector.Sink                         { return nil }
func (thoughtTestDeps) RootDir() string                              { return "" }
func (thoughtTestDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (thoughtTestDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (thoughtTestDeps) WorkerReady() bool                            { return true }
func (thoughtTestDeps) PropReady() bool                              { return true }
func (thoughtTestDeps) PipelineReady() bool                          { return true }
func (thoughtTestDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (thoughtTestDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (thoughtTestDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (thoughtTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (thoughtTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (thoughtTestDeps) BackendResolver() BackendResolver             { return nil }
func (thoughtTestDeps) GraphCaller() GraphCaller                     { return nil }
func (thoughtTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (thoughtTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (thoughtTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (thoughtTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (thoughtTestDeps) SegmentPruner() SegmentPruner                 { return nil }
func (thoughtTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (thoughtTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (thoughtTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (thoughtTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (thoughtTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (thoughtTestDeps) ClusterProvider() ClusterProvider     { return nil }
func (thoughtTestDeps) TensionsProvider() TensionsProvider   { return nil }

// TestInterceptThoughts_NameFiltering pins that non-thoughts /
// non-query tool calls fall through unchanged so the intercept chain's
// other handlers see them.
func TestInterceptThoughts_NameFiltering(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	for _, name := range []string{"worker", "ast", "collect", "manage", "search", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptThoughts(deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptThoughts", name)
		assert.Empty(t, res.Content, "non-thoughts/query call must return zero ToolResult")
	}
}

// TestInterceptThoughts_ThoughtsFallthroughUnknownOp pins that an
// UNRECOGNIZED thoughts op falls through unchanged so the server can
// surface its own error. Every documented op — think, charge, recall,
// trace, propagate, similarity_report, adjacency, charges_for — is
// claimed client-side; the dispatch-claim of adjacency + charges_for is
// asserted positively in their own intercept tests.
func TestInterceptThoughts_ThoughtsFallthroughUnknownOp(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	cases := []string{
		`{"operation":"unknown-op"}`,
	}
	for _, args := range cases {
		params := kgtools.CallToolParams{Name: "thoughts", Arguments: json.RawMessage(args)}
		handled, _ := InterceptThoughts(deps, params)
		assert.False(t, handled, "thoughts op %q must fall through to server", args)
	}
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
		handled, res := InterceptThoughts(deps, params)
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
		handled, _ := InterceptThoughts(deps, params)
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
	for mode, wantErr := range requiresGC {
		args := `{"mode":"` + mode + `","cluster_a":"A","cluster_b":"B"}`
		params := kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(args)}
		handled, res := InterceptThoughts(deps, params)
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
	handled, res := InterceptThoughts(deps, params)
	assert.True(t, handled, "blind_spots is handled client-side")
	assert.False(t, res.IsError, "nil provider returns a non-error cold/loop-not-running message, not an error")
	assert.Contains(t, res.Content[0].Text, "reflection loop is not running",
		"the message names the not-running reflection loop")
}

// TestInterceptThoughts_MalformedArgsFallthrough pins that JSON parse
// errors don't claim the call — the server-side handler reparses and
// surfaces the canonical error message.
func TestInterceptThoughts_MalformedArgsFallthrough(t *testing.T) {
	t.Parallel()
	deps := thoughtTestDeps{}
	for _, name := range []string{"thoughts", "query"} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{not json`)}
		handled, _ := InterceptThoughts(deps, params)
		assert.False(t, handled, "malformed %q args must fall through to server", name)
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
	handled, res := InterceptThoughts(deps, params)
	assert.True(t, handled, "evolution mode is claimed even when validation fails")
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "cluster_a")
}
