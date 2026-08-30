// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTypedUpdate_NoSummarySupplied_PreservesStoredSummary pins the rule that a
// typed update supplying no summary forwards none, leaving the stored one
// untouched.
//
// Both subtests are the reported defect: a status-only or metadata-only write on
// a node whose summary the caller never named used to re-derive that summary,
// and was refused when the derivation overflowed the rune cap — for a field the
// call never touched.
func TestTypedUpdate_NoSummarySupplied_PreservesStoredSummary(t *testing.T) {
	t.Run("status-only forwards no summary", func(t *testing.T) {
		node := nodeOf(t, "f1", "finding", "leak", "leak in handler",
			map[string]string{"evidence": "store.go:42"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "f1", Status: "completed",
		})
		require.True(t, handled, "a finding update is claimed by the typed router")

		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "completed", m.GetSetFields()["status"])
		// THE ABSENCE IS THE ASSERTION. Comparing the forwarded summary against the
		// stored one would also pass on an implementation that composed one and
		// happened to match; only the missing key proves nothing was forwarded.
		assert.NotContains(t, m.GetSetFields(), "summary",
			"a call supplying no summary must forward none at all")
	})

	t.Run("metadata-only forwards no summary but still lands the key", func(t *testing.T) {
		node := nodeOf(t, "f2", "finding", "leak", "leak in handler",
			map[string]string{"evidence": "store.go:42"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{
			Operation: "update", ID: "f2",
			Metadata: map[string]string{"evaluate_at": "phase-2-boundary"},
		})
		require.True(t, handled)

		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "summary")
		assert.Equal(t, "phase-2-boundary", m.GetSetMetadata()["evaluate_at"])
	})
}

// TestTypedUpdate_NoDerivation_DeriveSourceEditForwardsNoSummary is the catcher
// for a re-derivation surviving anywhere in the seam. It drives the ONE call
// shape the retired seam did re-derive on — a finding whose DESCRIPTION this
// call replaces, on a node carrying NO stored summary, so neither the
// no-derive-source gate nor the stored-summary preserve branch could have
// short-circuited it — and asserts no summary is forwarded even so.
//
// THE ABSENCE IS THE ASSERTION, for the reason the sibling test states: a
// comparison against the stored value would also pass on an implementation that
// re-derived and happened to match.
func TestTypedUpdate_NoDerivation_DeriveSourceEditForwardsNoSummary(t *testing.T) {
	node := nodeOf(t, "f3", "finding", "leak", "the suite is green",
		map[string]string{"evidence": "store.go:42"})
	require.Empty(t, node.GetSummary(),
		"the retired derive branch was only reachable with no stored summary; a fixture carrying one asserts nothing")

	fc, handled := runTypedUpdate(t, node, mutateArgs{
		Operation: "update", ID: "f3", Description: "the sweep remainder is zero",
	})
	require.True(t, handled)

	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "the sweep remainder is zero", m.GetSetFields()["description"],
		"the description edit itself must still land, or the summary absence proves nothing")
	assert.NotContains(t, m.GetSetFields(), "summary",
		"a description edit no longer re-derives: nothing composes a summary on the update path")
}
