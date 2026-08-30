// SPDX-License-Identifier: Apache-2.0

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInstanceKeyOf_RoundTripsEveryFamily drives the reverse read against
// selectors built by the PRODUCTION builder rather than by hand, which is what
// makes it pin both directions of one switch instead of comparing a literal
// against itself: a family added to InstanceField and wired into only one
// direction fails here.
func TestInstanceKeyOf_RoundTripsEveryFamily(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		gt   kgtypes.GraphType
		name string
	}{
		{kgtypes.GraphCode, "repoA"},
		{kgtypes.GraphCloud, "acct-1"},
		{kgtypes.GraphCICD, "org-1"},
		{kgtypes.GraphPractice, "go"},
	} {
		t.Run(string(tc.gt), func(t *testing.T) {
			t.Parallel()
			gotGT, gotName, ok := InstanceKeyOf(GraphSelectorFor(tc.gt, tc.name, false))
			require.True(t, ok)
			assert.Equal(t, tc.gt, gotGT)
			assert.Equal(t, tc.name, gotName)
		})
	}

	// checks ROUND-TRIPS TO NO INSTANCE: it is the one family with no instance
	// identity anywhere, so no builder may put a name on it.
	//
	// knowledge and linkage are deliberately NOT asserted this way. They are
	// singletons for the SERVER's resolver, but they do carry an instance
	// identity for client-internal routing — segmentdist names knowledge graphs
	// and routes on that name — so their round trip keeps the name. That
	// distinction is AddressesOneGraph's, not InstanceField's.
	t.Run("singleton checks carries no instance identity", func(t *testing.T) {
		t.Parallel()
		gotGT, gotName, ok := InstanceKeyOf(GraphSelectorFor(kgtypes.GraphChecks, "default", false))
		require.True(t, ok)
		assert.Equal(t, kgtypes.GraphChecks, gotGT, "the family must still round-trip")
		assert.Empty(t, gotName, "checks holds ONE graph and has no named consumer, so it carries no instance name")
	})

	t.Run("an empty Graph is the knowledge default", func(t *testing.T) {
		t.Parallel()
		gt, name, ok := InstanceKeyOf(&knowledgev1.GraphSelector{})
		require.True(t, ok)
		assert.Equal(t, kgtypes.GraphKnowledge, gt)
		assert.Empty(t, name, `the ""→"default" collapse belongs to workingset.Normalize, not here`)
	})

	t.Run("a type-only target resolves no instance", func(t *testing.T) {
		t.Parallel()
		// The shape a catalog enumeration compiles to: a graph type and nothing else.
		gt, name, ok := InstanceKeyOf(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode)})
		require.True(t, ok, "the selector still names a graph TYPE")
		assert.Equal(t, kgtypes.GraphCode, gt)
		assert.Empty(t, name, "an enumeration must resolve no instance key")
	})

	t.Run("a nil selector addresses nothing", func(t *testing.T) {
		t.Parallel()
		_, _, ok := InstanceKeyOf(nil)
		assert.False(t, ok)
	})
}

// TestInstanceField_EveryBuiltinFamilyPinned is the exhaustive pin on the ONE
// partition: for every builtin family, which selector field carries its
// instance — and, for the singletons, that the answer is "none".
//
// WHY EXHAUSTIVE AND NOT PER-INCIDENT. This switch replaced a hand-maintained
// map of name-blind families, and an audit of that map found THREE of its seven
// rows could be deleted with the whole suite still green — including the
// knowledge row, in the very file that existed as the knowledge-family
// regression pin, because every payload there omitted `graph` and so exercised
// the "" alias instead. A regression test that pins the incident's exact
// spelling pins the incident, not the rule. A table over every family cannot
// have that gap: delete any arm and a row goes red.
//
// THE EMPTY STRING IS A SEPARATE ROW FROM "knowledge" ON PURPOSE. They are the
// same family and different spellings, the second is what real callers send,
// and covering only one is exactly the hole described above.
//
// The projection is asserted alongside the field with a DISTINCT sentinel per
// input, so a wrong arm returns a visibly wrong value rather than an empty
// string that could be mistaken for a correct singleton answer.
func TestInstanceField_EveryBuiltinFamilyPinned(t *testing.T) {
	const (
		repo    = "sentinel-repo"
		account = "sentinel-account"
		name    = "sentinel-name"
		lang    = "sentinel-language"
	)

	cases := []struct {
		gt        kgtypes.GraphType
		wantField Field
		wantValue string
	}{
		// SERVER-SIDE SINGLETONS. Their projected value is empty because the
		// server's resolver consumes no instance field for them, but knowledge
		// and linkage still report a client-internal identity FIELD — the two
		// columns diverge here on purpose, and that divergence is the finding
		// this table exists to hold.
		{"", FieldName, ""},
		{kgtypes.GraphKnowledge, FieldName, ""},
		{kgtypes.GraphLinkage, FieldName, ""},
		// checks alone has no identity anywhere.
		{kgtypes.GraphChecks, FieldNone, ""},

		// Instance-addressed families, one field each.
		{kgtypes.GraphCode, FieldRepo, repo},
		{kgtypes.GraphCloud, FieldAccount, account},
		{kgtypes.GraphCICD, FieldAccount, account},
		{kgtypes.GraphPractice, FieldLanguage, lang},

		// Name-addressed families.
		{kgtypes.GraphLogs, FieldName, name},
		{kgtypes.GraphWebRaw, FieldName, name},
		{kgtypes.GraphPDFRaw, FieldName, name},
		{kgtypes.GraphTransformers, FieldName, name},

		// A registered custom type is name-addressed by the default arm.
		{kgtypes.GraphType("my-registered-type"), FieldName, name},
	}

	seen := map[kgtypes.GraphType]bool{}
	for _, c := range cases {
		t.Run(string(c.gt), func(t *testing.T) {
			assert.Equalf(t, c.wantField, InstanceField(c.gt),
				"family %q addresses its instance by a different field than pinned", c.gt)
			assert.Equalf(t, c.wantValue, InstanceValueOf(c.gt, repo, account, name, lang),
				"family %q projected the wrong caller value; the projection and the field must agree", c.gt)
		})
		seen[c.gt] = true
	}

	// COMPLETENESS: every builtin graph type must have a row. Without this, a
	// newly added family silently inherits the FieldName default — which is how
	// checks shipped carrying an instance name for a family that has none.
	for _, gt := range kgtypes.SyncEligibleGraphTypes() {
		assert.Truef(t, seen[gt], "builtin family %q has no row in this table", gt)
	}
	for _, gt := range []kgtypes.GraphType{kgtypes.GraphLogs, kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw} {
		assert.Truef(t, seen[gt], "builtin family %q has no row in this table", gt)
	}
}
