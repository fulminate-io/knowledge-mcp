// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_rules_graph_alias_test.go pins the REDUNDANT-ALIAS half of the
// rule browse's `graph` contract: the arm serves a rule browse that names the
// graph family it already pins, and refuses one that names any other.
//
// The sibling file intercept_query_rules_paging_test.go owns the rejection half
// (TestInterceptQueryRules_RejectsGraphSelector, which probes a FOREIGN graph);
// the two together are the whole contract, and neither is meaningful alone. A
// blanket accept would pass the acceptance cases here while dropping a
// graph:"code" rule browse — a real caller error — on the floor, so the foreign
// case below is a required control rather than a courtesy.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptQueryRules_AcceptsRedundantKnowledgeGraph drives the four shapes
// that decide the convention.
func TestInterceptQueryRules_AcceptsRedundantKnowledgeGraph(t *testing.T) {
	t.Run("explicit_knowledge_graph_is_redundant_not_wrong", func(t *testing.T) {
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"type":"rule","graph":"knowledge"}`)
		require.False(t, res.IsError,
			"graph:\"knowledge\" names the family the arm already pins — redundant, not a caller error: %s",
			toolResultText(res))
		assert.NotEmpty(t, fc.execRequests, "and the browse still issues its read")
	})

	t.Run("the_graph_explorer_browse_shape_is_served", func(t *testing.T) {
		// The shape the agent graph explorer sends: its graph-dropdown value rides
		// EVERY query, so the rule type chip arrives carrying graph:"knowledge"
		// beside the json render params. This is the reported break, driven as the
		// acceptance case rather than paraphrased.
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"graph":"knowledge","type":"rule","limit":50,"format":"json"}`)
		require.False(t, res.IsError, "the explorer's rule browse must be served: %s", toolResultText(res))
		var env browseJSONEnvelope
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &env),
			"the json caller gets the browse envelope, not a rejection body")
		assert.Equal(t, 3, env.Total, "the total counts the matching rules")
		assert.Len(t, env.Results, 3, "and the page carries them")
	})

	t.Run("an_omitted_graph_is_unaffected", func(t *testing.T) {
		// The empty string never reaches the rejection loop at all — accounting
		// counts a key as supplied only when its value is non-empty — so this case
		// pins that the alias set did not disturb the shape that always worked.
		fc := seedPagingRules(3)
		res := driveRules(t, fc, `{"type":"rule","graph":""}`)
		require.False(t, res.IsError, "an empty graph is not a supplied param: %s", toolResultText(res))
		assert.NotEmpty(t, fc.execRequests, "the browse reads as it always did")
	})

	t.Run("a_foreign_graph_still_rejects_by_name_with_no_read", func(t *testing.T) {
		// THE CONTROL. graph:"code" is a caller genuinely asking for rules from a
		// family that has none, and the alias set must not have widened into a
		// blanket accept. Its zero-read assertion carries the acceptance cases above
		// as its own known positive: the same arm, same fixture, reads on every one
		// of them.
		fc := seedPagingRules(3)
		handled, res := InterceptQueryRules(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"type":"rule","graph":"code","repo":"knowledge"}`),
		})
		require.True(t, handled, "the arm claims the call before refusing it")
		require.True(t, res.IsError, "a rule browse naming another graph family is still a caller error")
		body := toolResultText(res)
		assert.Contains(t, body, "graph", "the message names the param")
		assert.Contains(t, body, "knowledge-graph nodes", "with the shared contract wording")
		assert.Empty(t, fc.execRequests, "and no read precedes the refusal")
	})
}
