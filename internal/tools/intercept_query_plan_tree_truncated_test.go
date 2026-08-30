// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
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

// TestPlanTreeJSON_TruncatedField pins the `truncated` key on the plan_tree JSON
// envelope — the sixth ceiling-engageable query read, and the one whose absence
// would have made query_schema.go's own new sentence false on the day it shipped.
//
// THE KEY IS ON THE ENVELOPE ROOT AND NOWHERE ELSE. buildPlanTreeJSON is
// recursive and returns the root row AS the whole payload, so putting the key
// inside it would stamp all 9 rows of this fixture. Truncation is a property of
// the READ, not of a node — the row-count assertion below is what holds that
// placement in place.
//
// TWO POLARITIES. The FALSE leg matters more than usual here because the SAME
// bool already drives the prose notice block, so a constant-wired key would still
// look right wherever that block appears.
func TestPlanTreeJSON_TruncatedField(t *testing.T) {
	planID := "00000000000000000000000000000001"

	payloadFor := func(t *testing.T, truncated bool) map[string]any {
		t.Helper()
		f := newParityFixture()
		seedPlanTreeFixture(f)
		f.truncated = truncated
		args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
		_, res := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: f.gc()},
			kgtools.CallToolParams{Name: "query", Arguments: args})
		require.False(t, res.IsError)
		require.NotEmpty(t, res.Content)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
			"the payload block must stay parseable with the key added")
		return payload
	}

	t.Run("clamped traversal renders the root key true", func(t *testing.T) {
		payload := payloadFor(t, true)
		got, ok := payload["truncated"]
		require.True(t, ok, "the plan_tree envelope root carries no truncated key")
		assert.Equal(t, true, got)
	})

	t.Run("whole traversal renders the root key false", func(t *testing.T) {
		payload := payloadFor(t, false)
		got, ok := payload["truncated"]
		require.True(t, ok,
			"the key is UNCONDITIONAL: an absent key is indistinguishable from an old binary")
		assert.Equal(t, false, got)
	})

	t.Run("exactly one row carries the key — the root", func(t *testing.T) {
		// THE PLACEMENT GATE. The fixture is 9 rows (root + 2 phases + 6 steps); if
		// the key ever moves inside buildPlanTreeJSON this counts 9 and fails, which
		// is the arrangement with the larger blast radius on every large tree.
		f := newParityFixture()
		seedPlanTreeFixture(f)
		f.truncated = true
		args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
		_, res := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: f.gc()},
			kgtools.CallToolParams{Name: "query", Arguments: args})
		require.False(t, res.IsError)
		assert.Equal(t, 1, strings.Count(res.Content[0].Text, `"truncated"`),
			"the key belongs on the envelope ROOT only — truncation is a property of the read, "+
				"not of a node, and a leaf asserting truncated:false says nothing about anything")
	})
}
