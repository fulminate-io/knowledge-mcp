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
