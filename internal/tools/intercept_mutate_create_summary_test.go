// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestMutateCreate_EmptySummaryRefused_Finding_Rule_Research is a
// CHARACTERIZATION GUARD, green before and after the fallback deletion — it is
// NOT red-first and must not be described as such.
//
// Its job is to make explicit the gate the finding/rule/research create arms
// now depend on: validate.ClampSummary hard-rejects an empty summary before any
// node is built, so no arm needs a summary of its own to fall back on. Nothing
// asserted that for these three arms before (the rejection message was covered
// only for criterion), so a future edit to ClampSummary could have silently
// re-opened them.
func TestMutateCreate_EmptySummaryRefused_Finding_Rule_Research(t *testing.T) {
	for _, tc := range []struct {
		nodeType string
		args     string
	}{
		{"finding", `{"operation":"create","type":"finding","name":"fixture-finding","description":"desc"}`},
		{"rule", `{"operation":"create","type":"rule","name":"fixture-rule","description":"desc","scope":"*.go"}`},
		{"research", `{"operation":"create","type":"research","name":"Why slow?","content":"context"}`},
	} {
		t.Run(tc.nodeType, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: json.RawMessage(tc.args),
			})
			require.True(t, handled)
			require.True(t, res.IsError, "a %s created with no summary must be refused, never filled in", tc.nodeType)
			assert.Contains(t, toolResultText(res), "is required and must be non-empty")
			assert.Empty(t, fc.calls, "the refusal must precede every RPC")
		})
	}
}

// TestFindingReference_SummaryRequired pins the PER-KIND summary rule on
// mutate(create, type=finding) references. url and file entries CREATE a
// reference node and so require an author summary; a node_id entry creates no
// node and requires none.
//
// The node_id subtest is the near-miss that separates the per-kind rule from a
// blanket requirement: a blanket implementation passes the first three subtests
// and fails the fourth.
func TestFindingReference_SummaryRequired(t *testing.T) {
	findingArgs := func(refs string) json.RawMessage {
		return json.RawMessage(`{"operation":"create","type":"finding","name":"fixture-finding",
			"summary":"the finding's own summary","description":"desc","references":[` + refs + `]}`)
	}

	t.Run("a url reference with no summary is refused", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: findingArgs(`{"url":"https://example.invalid/a","title":"A"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "a node-creating reference with no summary must be refused")
		assert.Contains(t, toolResultText(res), "references[0].summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations, "the refusal must precede any write")
	})

	t.Run("a file reference with no summary is refused", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: findingArgs(`{"file":"store.go","title":"A"}`),
		})
		require.True(t, handled)
		require.True(t, res.IsError, "the file kind creates a node too, so it carries the same rule")
		assert.Contains(t, toolResultText(res), "references[0].summary is required and must be non-empty")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("a supplied summary reaches the reference node verbatim", func(t *testing.T) {
		const authored = "the upstream advisory describing the sweep's re-read"
		fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1", "ref-1"}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: findingArgs(
				`{"url":"https://example.invalid/a","title":"A","summary":"` + authored + `"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an authored reference summary must be accepted: %s", toolResultText(res))

		require.Len(t, fc.execMutations, 1)
		var refBody *knowledgev1.NodeBody
		for _, b := range fc.execMutations[0].GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeReference) {
				refBody = b
			}
		}
		require.NotNil(t, refBody, "a url reference must create a reference node")
		assert.Equal(t, authored, refBody.GetSummary(), "the author's summary must reach the node untouched")
		// " — " joined the retired composition's title and target.
		assert.NotContains(t, refBody.GetSummary(), " — ")
	})

	t.Run("a node_id reference with no summary still succeeds", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1"}}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: findingArgs(`{"node_id":"n-1","title":"A"}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError,
			"a node_id reference creates no node, so it needs no summary: %s", toolResultText(res))

		require.Len(t, fc.execMutations, 1)
		for _, b := range fc.execMutations[0].GetNodeBodies() {
			assert.NotEqual(t, string(kgtypes.NodeReference), b.GetType(),
				"a node_id reference must create no reference node — only an edge")
		}
	})
}
