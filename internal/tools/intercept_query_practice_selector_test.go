// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_selector_test.go covers the three practice-selector
// SHAPES whose refusals had to start naming the call that works: the list-graphs
// arm dropping browse filters, a by-id read with no language, and the text-less
// language:"all" fan-out that answered with a confident zero.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// drivePracticeSelector runs one payload through the real practice entry point.
func drivePracticeSelector(t *testing.T, args map[string]any) (bool, kgtools.ToolResult) {
	t.Helper()
	gc := newFanOutHarness(t, []string{"go-idioms", "postgres-best-practices"},
		practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}}
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return InterceptQueryPracticeLinkage(opCtx(), &interceptDeps{gc: gc, segMgr: mgr},
		kgtools.CallToolParams{Name: "query", Arguments: raw})
}

// TestPracticeSelector_RefusalNamesTheWorkingCall (FAILS-WHEN-ABSENT) asserts the
// three shapes on MESSAGE CONTENT rather than merely on erroring — a refusal that
// does not name a working call is the defect, not the fix — plus the both-
// directions leg without which every assertion here is satisfiable by an
// implementation that refuses every practice query.
func TestPracticeSelector_RefusalNamesTheWorkingCall(t *testing.T) {
	t.Run("A_no_language_browse_filter_names_language", func(t *testing.T) {
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "type": "pattern", "limit": 3,
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a browse filter on the enumeration is refused")
		body := textBodyTools(res)
		assert.Contains(t, body, "language", "the refusal names `language` as the working selector")
		assert.Contains(t, body, `query(graph:"practice", language:`, "it names the call, not just the param")
		assert.NotContains(t, body, "drop it or issue a separate call that does",
			"the generic tail is replaced — the caller does not know that language IS the separate call")
	})

	t.Run("B_by_id_without_language_names_the_enumeration", func(t *testing.T) {
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "id": "dde3a949b972cd6c",
		})
		require.True(t, handled, "claimed client-side purely to refuse it legibly")
		require.True(t, res.IsError)
		body := textBodyTools(res)
		assert.Contains(t, body, `mode:"modules"`, "the refusal names the enumeration call")
		assert.Contains(t, body, `language:"<lang>"`, "and the by-id call that works")
	})

	t.Run("C_textless_all_is_refused_not_answered_with_a_zero", func(t *testing.T) {
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "language": "all", "limit": 3,
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a text-less fan-out is REFUSED")
		body := textBodyTools(res)
		// The ABSENCE leg is what distinguishes the fix from a friendlier header on
		// the same confident zero: an implementation that still ran the empty-text
		// fan-out would render a "0 results" search body.
		assert.NotContains(t, body, "0 results",
			"the vacuous zero-result render is gone, not merely reworded; got: %s", body)
		assert.NotContains(t, body, "Searched", "no scatter-gather ran")
		assert.Contains(t, body, "text", "the refusal names the missing input")
		assert.Contains(t, body, `mode:"modules"`, "and the enumeration call")
	})

	t.Run("B2_a_mode_bearing_id_shape_is_left_to_the_arm_that_owns_it", func(t *testing.T) {
		// THE REGRESSION THIS PINS: shape B's guard must not claim every id-bearing
		// practice payload. A mode carries the call to an arm that owns it and
		// refuses it BY NAME — mode:"examine" names the graph and the surface
		// examine does serve, a better message than shape B's. The first version of
		// the guard stole those shapes and the bootstrap parity suite caught it;
		// this leg is what catches it here, one package earlier.
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "mode": "examine", "id": "dde3a949b972cd6c",
		})
		assert.False(t, handled, "a mode-bearing practice payload declines to the arm that owns it")
		assert.NotContains(t, textBodyTools(res), "keys its instance by language",
			"and is therefore NOT answered by the by-id refusal")
	})

	t.Run("D_the_working_call_still_succeeds", func(t *testing.T) {
		// BOTH DIRECTIONS. Without this, every leg above is satisfiable by an
		// implementation that refuses every practice query.
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "language": "go-idioms", "type": "idiom",
		})
		require.True(t, handled)
		assert.False(t, res.IsError, "a language-scoped browse is still served: %s", textBodyTools(res))
	})

	t.Run("E_the_enumeration_itself_still_succeeds", func(t *testing.T) {
		// The second both-directions leg: shape A refuses a FILTER, never the
		// enumeration, so a bare graph:"practice" must still list the graphs.
		handled, res := drivePracticeSelector(t, map[string]any{"graph": "practice"})
		require.True(t, handled)
		assert.False(t, res.IsError, "the bare enumeration is untouched: %s", textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "Practice graphs", "it still renders the graph list")
	})
}
