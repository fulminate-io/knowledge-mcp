// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// emergingPatternBody returns the single emerging pattern node body the create
// batch persisted, failing loudly when none reached it — the assertions below
// are all about that body, so an absent one must red rather than skip.
func emergingPatternBody(t *testing.T, fc *fakeGraphCaller) *knowledgev1.NodeBody {
	t.Helper()
	require.Len(t, fc.execMutations, 1)
	for _, b := range fc.execMutations[0].GetNodeBodies() {
		if b.GetType() == string(kgtypes.NodePattern) {
			return b
		}
	}
	t.Fatal("no emerging pattern body reached the persisted batch")
	return nil
}

// TestProposedPattern_SummaryRequired pins the author-supplied summary on
// proposed_patterns across BOTH tools that accept them. The schema is shared
// (proposedPatternItems), but the THREADING is per-interceptor, so a fix applied
// to only one would pass a single-tool test.
//
// Each tool is asserted in both directions: an entry with no summary is refused
// naming proposed_patterns[0].summary, and a supplied one reaches the emerging
// pattern node verbatim. The supply arm is the control that stops the refuse arm
// from being satisfied by a handler that refuses everything.
func TestProposedPattern_SummaryRequired(t *testing.T) {
	const authored = "a bounded worker pool that drains its queue before the deadline"

	t.Run("create_plan", func(t *testing.T) {
		t.Run("no summary is refused", func(t *testing.T) {
			fc := seededPlanFake()
			res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s",`+minimalPlanPhases+`,
				"proposed_patterns":[{"name":"bounded-worker-pool","sketch":"type Pool interface{}"}]}`)
			require.True(t, res.IsError, "a proposed pattern with no summary must be refused, never derived")
			assert.Contains(t, toolResultText(res), "proposed_patterns[0].summary is required and must be non-empty")
			assert.Empty(t, fc.execMutations, "the refusal must precede any write")
		})

		t.Run("a supplied summary reaches the pattern node", func(t *testing.T) {
			fc := seededPlanFake()
			res := runCreatePlan(t, fc, `{"name":"p","goal":"g","summary":"s",`+minimalPlanPhases+`,
				"proposed_patterns":[{"name":"bounded-worker-pool","summary":"`+authored+`","sketch":"type Pool interface{}"}]}`)
			require.False(t, res.IsError, "an authored proposed-pattern summary must be accepted: %s", toolResultText(res))
			body := emergingPatternBody(t, fc)
			assert.Equal(t, authored, body.GetSummary(), "the author's summary must reach the pattern node untouched")
			// "Proposed pattern: " was the retired derivation's prefix.
			assert.NotContains(t, body.GetSummary(), "Proposed pattern: ")
		})
	})

	t.Run("create_ticket", func(t *testing.T) {
		t.Run("no summary is refused", func(t *testing.T) {
			fc := seededTicketFake()
			res := runCreateTicketLocal(t, fc, `"proposed_patterns":[{"name":"bounded-worker-pool","sketch":"type Pool interface{}"}]`)
			require.True(t, res.IsError, "a proposed pattern with no summary must be refused, never derived")
			assert.Contains(t, toolResultText(res), "proposed_patterns[0].summary is required and must be non-empty")
			assert.Empty(t, fc.execMutations, "the refusal must precede any write")
		})

		t.Run("a supplied summary reaches the pattern node", func(t *testing.T) {
			fc := seededTicketFake()
			res := runCreateTicketLocal(t, fc, `"proposed_patterns":[{"name":"bounded-worker-pool","summary":"`+authored+`","sketch":"type Pool interface{}"}]`)
			require.False(t, res.IsError, "an authored proposed-pattern summary must be accepted: %s", toolResultText(res))
			body := emergingPatternBody(t, fc)
			assert.Equal(t, authored, body.GetSummary(), "the author's summary must reach the pattern node untouched")
			assert.NotContains(t, body.GetSummary(), "Proposed pattern: ")
		})
	})
}
