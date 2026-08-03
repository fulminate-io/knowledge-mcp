// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// The plan_tree truncation-honesty pair. plan_tree bypasses engine.Render (it
// returns jsonResult / TextResult directly), so Render's append-a-notice
// wrapper never fires for it: without the propagation a subtree clamped by a
// server ceiling renders as a complete-looking tree with missing branches and
// no marker.
//
// Both halves are load-bearing and must stay together. The truncated=true case
// is the fails-when-absent test; the truncated=false case is its known-negative
// control, without which "the notice block is present" is indistinguishable
// from "a notice is appended unconditionally". Split into its own file because
// intercept_query_plan_tree_test.go sits near the repo's hard 500-line gate.

// TestInterceptQueryPlanTree_TruncatedResponse_AppendsNoticeBlock drives the
// intercept against a fixture whose traversal answers Truncated=true and
// asserts the notice arrives as a SECOND content block — never concatenated
// into the first — in both render formats.
func TestInterceptQueryPlanTree_TruncatedResponse_AppendsNoticeBlock(t *testing.T) {
	seed := func(truncated bool) *parityGraphFixture {
		f := newParityFixture()
		seedPlanTreeFixture(f)
		f.truncated = truncated
		return f
	}
	planID := "00000000000000000000000000000001"

	t.Run("text_gains_exactly_one_block", func(t *testing.T) {
		complete := seed(false)
		clamped := seed(true)

		args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": planID})
		_, plain := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: complete.gc()},
			kgtools.CallToolParams{Name: "query", Arguments: args})
		require.False(t, plain.IsError)

		_, got := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: clamped.gc()},
			kgtools.CallToolParams{Name: "query", Arguments: args})
		require.False(t, got.IsError)

		require.Len(t, got.Content, len(plain.Content)+1,
			"a truncated plan_tree gains exactly ONE block")
		assert.Equal(t, plain.Content[0].Text, got.Content[0].Text,
			"the tree block itself is untouched — the notice is appended, not concatenated")

		notice := got.Content[len(got.Content)-1].Text
		assert.Contains(t, notice, "may be incomplete",
			"the notice says the result may be incomplete")
		assert.Contains(t, notice, "limit",
			"the notice names the `limit` parameter so a reader maps the advice onto it")
		assert.Contains(t, notice, "8",
			"the notice names the row count (2 phases + 6 steps)")
	})

	t.Run("json_payload_block_stays_valid_json", func(t *testing.T) {
		// The gate on appending as a SEPARATE block rather than concatenating:
		// the payload block must survive the notice intact. If this ever fails,
		// the fix is to skip the notice for format=json — never to weaken this
		// assertion.
		clamped := seed(true)
		args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})

		_, got := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: clamped.gc()},
			kgtools.CallToolParams{Name: "query", Arguments: args})
		require.False(t, got.IsError)
		require.Len(t, got.Content, 2, "json payload block + notice block")

		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(got.Content[0].Text), &payload),
			"the first block must still parse as JSON with the notice appended")
		assert.Equal(t, planID, payload["id"], "the parsed payload is the plan-tree root row")
		assert.Contains(t, got.Content[1].Text, "limit", "the notice rides in its own trailing block")
	})
}

// TestInterceptQueryPlanTree_CompleteResponse_AppendsNothing is the
// known-negative control for the pair above: it is the only thing keeping the
// notice off every COMPLETE tree. Without it, an unconditional append would
// satisfy the truncated=true assertions perfectly.
func TestInterceptQueryPlanTree_CompleteResponse_AppendsNothing(t *testing.T) {
	f := newParityFixture()
	seedPlanTreeFixture(f)
	// f.truncated stays false — the traversal answers a complete subtree.
	deps := &parityDeps{gc: f.gc()}
	planID := "00000000000000000000000000000001"

	for _, format := range []string{"", "json"} {
		payload := map[string]any{"mode": "plan_tree", "id": planID}
		if format != "" {
			payload["format"] = format
		}
		_, res := InterceptQueryPlanTree(opCtx(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: mustMarshal(t, payload)})
		require.False(t, res.IsError)
		require.Len(t, res.Content, 1, "a complete tree renders as a single block (format=%q)", format)
		assert.NotContains(t, res.Content[0].Text, "may be incomplete",
			"a complete tree carries no truncation notice (format=%q)", format)
	}
}
