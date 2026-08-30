// SPDX-License-Identifier: Apache-2.0

package workingset

// set_singleton_test.go pins the ""→"default" collapse for a family that carries
// NO instance field at all.
//
// THE DISTINCTION IS THE WHOLE POINT. For code / cloud / practice an empty name
// means the caller named no repo, account or language — a catalog enumeration,
// which must admit nothing. For a family whose selector policy declares it has no
// instance field, an empty name is not an absent selector: it IS the one
// instance. Treating the two the same is what left the checks graph permanently
// outside the working set, so no collector was ever registered for it and its
// nodes stayed unembedded through every drain.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestNormalize_SingletonEmptyNameIsItsDefaultInstance asserts the collapse for
// checks, alongside the knowledge precedent it follows.
//
// THE CODE ROW IS THE CONTROL, and it is what stops this from being a change that
// simply makes every empty name admit: a code target with no repo is a catalog
// enumeration and must still be refused. Without it, deleting the whole
// empty-name guard would satisfy every other row here.
func TestNormalize_SingletonEmptyNameIsItsDefaultInstance(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		gt       kgtypes.GraphType
		in       string
		wantOK   bool
		wantName string
	}{
		{"checks with no instance name is its default instance", kgtypes.GraphChecks, "", true, "default"},
		{"checks explicitly naming default is the same ref", kgtypes.GraphChecks, "default", true, "default"},
		{"knowledge keeps the precedent this follows", kgtypes.GraphKnowledge, "", true, "default"},
		// CONTROLS — an empty instance field for a family that HAS one is an
		// absent selector, never a default instance.
		{"code with no repo is a catalog enumeration, not an instance", kgtypes.GraphCode, "", false, ""},
		{"practice with no language is a catalog enumeration", kgtypes.GraphPractice, "", false, ""},
		{"cloud with no account is a catalog enumeration", kgtypes.GraphCloud, "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, ok := Normalize(tc.gt, tc.in)
			require.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.gt, ref.GraphType)
			assert.Equal(t, tc.wantName, ref.Name)
		})
	}
}

// TestSet_ChecksAdmitsAndIsFoundUnderBothSpellings drives the real Set rather
// than Normalize alone.
//
// THE TWO SPELLINGS MATTER because the admitting sites and the consulting sites
// do not agree on how they name this graph: the corpus reader sends an EMPTY name
// (the selector policy rejects a set one), while the catalog loop and the
// coverage row ask for it under "default". A collapse that landed under only one
// of those would admit a graph nothing then finds.
func TestSet_ChecksAdmitsAndIsFoundUnderBothSpellings(t *testing.T) {
	t.Parallel()

	s := New()
	require.True(t, s.Admit(kgtypes.GraphChecks, "", "manage_checks"),
		"a direct interaction with the checks graph must admit it")

	assert.True(t, s.Has(kgtypes.GraphChecks, ""), "the empty spelling the corpus reader sends must find it")
	assert.True(t, s.Has(kgtypes.GraphChecks, "default"), "the default spelling the catalog loop asks for must find it")

	// Re-admitting under the OTHER spelling is a no-op, which is how we know both
	// resolve to ONE member rather than two.
	assert.False(t, s.Admit(kgtypes.GraphChecks, "default", "manage_checks"),
		"the two spellings must be one member, or the set holds a duplicate nothing can evict")

	// CONTROL: a graph nobody interacted with is still absent, so the assertions
	// above are about this admission rather than about a set that answers true.
	assert.False(t, s.Has(kgtypes.GraphCode, "knowledge"), "an untouched graph must not be a member")
}
