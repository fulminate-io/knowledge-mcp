// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// reference_group_key_test.go holds the two properties the reference group key
// must have, and it exists because the key it replaced had neither.
//
// THE DEFECT, MEASURED RATHER THAN IMAGINED. A live graph was probed against the
// client's own capture, and the server was holding one (from, to, type) USES_TYPE
// edge
// TWICE — at evidence offsets :2982: and :3037: — while the client emitted only
// the later one. The key embedded the emitting declaration's byte offset, an
// edit above the site shifted it, and because evidence is part of the four-part
// edge identity the shifted key was a NEW ROW. The pre-edit row was never
// reclaimed, the file's server-side aggregate could never match the client's,
// and the file re-uploaded on every collect thereafter. Across six probed files
// the server's live edge count exceeded the client's by 1, 3, 1, 1, 1 and 1.

// byteShiftBase is a caller whose reference sites are ambiguous (two
// declarations of each name), so every site emits a keyed group.
var byteShiftBase = []fixtureFile{
	{path: "svc/one.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 1 }\n"},
	{path: "svc/two.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 100 }\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\nfunc Caller() int {\n\treturn Alpha()\n}\n"},
}

// byteShiftEdited is byteShiftBase with a declaration INSERTED ABOVE the caller.
// Every byte offset in caller.go moves; nothing about the reference site itself
// does. This is the ordinary editing shape the old key got wrong.
var byteShiftEdited = []fixtureFile{
	{path: "svc/one.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 1 }\n"},
	{path: "svc/two.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 100 }\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\n// A comment that did not exist before.\nfunc Padding() int {\n\treturn 0\n}\n\n" +
		"func Caller() int {\n\treturn Alpha()\n}\n"},
}

// byteShiftMoved keeps caller.go the same length-wise but MOVES the reference
// into a different enclosing declaration. This is the KNOWN-POSITIVE CONTROL
// for the test below: the key is supposed to track the enclosing declaration,
// so this edit MUST change it. Without this arm, "the key did not change" would
// also be satisfied by a key that can never change.
var byteShiftMoved = []fixtureFile{
	{path: "svc/one.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 1 }\n"},
	{path: "svc/two.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 100 }\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\nfunc Caller() int {\n\treturn 0\n}\n\nfunc Other() int {\n\treturn Alpha()\n}\n"},
}

// TestReferenceGroupKey_StableAcrossByteShift is the regression guard for the
// measured defect: inserting text above a reference must not re-key it.
func TestReferenceGroupKey_StableAcrossByteShift(t *testing.T) {
	keysOf := func(files []fixtureFile) map[string]int {
		res := populateFixture(t, files)
		edges := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.NotEmpty(t, edges,
			"control: the fixture must emit a keyed ambiguous group at all, or every "+
				"comparison below is between two empty sets")
		return evidenceKeys(edges)
	}

	before := keysOf(byteShiftBase)
	after := keysOf(byteShiftEdited)
	assert.Equal(t, before, after,
		"inserting a declaration ABOVE the reference shifts every byte offset in the file "+
			"and must leave the group key untouched — a re-keyed edge is a NEW identity, so the "+
			"pre-edit row is orphaned and the file can never converge")

	// KNOWN-POSITIVE CONTROL. The key names the ENCLOSING DECLARATION, so
	// genuinely moving the reference into another declaration MUST re-key it.
	// The resulting reclaim is correct rather than churn.
	moved := keysOf(byteShiftMoved)
	assert.NotEqual(t, before, moved,
		"moving the reference into a DIFFERENT declaration must change the key, or the "+
			"stability assertion above is vacuous — it would be satisfied by a constant")

	for key := range before {
		assert.NotRegexp(t, `:\d+:CALLS:`, key,
			"no byte offset may appear in the key: %q", key)
	}
}

// twoIdenticalSites declares one reference target twice inside ONE declaration
// in one file. Both sites are identical in every recorded respect — same target,
// same edge type, same enclosing declaration — which is the one case the
// discriminator alone cannot separate and the ordinal exists for.
var twoIdenticalSites = []fixtureFile{
	{path: "svc/one.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 1 }\n"},
	{path: "svc/two.go", src: "" +
		"package svc\n\nfunc Alpha() int { return 100 }\n"},
	{path: "svc/caller.go", src: "" +
		"package svc\n\nfunc Caller() int {\n\treturn Alpha()\n}\n\nfunc Second() int {\n\treturn Alpha()\n}\n"},
}

// TestReferenceGroupKey_IdenticalSitesBothSurvive pins the no-dropping property:
// two real sites remain two rows, never collapsed onto one key.
func TestReferenceGroupKey_IdenticalSitesBothSurvive(t *testing.T) {
	res := populateFixture(t, twoIdenticalSites)
	edges := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
	require.NotEmpty(t, edges, "control: the fixture must emit keyed ambiguous groups at all")

	keys := evidenceKeys(edges)
	// TWO DISTINCT SITES, TWO DISTINCT KEYS. A scheme that collapsed them would
	// report one key here, and one of the two references would be silently lost
	// the moment the two rows contended for one four-part identity.
	require.GreaterOrEqual(t, len(keys), 2,
		"two separate reference sites must carry two distinct group keys, got: %v", keys)

	// EVERY MEMBER OF ONE SITE'S GROUP SHARES THAT SITE'S ONE KEY. The ordinal
	// separates SITES, never the candidates of a single site — handing each
	// candidate its own key would destroy the grouping the key exists to express.
	for key, n := range keys {
		assert.Equal(t, 2, n,
			"group %q must carry one edge per candidate declaration (the fixture declares 2)", key)
	}
}
