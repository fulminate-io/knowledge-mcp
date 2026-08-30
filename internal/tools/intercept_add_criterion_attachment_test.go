// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_add_criterion_attachment_test.go holds two criterion-create
// contracts: the ATTACHMENT contract (a failed verifies/contains link fails
// the call rather than reporting "Criterion added" over an unwired node) and the
// NAME derivation (clamped to the description's first line). Split from
// intercept_add_criterion_test.go, which the repo's 500-line file gate requires
// to stay under that limit; both files share that file's fakes
// (scriptedCriterionGc, seededStepGc, logE2EDeps) and its testStepID fixture.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptAddCriterion_MultiLineDescription_NameClampedToFirstLine pins the
// criterion-create half of the name derivation: the upserted node's `name` is the
// description's FIRST LINE, while `description` keeps every line.
//
// It drives the whole intercept rather than projects.DeriveCriterionName
// directly, because the value under test is what crosses the upsert RPC — a
// helper that clamps correctly but is not wired into upsertCriterionNode passes
// the projects unit test and fails here.
func TestInterceptAddCriterion_MultiLineDescription_NameClampedToFirstLine(t *testing.T) {
	const desc = "the pair gate rejects a lone verifies edge\n\n" +
		"Run: go test ./cmd/knowledge/internal/tools -run TestGuardCreateBatchCriterionPair\n" +
		"Why: a verifies-only criterion never renders under its step."

	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}
	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": desc,
		"summary":     "the multi-line description's first line",
	})
	handled, res := InterceptAddCriterion(opCtx(), deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	require.Len(t, gc.calls, 4)
	assert.Equal(t, "upsert", gc.calls[1].args["operation"])
	// The recorded arg map is a narrow call-shape summary and carries no `name`;
	// the retained NodeBody is the value that actually crossed the seam.
	require.NotNil(t, gc.lastUpsertBody)
	assert.Equal(t, "the pair gate rejects a lone verifies edge", gc.lastUpsertBody.GetName(),
		"the criterion's name is the description's first line, not the whole block")
	assert.Equal(t, desc, gc.lastUpsertBody.GetDescription(),
		"the description crosses the wire whole — only the NAME is clamped")
}

// Link RPC failure → the CALL FAILS, naming the unwired criterion and the edges
// to re-issue.
//
// This replaces an assertion that a link failure still returned "Criterion
// added". That tolerance mirrored the server's slog.Warn-and-continue, and it
// produced the exact failure mode the pair convention exists for: the node exists,
// the caller is told it was added, and with the `contains` edge missing it is
// invisible to plan_tree — with the only record of the failure in the daemon
// log, which the caller never sees. The node is NOT rolled back (four separate
// RPCs, no enclosing transaction), so the contract asserted here is that the
// error names the orphan and its repair.
//
// EACH DIRECTION IS DRIVEN SEPARATELY, and the both-fail case with it: a check
// written against only one relationship would pass while the other link's
// failure stayed silent, which is half the defect surviving the fix.
func TestInterceptAddCriterion_LinkFailure_IsAnError(t *testing.T) {
	cases := []struct {
		name       string
		linkErr    map[string]error
		wantEdges  []string
		wantCount  string
		wantNoEdge string
	}{
		{
			name:       "verifies link fails",
			linkErr:    map[string]error{"verifies": errors.New("transient")},
			wantEdges:  []string{"criterion--verifies-->step"},
			wantCount:  "1 of its 2 attachment edges failed",
			wantNoEdge: "step--contains-->criterion",
		},
		{
			name:       "contains link fails",
			linkErr:    map[string]error{"contains": errors.New("transient")},
			wantEdges:  []string{"step--contains-->criterion"},
			wantCount:  "1 of its 2 attachment edges failed",
			wantNoEdge: "criterion--verifies-->step",
		},
		{
			name: "both links fail",
			linkErr: map[string]error{
				"verifies": errors.New("transient"),
				"contains": errors.New("transient"),
			},
			wantEdges: []string{"criterion--verifies-->step", "step--contains-->criterion"},
			wantCount: "2 of its 2 attachment edges failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc := seededStepGc()
			gc.linkErr = tc.linkErr
			deps := &logE2EDeps{gc: gc}

			args := mustMarshal(t, map[string]any{
				"operation":   "create",
				"type":        "criterion",
				"step_id":     testStepID,
				"description": "desc",
				"summary":     "the desc",
			})
			handled, res := InterceptAddCriterion(opCtx(), deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
			require.True(t, handled)
			require.True(t, res.IsError, "a failed attachment edge fails the call — the criterion is unwired")

			msg := extractText(res)
			assert.NotContains(t, msg, "Criterion added",
				"the success line must not appear for a criterion that was not attached")
			assert.Contains(t, msg, "was created but is NOT attached to step "+testStepID,
				"the error states the residual: the node exists, the attachment does not")
			assert.Contains(t, msg, tc.wantCount)
			for _, edge := range tc.wantEdges {
				assert.Containsf(t, msg, edge, "the error names the failed edge %s", edge)
			}
			if tc.wantNoEdge != "" {
				assert.NotContains(t, msg, tc.wantNoEdge,
					"the edge that SUCCEEDED is not reported as failed")
			}
			assert.Contains(t, msg, "Re-issue the failed edge(s) with mutate(link)",
				"the error names the repair, not just the fault")

			// BOTH links are still attempted before reporting, so the error can name
			// the full residual state rather than only the first failure.
			require.Len(t, gc.calls, 4, "all 4 RPCs fire; the report comes after both link attempts")
		})
	}
}

// KNOWN-POSITIVE CONTROL for the case above: with both links succeeding the same
// call reports success. Without it, an implementation that errored on EVERY
// criterion create would pass every sub-test above.
func TestInterceptAddCriterion_BothLinksSucceed_NoError(t *testing.T) {
	gc := seededStepGc()
	deps := &logE2EDeps{gc: gc}

	args := mustMarshal(t, map[string]any{
		"operation":   "create",
		"type":        "criterion",
		"step_id":     testStepID,
		"description": "desc",
		"summary":     "the desc",
	})
	handled, res := InterceptAddCriterion(opCtx(), deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "both attachment edges landed — this is the success path")
	assert.Contains(t, extractText(res), "Criterion added: desc → ID:")
	require.Len(t, gc.calls, 4)
}
