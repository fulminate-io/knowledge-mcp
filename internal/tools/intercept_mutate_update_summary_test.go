// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// nodeWithSummary builds a typed node that ALREADY CARRIES A STORED SUMMARY.
// It exists because nodeOf leaves Summary empty, so every fixture built with
// nodeOf exercises only the no-stored-summary half of the seam — the half where
// "kept" and "rewritten with the same text" cannot be told apart. An assertion
// about preservation built on nodeOf would be decoration.
func nodeWithSummary(t *testing.T, id, typ, symbolName, description, summary string, metadata map[string]string) *knowledgev1.Node {
	t.Helper()
	n := nodeOf(t, id, typ, symbolName, description, metadata)
	n.Summary = summary
	return n
}

// TestTypedUpdate_StoredSummaryIsNeverWrittenOver pins the destructive half of
// the retired three-way divergence: a finding/rule update that supplies no
// summary must leave the stored, caller-authored one alone, whatever else the
// call changes.
//
// Writing over it was data destruction with no recovery — the composed text was
// plausible prose rather than an obvious corruption, so a caller who had not
// hydrated the node first lost the claim with no trace. mutate(create) requires
// a caller-authored summary for both types (validate.ClampSummary rejects an
// empty one), so a stored finding/rule summary is caller-authored by
// construction and the overwrite could only ever be destroying authored text.
func TestTypedUpdate_StoredSummaryIsNeverWrittenOver(t *testing.T) {
	t.Run("finding description edit preserves the stored summary", func(t *testing.T) {
		const stored = "the reconcile drops rows the sweep should have collected"
		node := nodeWithSummary(t, "f1", "finding", "orphan rows", "old desc", stored, nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "f1", Description: "a new description body",
		})
		require.True(t, handled)

		m := lastUpdatePlan(t, fc)
		// THE ABSENCE IS THE ASSERTION. Comparing the forwarded summary against the
		// stored one would also pass on an implementation that composed one and
		// happened to match; only the missing key proves nothing was written over it.
		assert.NotContains(t, m.GetSetFields(), "summary",
			"a stored summary the caller did not ask to replace must not be written at all")
		// The description the caller DID ask for still lands — preservation is not a
		// refusal.
		assert.Equal(t, "a new description body", m.GetSetFields()["description"])
	})

	t.Run("finding evidence edit preserves the stored summary", func(t *testing.T) {
		// Evidence was the OTHER finding derive source. Covering only description
		// would leave a fix that special-cased one field looking correct.
		node := nodeWithSummary(t, "f2", "finding", "leak", "leak in handler",
			"the handler leaks a connection per request", map[string]string{"evidence": "store.go:42"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "f2", Evidence: "store.go:99",
		})
		require.True(t, handled)

		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "summary")
		assert.Equal(t, "store.go:99", m.GetSetMetadata()["evidence"])
	})

	t.Run("rule scope edit preserves the stored summary", func(t *testing.T) {
		node := nodeWithSummary(t, "r1", "rule", "no naked goroutines", "no naked goroutines",
			"every goroutine is owned by a supervisor that can cancel it", map[string]string{"scope": "*.go"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "r1", Scope: "cmd/",
		})
		require.True(t, handled)

		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "summary")
		assert.Equal(t, "cmd/", m.GetSetMetadata()["scope"])
	})

	t.Run("the receipt says no summary was supplied and how to replace it", func(t *testing.T) {
		node := nodeWithSummary(t, "f3", "finding", "orphan rows", "old desc",
			"the reconcile drops rows the sweep should have collected", nil)
		body, _ := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "f3", Description: "a new description body",
		})
		// An untouched summary reported as nothing at all is what let a destructive
		// write read as a routine success. The disposition literal is what tells a
		// caller their summary survived and how to replace it deliberately.
		assert.Contains(t, body, "Summary: "+summaryDispositionUnchanged)
		assert.Contains(t, body, "pass an explicit summary to replace it")
	})

	t.Run("an explicit caller summary still replaces a stored one", func(t *testing.T) {
		// The known-positive: preservation must not become a blanket refusal to
		// write summaries. Without this, an implementation that never forwarded a
		// summary for findings at all would pass every subtest above.
		node := nodeWithSummary(t, "f4", "finding", "orphan rows", "old desc",
			"the reconcile drops rows the sweep should have collected", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "f4",
			Description: "a new description body", Summary: "a deliberate replacement summary",
		})
		require.True(t, handled)

		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "a deliberate replacement summary", m.GetSetFields()["summary"])
	})
}

// TestTypedUpdate_CriterionSummaryPreservedWhenAbsent is the gate on the LAST
// type to join the seam's cross-type rules. A criterion's summary is
// author-supplied on create, so on update it behaves like every other type: a
// call supplying none forwards nothing and the STORED summary survives, and an
// explicit one is applied verbatim. Its NAME is still derived from the
// description's first line — that is the name, not the summary.
func TestTypedUpdate_CriterionSummaryPreservedWhenAbsent(t *testing.T) {
	t.Run("a command edit with no summary forwards no summary at all", func(t *testing.T) {
		node := nodeWithSummary(t, "c1", "criterion", "the suite is green", "the suite is green",
			"an authored criterion summary", map[string]string{"type": "automated"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "c1", Command: "go test ./...",
		})
		require.True(t, handled, "a criterion update is still claimed by the typed router")

		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "go test ./...", m.GetSetMetadata()["command"],
			"the per-type command routing survives — only the summary composition is gone")
		// THE ABSENCE IS THE ASSERTION. Comparing the forwarded summary against the
		// stored one would also pass on an implementation that composed one and
		// happened to match; only the missing key proves the stored value is kept.
		assert.NotContains(t, m.GetSetFields(), "summary",
			"nothing composes a criterion summary, so nothing is forwarded and the stored one stands")
	})

	t.Run("an explicit summary is forwarded verbatim", func(t *testing.T) {
		node := nodeWithSummary(t, "c2", "criterion", "the suite is green", "the suite is green",
			"an authored criterion summary", map[string]string{"type": "automated"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "c2", Command: "go test ./...",
			Summary: "the replacement summary the caller authored",
		})
		require.True(t, handled)
		assert.Equal(t, "the replacement summary the caller authored",
			lastUpdatePlan(t, fc).GetSetFields()["summary"])
	})
}
