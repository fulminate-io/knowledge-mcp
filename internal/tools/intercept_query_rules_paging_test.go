// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_rules_paging_test.go pins the rule browse's READ SHAPE: how
// many rows the arm's own fetch asks the server for, how many requests it
// issues, and whether the number the render prints is the page or the corpus.
//
// THE HARNESS FACT THAT DECIDES EVERY ASSERTION. fakeGraphCaller.Execute's
// Match(NodeType) branch serves nodeMatchResults keyed by graphKey and IGNORES
// the plan's Limit and Offset entirely. So "I asked for limit:1 and got 1 row"
// is UNSATISFIABLE against this fake however the arm behaves, and "I asked for
// limit:1 and got 3 rows" is VACUOUS — true whether the limit was routed or
// dropped. Every fetch-level assertion here therefore reads the CAPTURED
// REQUEST (fc.execRequests) and every result-level assertion reads the rendered
// output. Do not teach the fake to honor Limit; it is a shared fixture dozens of
// tests in this package depend on.
//
// WHAT EACH SUBTEST MEASURED BEFORE THE FIX, retained so a later reader can tell
// this intentional flip from a regression. The arm used to marshal its OWN fixed
// rule-browse payload carrying no row count, which the engine's
// applyBrowseLimitOffset rewrote to browseDefaultLimit — so a BARE browse asked
// the server for ten rules however large the corpus was, and the render printed
// that page's length as though it were the total. The caller's own limit and
// offset never reached the wire at all: the param-accounting gate classified
// both REJECTED on this arm, so a payload carrying them was refused before any
// read.
//
// ALL FOUR OF THOSE OBSERVATIONS ARE NOW FALSE, which is the point. The fetch is
// a keyset drain at paging.BrowsePageSize with the cursor SET on page one, the
// two paging params are consumed client-side against the scope-filtered set, and
// the render reports the page beside the matching total. Each subtest below
// names the value it used to see next to the value it asserts now.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// seedPagingRules returns n rule nodes under the knowledge graphKey the rule
// arm's fetch targets. Ids are zero-padded so the natural order is stable and a
// keyset cursor taken from the last row of a page is meaningful.
func seedPagingRules(n int) *fakeGraphCaller {
	nodes := make([]*knowledgev1.Node, 0, n)
	for i := range n {
		node := &knowledgev1.Node{
			Id:          fmt.Sprintf("%032d", i+1),
			Type:        string(kgtypes.NodeRule),
			SymbolName:  fmt.Sprintf("rule-%02d", i+1),
			Description: fmt.Sprintf("description of rule %02d", i+1),
			Status:      "active",
		}
		nodes = append(nodes, node)
	}
	return &fakeGraphCaller{nodeMatchResults: map[graphKey][]*knowledgev1.Node{
		{Type: "knowledge"}: nodes,
	}}
}

// seedScopedPagingRules returns n rule nodes of which the first scoped carry a
// scope metadata value containing "commits". The scope filter reads
// scope+description, so the remaining rules must contain neither — their
// descriptions come from seedPagingRules' "description of rule NN" shape, which
// does not.
func seedScopedPagingRules(n, scoped int) *fakeGraphCaller {
	fc := seedPagingRules(n)
	nodes := fc.nodeMatchResults[graphKey{Type: "knowledge"}]
	for i := range scoped {
		kgtypes.SetValue(nodes[i], "scope", "commits")
	}
	return fc
}

// driveRules runs the rule arm over the raw payload and requires it claimed the
// call, returning the result for the caller to inspect.
func driveRules(t *testing.T, fc *fakeGraphCaller, payload string) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptQueryRules(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(payload),
	})
	require.True(t, handled, "the rules arm claims query(type:\"rule\")")
	return res
}

// TestInterceptQueryRules_PagesTheWholeCorpusAndSlicesAfterFilter is the
// reproduction suite for the rule browse's read shape. Each subtest names the
// transition SHIP GATE 47's Phase 2 makes, so a later reader can tell an
// intentional flip from a regression.
func TestInterceptQueryRules_PagesTheWholeCorpusAndSlicesAfterFilter(t *testing.T) {
	t.Run("plan_limit_is_the_drain_page_size_not_the_engine_default", func(t *testing.T) {
		// WAS 10 (the arm's own fixed payload compiled through
		// applyBrowseLimitOffset). IS paging.BrowsePageSize: the fetch asks for a
		// full page at a time, so the corpus size no longer decides what the client
		// gets to see.
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"type":"rule","format":"json"}`)
		require.False(t, res.IsError, "a bare rule browse succeeds: %s", toolResultText(res))
		require.NotEmpty(t, fc.execRequests, "the bare browse issues its fetch")
		assert.EqualValues(t, paging.BrowsePageSize, fc.execRequests[0].GetQuery().GetLimit(),
			"the drain asks for one page per request")

		// WAS a pre-read refusal naming limit. IS a served call whose limit is
		// applied to the FILTERED set rather than to the fetch — so the plan still
		// carries the page size while the ANSWER carries one row.
		capped := seedPagingRules(3)
		res = driveRules(t, capped, `{"type":"rule","limit":1,"format":"json"}`)
		require.False(t, res.IsError, "a caller limit is routed now, not refused: %s", toolResultText(res))
		require.NotEmpty(t, capped.execRequests, "and the fetch still runs")
		assert.EqualValues(t, paging.BrowsePageSize, capped.execRequests[0].GetQuery().GetLimit(),
			"the caller's limit does not shrink the fetch — it selects a page of the filtered set")
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Len(t, env.Results, 1, "the caller asked for one row and got one")
		assert.Equal(t, 3, env.Total, "against the true total of the matching set")
	})

	t.Run("caller_offset_is_applied_after_the_filter_not_on_the_plan", func(t *testing.T) {
		// The plan's Offset STAYS zero — a keyset drain never offsets, and the
		// server rejects a plan carrying both a cursor and an offset. What changed
		// is where the caller's offset lands: on the scope-filtered set, client-side.
		// Limit and offset are still checked separately, because they were dropped
		// by different mechanisms and a fix routing one without the other must fail
		// exactly one of these two subtests.
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"type":"rule","format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		require.NotEmpty(t, fc.execRequests)
		assert.EqualValues(t, 0, fc.execRequests[0].GetQuery().GetOffset(),
			"the drain plan carries no offset")

		// WAS a pre-read refusal naming offset. IS a served call that skips the
		// first two of three matching rules.
		skipped := seedPagingRules(3)
		res = driveRules(t, skipped, `{"type":"rule","offset":2,"format":"json"}`)
		require.False(t, res.IsError, "a caller offset is routed now, not refused: %s", toolResultText(res))
		require.NotEmpty(t, skipped.execRequests, "and the fetch still runs")
		assert.EqualValues(t, 0, skipped.execRequests[0].GetQuery().GetOffset(),
			"still not on the plan — the offset is applied to the filtered set")
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		require.Len(t, env.Results, 1, "offset 2 over three matching rules leaves one")
		assert.Equal(t, "00000000000000000000000000000003", env.Results[0].ID,
			"and it is the THIRD rule — the offset skipped, it did not truncate")
		assert.Equal(t, 3, env.Total, "with the total still counting all three")
	})

	t.Run("the_whole_corpus_is_drained_with_a_keyset_cursor", func(t *testing.T) {
		// THIS SUBTEST ASSERTS THE CAUSE, NOT THE EFFECT, and that is deliberate.
		// The fake ignores Limit and hands back all eleven rows, so neither the old
		// truncation nor the new completeness is observable in the row count here.
		// What IS observable is the SHAPE of the read: WAS one bounded request with
		// a nil cursor (no page two existed); IS a request whose cursor field is SET
		// to the empty string, which is what selects the keyset browse — an omitted
		// field pages in the backend's default order instead. Do not "fix" this into
		// a row-count check; a row count here is vacuous.
		fc := seedPagingRules(11)
		res := driveRules(t, fc, `{"type":"rule","format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		require.NotEmpty(t, fc.execRequests, "the drain issues at least its first page")
		plan := fc.execRequests[0].GetQuery()
		assert.EqualValues(t, paging.BrowsePageSize, plan.GetLimit(), "each page is bounded")
		require.NotNil(t, plan.AfterId, "the cursor is SET on page one — presence selects the keyset browse")
		assert.Empty(t, *plan.AfterId, "and page one's cursor value is the empty string")
		assert.True(t, plan.GetSkipTotal(), "no page pays for a COUNT the drain never reads")
	})

	t.Run("render_reports_the_matching_total_beside_the_page", func(t *testing.T) {
		// WAS: both numbers came from the same len(rules), so a caller could not
		// tell a complete answer from a truncated one. IS: the rows are the page
		// the render cap selected and the total is the matching count, so eleven
		// rules under the default cap report ten OF eleven.
		fc := seedPagingRules(11)
		res := driveRules(t, fc, `{"type":"rule","format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Equal(t, 11, env.Total, "the total counts every matching rule")
		assert.Len(t, env.Results, engine.BrowseDefaultLimit, "while the page stays at the render cap")

		text := seedPagingRules(11)
		res = driveRules(t, text, `{"type":"rule"}`)
		require.False(t, res.IsError, toolResultText(res))
		assert.True(t, strings.HasPrefix(toolResultText(res), "Rules (10 of 11):"),
			"the markdown header names both numbers: %s", toolResultText(res))
	})

	t.Run("complete_set_header_omits_the_of_clause", func(t *testing.T) {
		// The OTHER branch of renderRulesMarkdown, and the one no exact assertion
		// covered: a page that IS the whole matching set is already a complete
		// answer, so its header carries the single count and no " of ". Without
		// this, a render that printed " of <matched>" unconditionally would keep
		// every assertion in this file green — the three above all sit on the
		// subset branch, and the one Contains("Rules (") check elsewhere in the
		// package matches either shape.
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"type":"rule"}`)
		require.False(t, res.IsError, toolResultText(res))
		assert.True(t, strings.HasPrefix(toolResultText(res), "Rules (3):"),
			"a complete answer names one number: %s", toolResultText(res))
		// firstLine (help_recipes_parse_test.go) scopes this to the header, so the
		// assertion cannot be satisfied or defeated by body text further down.
		assert.NotContains(t, firstLine(toolResultText(res)), " of ",
			"the complete-set header must not carry the subset clause: %s", toolResultText(res))
	})
}

// TestInterceptQueryRules_RoutesStatusMetaAndTombstones asserts the three
// SERVER-SIDE filters reach the fetch plan. Every assertion reads the CAPTURED
// plan rather than the result: the fake ignores the selection entirely and hands
// back its whole seeded set, so a result-side check would pass whether or not
// the filter was routed.
func TestInterceptQueryRules_RoutesStatusMetaAndTombstones(t *testing.T) {
	fc := seedPagingRules(3)
	res := driveRules(t, fc,
		`{"type":"rule","status":"active","meta":{"scope":"*","team":"platform"},"include_tombstones":true}`)
	require.False(t, res.IsError, toolResultText(res))
	require.NotEmpty(t, fc.execRequests, "the filtered browse still issues its fetch")

	plan := fc.execRequests[0].GetQuery()
	assert.Contains(t, plan.GetSelection().GetStatuses(), "active", "status rides the selection")
	assert.True(t, plan.GetIncludeTombstones(), "include_tombstones rides the plan")

	// The OP of each predicate is asserted, not merely its key. A lowering that
	// mapped the "*" sentinel to equality-against-the-literal-asterisk would pass
	// a key-only check and then match nothing in production. Two entries with
	// DIFFERENT concrete values are required: one fixture entry cannot tell the
	// sentinel branch from the literal branch.
	byKey := map[string]*knowledgev1.MetadataPredicate{}
	for _, p := range plan.GetSelection().GetMetadataPredicates() {
		byKey[p.GetKey()] = p
	}
	require.Contains(t, byKey, "scope", "the sentinel meta entry reaches the plan")
	require.Contains(t, byKey, "team", "and so does the literal one")
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EXISTS, byKey["scope"].GetOp(),
		`meta {"scope":"*"} lowers to key-presence`)
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, byKey["team"].GetOp(),
		`meta {"team":"platform"} lowers to equality`)
	assert.Equal(t, "platform", byKey["team"].GetValue(), "carrying the caller's value")
}

// TestInterceptQueryRules_RejectsGraphSelector pins the pre-read rejection. The
// empty-request assertion is the load-bearing half: an arm that read first and
// errored afterwards would satisfy a message-only check while still hitting the
// graph.
func TestInterceptQueryRules_RejectsGraphSelector(t *testing.T) {
	fc := seedPagingRules(3)
	handled, res := InterceptQueryRules(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"type":"rule","graph":"practice","language":"go"}`),
	})
	require.True(t, handled, "the arm claims the call before refusing it")
	require.True(t, res.IsError, "a graph-bearing rule browse is refused")
	assert.Contains(t, toolResultText(res), "graph", "the message names the param")
	assert.Empty(t, fc.execRequests, "and no read precedes the refusal")

	// KNOWN POSITIVE for that zero: the same arm and the same fixture, without the
	// selector, must drive a real read — otherwise the emptiness above would also
	// be satisfied by a harness that wired nothing.
	ok := seedPagingRules(3)
	res = driveRules(t, ok, `{"type":"rule"}`)
	require.False(t, res.IsError, toolResultText(res))
	assert.NotEmpty(t, ok.execRequests, "the control issues the read the rejection suppressed")
}

// TestInterceptQueryRules_RenderReportsHonestTotal drives cases where the page
// and the total DIFFER, which is the only shape that can prove the two stopped
// being the same number.
func TestInterceptQueryRules_RenderReportsHonestTotal(t *testing.T) {
	t.Run("offset_and_limit_select_a_window_of_the_total", func(t *testing.T) {
		fc := seedPagingRules(11)
		res := driveRules(t, fc, `{"type":"rule","offset":8,"limit":5,"format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Equal(t, 11, env.Total, "the total counts every matching rule")
		assert.Len(t, env.Results, 3, "and the window is what is left after skipping eight")
	})

	t.Run("limit_alone_caps_the_page_not_the_total", func(t *testing.T) {
		fc := seedPagingRules(11)
		res := driveRules(t, fc, `{"type":"rule","limit":2,"format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Equal(t, 11, env.Total)
		assert.Len(t, env.Results, 2)
	})

	t.Run("markdown_header_names_both_numbers", func(t *testing.T) {
		fc := seedPagingRules(11)
		res := driveRules(t, fc, `{"type":"rule","limit":3}`)
		require.False(t, res.IsError, toolResultText(res))
		assert.True(t, strings.HasPrefix(toolResultText(res), "Rules (3 of 11):"),
			"the header reports the page beside the matching total: %s", toolResultText(res))
	})

	t.Run("the_total_counts_the_filtered_set_not_the_fetch", func(t *testing.T) {
		// The deepest form of the original defect: a fetch-level limit silently
		// changes which rules the scope filter ever sees, so the total must be
		// measured AFTER the client-side filter. Four of eleven carry the scope.
		fc := seedScopedPagingRules(11, 4)
		res := driveRules(t, fc, `{"type":"rule","scope":"commits","format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Equal(t, 4, env.Total, "the total is the count of scope-matching rules, not the eleven fetched")
		assert.Len(t, env.Results, 4, "and all four fit under the default cap")
	})
}

// TestInterceptQueryRules_BareBrowseStaysBounded is the guard on the bound this
// plan must NOT drop. The corpus is deliberately larger than the default: a
// corpus at or below the cap cannot distinguish a bounded render from an
// unbounded one.
func TestInterceptQueryRules_BareBrowseStaysBounded(t *testing.T) {
	const corpus = 25

	t.Run("no_limit_takes_the_default_cap", func(t *testing.T) {
		fc := seedPagingRules(corpus)
		res := driveRules(t, fc, `{"type":"rule","format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Len(t, env.Results, engine.BrowseDefaultLimit, "the page stays at the default cap")
		assert.Equal(t, corpus, env.Total, "while the total reports the whole corpus")

		text := seedPagingRules(corpus)
		res = driveRules(t, text, `{"type":"rule"}`)
		require.False(t, res.IsError, toolResultText(res))
		assert.True(t, strings.HasPrefix(toolResultText(res), "Rules (10 of 25):"),
			"the text path renders the same pair: %s", toolResultText(res))
	})

	t.Run("explicit_zero_limit_is_also_the_default_cap", func(t *testing.T) {
		// An implementation reading zero as "no cap" would reintroduce the whole
		// corpus into the caller's context window through an explicit param. The arm
		// mirrors applyBrowseLimitOffset, where any non-positive limit takes the
		// default.
		fc := seedPagingRules(corpus)
		res := driveRules(t, fc, `{"type":"rule","limit":0,"format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Len(t, env.Results, engine.BrowseDefaultLimit, "limit:0 is the default, not everything")
		assert.Equal(t, corpus, env.Total)
	})

	t.Run("an_explicit_limit_above_the_default_is_honored", func(t *testing.T) {
		// The control that proves the cap is a DEFAULT rather than a ceiling —
		// without it, an arm that always returned ten rows would pass both cases
		// above.
		fc := seedPagingRules(corpus)
		res := driveRules(t, fc, `{"type":"rule","limit":25,"format":"json"}`)
		require.False(t, res.IsError, toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env))
		assert.Len(t, env.Results, corpus, "a caller who asks for all 25 gets all 25")
		assert.Equal(t, corpus, env.Total)
	})
}
