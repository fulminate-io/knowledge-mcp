// SPDX-License-Identifier: Apache-2.0

package workingset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestWorkingSet_RemoveEvictsAndReadmitIsPossible pins Remove's four properties
// in one test, because they are one behaviour.
//
// THE RE-ADMISSION LEG IS THE POINT. It is the property a set-of-tombstones
// implementation would fail — one that remembered "this graph was evicted" and
// refused it thereafter. That is what keeps eviction a REPAIR rather than a
// second denial mechanism: the working set is what a durable not-found removes
// from, and a graph that later turns out to exist must be able to earn its way
// back in through the ordinary Admit path.
//
// The SECOND-removal-reports-false leg is what lets a caller log exactly once.
// The pipeline's eviction closure logs on the true return, so a Remove that
// reported true every time would log an eviction on every scan of a graph that
// was already gone.
func TestWorkingSet_RemoveEvictsAndReadmitIsPossible(t *testing.T) {
	s := New()

	// Removing a NON-member reports false. Without this the "true" below could
	// equally be a Remove that reports true unconditionally.
	assert.False(t, s.Remove(kgtypes.GraphCode, "never-admitted"),
		"removing a graph that was never a member removes nothing and must report false")

	require.True(t, s.Admit(kgtypes.GraphCode, "repo-a", "test"),
		"fixture: the graph must be admitted, or every assertion below is vacuous")
	require.True(t, s.Has(kgtypes.GraphCode, "repo-a"))

	// The FIRST removal of a member reports true and flips Has.
	assert.True(t, s.Remove(kgtypes.GraphCode, "repo-a"),
		"the first removal of a member reports that it WAS a member")
	assert.False(t, s.Has(kgtypes.GraphCode, "repo-a"),
		"a removed graph is no longer a member — this is what unregisters its collector")

	// A SECOND removal reports false, which is what lets a caller log once.
	assert.False(t, s.Remove(kgtypes.GraphCode, "repo-a"),
		"a second removal removes nothing and must report false, so an eviction logs exactly once")

	// RE-ADMISSION IS POSSIBLE. Eviction is a repair, not a tombstone.
	require.True(t, s.Admit(kgtypes.GraphCode, "repo-a", "test"),
		"an evicted graph must be admissible again — a later successful interaction re-admits it")
	assert.True(t, s.Has(kgtypes.GraphCode, "repo-a"),
		"the re-admitted graph is a member again")
}

// TestWorkingSet_RemoveNormalizesLikeAdmitAndHas pins that Remove goes through
// the SAME Normalize the other two do.
//
// A code branch name is cut at the first at-sign, so a member admitted under a
// bare repo must be removable by its branch-qualified spelling and vice versa. A
// Remove that skipped normalization would silently fail to evict exactly the
// graphs an overlay collect admitted.
func TestWorkingSet_RemoveNormalizesLikeAdmitAndHas(t *testing.T) {
	s := New()

	require.True(t, s.Admit(kgtypes.GraphCode, "repo-b@feature-branch", "test"))
	require.True(t, s.Has(kgtypes.GraphCode, "repo-b"),
		"fixture: Admit and Has already cut at the at-sign")

	assert.True(t, s.Remove(kgtypes.GraphCode, "repo-b@some-other-branch"),
		"Remove must cut at the at-sign exactly as Admit and Has do")
	assert.False(t, s.Has(kgtypes.GraphCode, "repo-b"),
		"the member is gone under its normalized ref")
}

// TestWorkingSet_RemoveIsNilSafe pins the nil receiver, like every other method
// on this type.
func TestWorkingSet_RemoveIsNilSafe(t *testing.T) {
	var s *Set
	assert.False(t, s.Remove(kgtypes.GraphCode, "repo-c"),
		"a nil *Set removes nothing and reports false rather than panicking")
}
