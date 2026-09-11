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
	gc := newFanOutHarness(t, []string{"default"},
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
	t.Run("A_no_selector_browse_filter_is_SERVED_not_refused", func(t *testing.T) {
		// THIS LEG INVERTED WITH THE COMBINED GRAPH, and the inversion is the
		// requirement rather than an accommodation. A filtered browse with no
		// selector used to be refused because it named no graph — the enumeration
		// was what an empty selector meant. There is ONE graph now, so the filter
		// has somewhere to apply and the call is served.
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "type": "pattern", "limit": 3,
		})
		require.True(t, handled)
		assert.False(t, res.IsError,
			"an unselected practice browse reads the ONE combined graph: %s", textBodyTools(res))
		assert.NotContains(t, textBodyTools(res), "Practice graphs",
			"and it browses NODES rather than enumerating graphs, which is the shape that moved")
	})

	t.Run("A2_a_browse_filter_on_the_ENUMERATION_is_still_refused", func(t *testing.T) {
		// The enumeration survives as the CATALOG read, asked for by name, and it
		// still takes no filters — the half of shape A that did not invert.
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "mode": "modules", "type": "pattern", "limit": 3,
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a browse filter on the enumeration is refused")
		body := textBodyTools(res)
		assert.Contains(t, body, `query(graph:"practice", type:`,
			"the refusal names the combined-graph browse, which is the call that works")
		assert.NotContains(t, body, "drop it or issue a separate call that does",
			"the generic tail is replaced — the caller does not know which separate call to issue")
	})

	t.Run("B_by_id_without_a_selector_is_SERVED_not_refused", func(t *testing.T) {
		// The second inversion. A language-less by-id read used to name no graph
		// and could resolve nowhere, so it was claimed here purely to say which
		// call worked. It resolves the one combined graph now, so this entry point
		// declines it to the by-id arm that serves it.
		handled, _ := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "id": "dde3a949b972cd6c",
		})
		assert.False(t, handled,
			"an id-bearing practice payload declines to the by-id arm, which resolves the combined graph")
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
		// THE MESSAGE IS THE FIELD'S, NOT THE SENTINEL'S. "all" had a refusal of
		// its own while every other value of `language` still read a graph; every
		// value is refused now, with one spelling, so a per-value wording would be
		// a second message for a rule reachable one way.
		assert.Contains(t, body, "does not accept `language`",
			"the refusal names the field rather than the value")
		assert.Contains(t, body, `source:"<hub id>"`,
			"and names the selector that does narrow the corpus")
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
		assert.NotContains(t, textBodyTools(res), "does not accept `language` on a write",
			"and is therefore NOT answered by a practice write refusal")
	})

	t.Run("D_the_working_call_still_succeeds", func(t *testing.T) {
		// BOTH DIRECTIONS. Without this, every leg above is satisfiable by an
		// implementation that refuses every practice query. The working call used
		// to be a language-scoped browse; it is the unselected one, narrowed by
		// `source` where a caller wants less than the whole corpus.
		handled, res := drivePracticeSelector(t, map[string]any{
			"graph": "practice", "type": "idiom",
		})
		require.True(t, handled)
		assert.False(t, res.IsError, "an unselected browse is served: %s", textBodyTools(res))
	})

	t.Run("E_the_enumeration_itself_still_succeeds", func(t *testing.T) {
		// The second both-directions leg. The enumeration is asked for BY NAME now
		// — a bare graph:"practice" browses the combined graph — but it still
		// works, which is what keeps the catalog read reachable.
		handled, res := drivePracticeSelector(t, map[string]any{"graph": "practice", "mode": "modules"})
		require.True(t, handled)
		assert.False(t, res.IsError, "the enumeration is untouched: %s", textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "Practice graphs", "it still renders the graph list")
	})
}
