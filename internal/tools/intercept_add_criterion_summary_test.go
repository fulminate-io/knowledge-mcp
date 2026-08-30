// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptAddCriterion_ExplicitSummary pins the author-supplied summary
// contract on mutate(create, type=criterion): required non-empty, stored
// verbatim, clamped-not-rejected over the cap. The name stays derived, so an
// explicit name is still refused — that subtest is what catches a registry edit
// that opened `name` along with `summary`.
//
// The clamp here is the ONLY enforcement on this path: the arm writes through
// mutate(upsert), and upsert is on the server's create-validation bypass
// allowlist, so the server's non-empty-summary rule never runs for a criterion
// created this way.
func TestInterceptAddCriterion_ExplicitSummary(t *testing.T) {
	t.Run("absent summary is refused", func(t *testing.T) {
		gc := seededStepGc()
		args := mustMarshal(t, map[string]any{
			"operation": "create", "type": "criterion", "step_id": testStepID,
			"description": "Test that the thing works",
		})
		handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: args,
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a criterion with no summary must be refused, never derived")
		assert.Contains(t, extractText(res), "criterion.summary is required and must be non-empty")
		require.Len(t, gc.calls, 1, "only the step lookup fired — no upsert, no links")
		assert.Equal(t, "query", gc.calls[0].tool)
	})

	t.Run("explicit summary is stored verbatim", func(t *testing.T) {
		gc := seededStepGc()
		const authored = "the widget reconciler drains its queue before the deadline"
		args := mustMarshal(t, map[string]any{
			"operation": "create", "type": "criterion", "step_id": testStepID,
			"description": "Test that the thing works", "command": "go test ./...",
			"summary": authored,
		})
		handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: args,
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored summary must be accepted: %v", res.Content)

		body := gc.lastUpsertBody
		require.NotNil(t, body, "an upsert must have been issued")
		assert.Equal(t, authored, body.GetSummary(), "the author's summary must reach the node untouched")
		// "criterion: " is the retired derivation's signature — its absence is what
		// tells a stored author summary from a re-derived one.
		assert.NotContains(t, body.GetSummary(), "criterion: ")
	})

	t.Run("over-cap summary clamps and warns", func(t *testing.T) {
		gc := seededStepGc()
		args := mustMarshal(t, map[string]any{
			"operation": "create", "type": "criterion", "step_id": testStepID,
			"description": "Test that the thing works",
			"summary":     strings.Repeat("word ", 120), // 600 runes
		})
		handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: args,
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an over-cap summary CLAMPS — it is never a hard reject")

		body := gc.lastUpsertBody
		require.NotNil(t, body, "the clamped create must still have been upserted")
		assert.LessOrEqual(t, utf8.RuneCountInString(body.GetSummary()), 500)
		assert.Contains(t, extractText(res), "clamped", "the caller must be told detail was dropped")
	})

	t.Run("explicit name is still refused", func(t *testing.T) {
		gc := seededStepGc()
		args := mustMarshal(t, map[string]any{
			"operation": "create", "type": "criterion", "step_id": testStepID,
			"description": "Test that the thing works", "summary": "the thing works",
			"name": "caller supplied",
		})
		handled, res := InterceptAddCriterion(opCtx(), &logE2EDeps{gc: gc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: args,
		})
		require.True(t, handled)
		require.True(t, res.IsError, "name stays derived from the description's first line")
		assert.Contains(t, extractText(res), "derived from its description")
		assert.Empty(t, gc.calls, "the reject must precede the step-lookup RPC")
	})
}
