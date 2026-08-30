// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// registered_graph_selector_test.go covers the CLIENT-side graph-selector
// validation on the custom-graph search arm: the gate that makes an unknown
// graph type and a never-collected registered graph LOUD instead of a clean
// zero, converging the client on the two classifications the server's
// resolveRegisteredCustom already draws (ErrGraphSelectorInvalid for an
// unregistered TYPE, ErrGraphNotFound for an absent INSTANCE).

// registeredGraphTypes builds a graph-type registry that knows exactly the named
// custom types. The custom-graph arms in this package share it: a fixture that
// means "this custom type IS registered" has to say so, because an unwired
// registry is now a refusal rather than a silent empty set.
func registeredGraphTypes(names ...string) *fakeGraphTypeCRUD {
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{}}
	for _, name := range names {
		crud.graph[name] = &knowledgev1.GraphTypeDef{Name: name}
	}
	return crud
}

// registeredGraphFixture builds the deps + handler for a custom-graph search:
// registered names the graph TYPES the registry knows, collected names the
// graph INSTANCES that have been collected for the type under test. Either can
// be empty — that is how the two loud cases are expressed.
func registeredGraphFixture(
	t *testing.T, registered []string, collected []string, hits []searchengine.Hit, nodes ...*knowledgev1.Node,
) (*interceptDeps, *fakeSegmentSearcher, *dispatchEngineHandler) {
	t.Helper()
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(nodes...))
	handler.graphNames = collected
	mgr := &fakeSegmentSearcher{hits: hits}
	return &interceptDeps{
		gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr,
		gtCRUD: registeredGraphTypes(registered...),
	}, mgr, handler
}

// TestRegisteredGraphSelector_UnknownTypeIsLoud is the core of the ruling: a
// graph selector naming a type no registry knows is an ERROR that names the
// value and the accepted vocabulary, on BOTH tools. graph:"all" — an enum token
// that was advertised for months with no dispatch arm behind it — and a plain
// typo are the SAME defect and get the same treatment; the byte-identical clean
// zero they used to render was what made them indistinguishable from a real
// empty result.
func TestRegisteredGraphSelector_UnknownTypeIsLoud(t *testing.T) {
	for _, graph := range []string{"all", "zzznotagraph"} {
		t.Run("search tool/"+graph, func(t *testing.T) {
			deps, mgr, _ := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, nil)

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
				"graph": graph, "query": "world",
			}))
			require.True(t, handled, "the custom-graph arm claims the call so it can refuse it")
			require.True(t, out.IsError, "an unknown graph type must be an error, not a clean zero")
			body := engine.FirstTextContent(out)
			assert.Contains(t, body, `unsupported graph type "`+graph+`"`,
				"the refusal must name the offending value, mirroring the server's ErrGraphSelectorInvalid shape")
			for _, vocab := range []string{"knowledge", "code", "practice", "logs"} {
				assert.Contains(t, body, vocab, "the refusal must list the accepted vocabulary")
			}
			assert.Equal(t, int64(0), mgr.calls.Load(),
				"an invalid selector must be refused BEFORE the segment engine is driven")
		})

		t.Run("query tool/"+graph, func(t *testing.T) {
			deps, mgr, _ := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, nil)

			handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
				"graph": graph, "mode": "hybrid", "text": "world",
			}))
			require.True(t, handled)
			require.True(t, out.IsError, "an unknown graph type must be an error, not a clean zero")
			assert.Contains(t, engine.FirstTextContent(out), `unsupported graph type "`+graph+`"`)
			assert.Equal(t, int64(0), mgr.calls.Load())
		})
	}
}

// TestRegisteredGraphSelector_RegisteredButNeverCollectedIsLoud pins the second
// half of the ruling. A REGISTERED type whose named graph has never been
// collected is not an empty search result — it is a not-found, the same
// classification the server already returns (ErrGraphNotFound, pinned by
// TestResolveGraphDB_RegisteredCustomType's "registered type with absent named
// graph" case). The client says so and names the collect path, so a forgotten
// collect cannot read as searched-and-absent.
func TestRegisteredGraphSelector_RegisteredButNeverCollectedIsLoud(t *testing.T) {
	t.Run("no graph of the type has ever been collected", func(t *testing.T) {
		deps, mgr, _ := registeredGraphFixture(t, []string{"hellograph"}, nil, nil)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "query": "world",
		}))
		require.True(t, handled)
		require.True(t, out.IsError, "a never-collected registered graph must be loud")
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, `hellograph graph "demo" not found`,
			"the refusal must name the graph, mirroring the server's ErrGraphNotFound shape")
		assert.Contains(t, body, `collect(type:"hellograph"`,
			"the refusal must name the collect path that would fix it")
		assert.Equal(t, int64(0), mgr.calls.Load(),
			"a not-found instance must be refused BEFORE the segment engine is driven")
	})

	t.Run("the type has collected graphs but not this one", func(t *testing.T) {
		deps, _, _ := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, nil)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "typo", "query": "world",
		}))
		require.True(t, handled)
		require.True(t, out.IsError)
		body := engine.FirstTextContent(out)
		assert.Contains(t, body, `hellograph graph "typo" not found`)
		assert.Contains(t, body, "demo", "the refusal must name the graphs that DO exist")
	})

	t.Run("an unnamed instance is a not-found, not a graceful empty", func(t *testing.T) {
		// The retired lane: an empty instance key used to reach Manager.Search and
		// render zero results "cleanly". The server resolves the same selector to
		// ErrGraphNotFound, so the client does too.
		deps, mgr, _ := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, nil)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "query": "world",
		}))
		require.True(t, handled)
		require.True(t, out.IsError, "an empty instance key is a not-found, not a clean zero")
		assert.Contains(t, engine.FirstTextContent(out), `hellograph graph "" not found`)
		assert.Equal(t, int64(0), mgr.calls.Load())
	})
}

// TestRegisteredGraphSelector_RegistryUnreadableIsLoud pins the third refusal:
// when the registry itself cannot be read, the arm says so. Without this the
// gate would answer "nothing is registered" for an unreachable registry and
// refuse every custom graph with the wrong reason.
func TestRegisteredGraphSelector_RegistryUnreadableIsLoud(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, _ := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp())
	mgr := &fakeSegmentSearcher{}
	// gtCRUD unset: the registry seam is unwired.
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "hellograph", "name": "demo", "query": "world",
	}))
	require.True(t, handled)
	require.True(t, out.IsError, "an unreadable registry must be loud, never read as 'nothing registered'")
	assert.Contains(t, engine.FirstTextContent(out), "graph-type registry")
	assert.Equal(t, int64(0), mgr.calls.Load())
}

// TestRegisteredGraphSelector_RegisteredAndCollectedStillSearches is the
// known-positive control for every refusal above: with the SAME gate in place, a
// registered type whose named graph HAS been collected reaches the client
// segment engine and renders its hit. Without this case the refusals would be
// satisfied by a gate that rejects everything.
func TestRegisteredGraphSelector_RegisteredAndCollectedStillSearches(t *testing.T) {
	hit := []searchengine.Hit{{ID: "h1", Score: 0.9}}
	node := &knowledgev1.Node{Id: "h1", Type: "fact", SymbolName: "HelloWorld"}

	t.Run("search tool", func(t *testing.T) {
		deps, mgr, handler := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, hit, node)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "query": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "a registered, collected graph searches: %v", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load(), "the client segment engine ran")
		assert.Equal(t, kgtypes.GraphType("hellograph"), mgr.lastGT)
		assert.Equal(t, "demo", mgr.lastName)
		assert.Contains(t, engine.FirstTextContent(out), "HelloWorld")
		assert.False(t, dispatchedAServerSearch(handler.recordedReqs()),
			"the validated custom search must still NOT dispatch a server RETURN_MODE_SEARCH")
	})

	t.Run("query tool", func(t *testing.T) {
		deps, mgr, _ := registeredGraphFixture(t, []string{"hellograph"}, []string{"demo"}, hit, node)

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "hybrid", "text": "world",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "a registered, collected graph searches: %v", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load())
		assert.Contains(t, engine.FirstTextContent(out), "HelloWorld")
	})
}

// TestRegisteredGraphSelector_InstanceFanOutsAreUntouched is the control for the
// blast radius of the gate. The ruling validates the GRAPH TYPE selector; the two
// decided INSTANCE fan-outs — language:"all" across practice graphs and
// repo:"all" across code repos — are builtin-graph selectors that never reach the
// custom-graph arm and must keep working unchanged.
//
// Both fixtures deliberately leave the graph-type registry UNWIRED, which is the
// state that refuses every custom graph outright
// (TestRegisteredGraphSelector_RegistryUnreadableIsLoud). A fan-out that still
// returns both instances' hits under that condition is proof the gate does not
// sit on these paths, rather than proof it happened to pass.
func TestRegisteredGraphSelector_InstanceFanOutsAreUntouched(t *testing.T) {
	t.Run(`practice language:"all" still fans out`, func(t *testing.T) {
		gc := newFanOutHarness(t, []string{"go", "python"},
			&knowledgev1.Node{Id: "n-go", Type: "pattern", SymbolName: "GoPattern"},
			&knowledgev1.Node{Id: "n-py", Type: "pattern", SymbolName: "PyPattern"},
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "n-go", Score: 0.90}},
			"python": {{ID: "n-py", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		// language:"all" is a QUERY-tool selector: the search tool has no language
		// param and always fans out across every practice graph, so the query arm
		// is where the "all" instance selector is actually read.
		handled, out := InterceptQueryPracticeLinkage(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "practice", "language": "all", "text": "x",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, textBodyTools(out))
		body := textBodyTools(out)
		assert.Contains(t, body, "GoPattern", "the go practice graph's hit survived the fan-out")
		assert.Contains(t, body, "PyPattern", "the python practice graph's hit survived the fan-out")
	})

	t.Run(`code repo:"all" still fans out`, func(t *testing.T) {
		// Empty temp manifest: the fan-out detects each repo's branch from the
		// machine-local manifest, and this control is about the fan-out surviving,
		// not about branch overlays.
		withTestManifest(t)
		gc := newFanOutHarness(t, []string{"repoA", "repoB"},
			&knowledgev1.Node{Id: "a.go:A", SymbolName: "AlphaFunc", Type: "function", FilePath: "a.go", StartLine: 1},
			&knowledgev1.Node{Id: "b.go:B", SymbolName: "BetaFunc", Type: "function", FilePath: "b.go", StartLine: 1},
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"repoA": {{ID: "a.go:A", Score: 0.90}},
			"repoB": {{ID: "b.go:B", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "code", "repo": "all", "query": "x",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, textBodyTools(out))
		body := textBodyTools(out)
		assert.Contains(t, body, "AlphaFunc", "repoA's hit survived the cross-repo fan-out")
		assert.Contains(t, body, "BetaFunc", "repoB's hit survived the cross-repo fan-out")
	})
}
