// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_checks_search_test.go pins the checks cutover: both rails serve, both
// refuse a non-empty instance name, neither claims the non-search shapes, and the
// retired refusal is gone from both while transformers' survives.

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// checksSearchHarness wires a deps whose segment manager RECORDS which graph it
// was searched for. Observing the search is the point: asserting the response is
// non-empty would be satisfied by the refusal string this cutover retires.
func checksSearchHarness(t *testing.T) (*interceptDeps, *fakeSegmentSearcher) {
	t.Helper()
	var execHits atomic.Int64
	gc, _ := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
	return &interceptDeps{gc: gc, segMgr: mgr}, mgr
}

// TestInterceptChecksSearch_ServedOnBothRails asserts both rails reach the client
// segment engine, both refuse a non-empty instance name, and neither claims the
// non-search shapes.
//
// SIX ARMS IN THREE PAIRS SO NEITHER RAIL CAN PASS ON THE OTHER'S BEHALF. The
// query rail is the one the original single-rail plan would have missed: with the
// refusal retired and no query-rail arm, query(graph:"checks", text:...) falls to
// the generic dispatch tail and renders a FALSE BM25-only disclosure — a footer
// asserting a retrieval arm answered when none did.
func TestInterceptChecksSearch_ServedOnBothRails(t *testing.T) {
	t.Run("the SEARCH rail reaches the segment engine for the checks graph", func(t *testing.T) {
		deps, mgr := checksSearchHarness(t)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "checks", "query": "bucket count provenance",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "the checks search must be served: %s", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load(), "the arm must SEARCH, not answer from a string")
		assert.Equal(t, kgtypes.GraphChecks, mgr.lastGT, "the engine must be searched FOR the checks graph")
		// THIS ASSERTION USED TO REQUIRE AN EMPTY NAME, AND IT PINNED A DEFECT.
		// "checks addresses no instance name" is true of the WIRE SELECTOR and
		// false of the ENGINE KEY: the collector seals the graph's segments under
		// the canonical instance, so a search asking for "" reached a different,
		// empty engine instance and returned a confident zero. The two namespaces
		// are now resolved separately; intercept_checks_search_keying_test.go
		// drives the corpus end of it against a keyed engine.
		assert.Equal(t, workingset.CanonicalInstanceName(kgtypes.GraphChecks, ""), mgr.lastName,
			"the engine is keyed by the canonical instance, whatever the caller's selector may carry")
		assert.Equal(t, "bucket count provenance", mgr.lastText, "the caller's query must reach the engine")
	})

	t.Run("the QUERY rail reaches the segment engine for the checks graph", func(t *testing.T) {
		deps, mgr := checksSearchHarness(t)
		handled, out := InterceptQueryPracticeLinkage(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "checks", "text": "bucket count provenance",
		}))
		require.True(t, handled, "the query rail must CLAIM a checks text search")
		require.False(t, out.IsError, "the checks query search must be served: %s", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load(), "the query rail must SEARCH, not fall to the generic tail")
		assert.Equal(t, kgtypes.GraphChecks, mgr.lastGT)
		assert.Equal(t, workingset.CanonicalInstanceName(kgtypes.GraphChecks, ""), mgr.lastName,
			"both rails must key the engine the same way, or one of them searches an instance nothing wrote to")
	})

	t.Run("the SEARCH rail refuses a non-empty instance name", func(t *testing.T) {
		deps, mgr := checksSearchHarness(t)
		_, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "checks", "name": "go", "query": "x",
		}))
		require.True(t, out.IsError, "checks addresses no instance field, so a name must be REFUSED not ignored")
		assert.Contains(t, engine.FirstTextContent(out), "singleton")
		assert.Zero(t, mgr.calls.Load(), "a refused selector must never reach the engine")
	})

	t.Run("the QUERY rail refuses a non-empty instance name", func(t *testing.T) {
		deps, mgr := checksSearchHarness(t)
		_, out := InterceptQueryPracticeLinkage(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "checks", "name": "go", "text": "x",
		}))
		require.True(t, out.IsError, "an ignored name would disagree with the corpus loader about the selector")
		assert.Contains(t, engine.FirstTextContent(out), "singleton")
		assert.Zero(t, mgr.calls.Load())
	})

	// THE NON-SEARCH SHAPES. An arm that claimed these would return a clean render
	// of a DIFFERENT operation, with nothing in the response marking it wrong — and
	// the browse is exactly what a caller inventorying the corpus reaches for.
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"a checks plural-type browse keeps its own path", map[string]any{
			"graph": "checks", "types": []string{"finding", "example"}}},
		{"a checks by-id read stays a lookup even with text riding along", map[string]any{
			"graph": "checks", "id": "some-check-id", "text": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, mgr := checksSearchHarness(t)
			handled, _ := InterceptQueryPracticeLinkage(opCtx(), deps, queryParams(t, tc.args))
			assert.False(t, handled, "%s must fall through to the path that serves it", tc.name)
			assert.Zero(t, mgr.calls.Load(), "a non-search shape must drive no ranked search")
		})
	}
}

// TestUnrankedBuiltinRefusal_ChecksRetiredTransformersSurvives is an absence
// assertion WITH its survivor named.
//
// THE SURVIVOR IS WHAT STOPS IT BEING SATISFIABLE BY DELETION. Without the
// transformers arm, ripping out the whole unranked-builtin refusal mechanism would
// pass every assertion about checks no longer being refused.
func TestUnrankedBuiltinRefusal_ChecksRetiredTransformersSurvives(t *testing.T) {
	const retiredMarker = "not available yet"

	t.Run("neither rail refuses checks any more", func(t *testing.T) {
		// The refusal switch does not claim it.
		_, claimed := unrankedBuiltinRefusalFor("checks")
		assert.False(t, claimed, "the refusal switch must no longer claim the checks graph")

		deps, _ := checksSearchHarness(t)
		_, searchOut := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "checks", "query": "x",
		}))
		assert.NotContains(t, engine.FirstTextContent(searchOut), retiredMarker,
			"the search rail must not return the retired checks refusal")

		queryHandled, queryOut := InterceptQueryUnrankedBuiltin(opCtx(), &interceptDeps{},
			queryParams(t, map[string]any{"graph": "checks", "mode": "text", "text": "x"}))
		assert.False(t, queryHandled, "the query refusal arm must not claim checks")
		assert.NotContains(t, engine.FirstTextContent(queryOut), retiredMarker)
	})

	t.Run("the transformers refusal survives on both rails, word for word", func(t *testing.T) {
		refusal, claimed := unrankedBuiltinRefusalFor("transformers")
		require.True(t, claimed, "transformers still carries no segments and must still be refused")
		want := engine.FirstTextContent(refusal)
		require.NotEmpty(t, want, "an empty survivor would make every assertion below vacuous")
		assert.Contains(t, want, "not available")
		assert.Contains(t, want, `query(graph:"transformers", name:"recipes", type:"recipe")`)

		deps, mgr := checksSearchHarness(t)
		_, searchOut := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "transformers", "query": "x",
		}))
		assert.Equal(t, want, engine.FirstTextContent(searchOut),
			"the search rail must still answer transformers with the shared refusal")
		assert.Zero(t, mgr.calls.Load(), "a refusal must drive no ranked search")

		queryHandled, queryOut := InterceptQueryUnrankedBuiltin(opCtx(), &interceptDeps{},
			queryParams(t, map[string]any{"graph": "transformers", "name": "recipes", "mode": "text", "text": "x"}))
		require.True(t, queryHandled, "the query refusal arm must still claim transformers")
		assert.Equal(t, want, engine.FirstTextContent(queryOut),
			"both rails must still give transformers callers the same advice")
	})

	t.Run("the retired wording ships nowhere in the tools package", func(t *testing.T) {
		// The message is gone from the code, not merely unreachable. A dead
		// composer left behind is the shape a later reader re-wires by accident.
		for _, graph := range []string{"checks", "transformers"} {
			res, claimed := unrankedBuiltinRefusalFor(graph)
			if !claimed {
				continue
			}
			assert.NotContains(t, engine.FirstTextContent(res), retiredMarker,
				"no surviving refusal may carry the retired checks wording")
		}
	})
}
