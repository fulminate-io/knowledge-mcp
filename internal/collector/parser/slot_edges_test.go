// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// parentMemberContains returns every CONTAINS edge whose source is NOT a file
// node — i.e. the parent-to-member shape — as (from, to) pairs.
func parentMemberContains(res PopulateResult) [][2]string {
	isFile := fileNodeIDs(res)
	var out [][2]string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
			continue
		}
		if isFile[e.FromId] {
			continue
		}
		out = append(out, [2]string{e.FromId, e.ToId})
	}
	return out
}

// requireFileContainment is the KNOWN-POSITIVE CONTROL shared by every subtest
// below. Without it, the zero asserted in receiver_not_declared_emits_nothing
// would also be satisfied by a run that emitted no containment whatsoever.
func requireFileContainment(t *testing.T, res PopulateResult, file, symbolNodeID string) {
	t.Helper()
	require.True(t, hasEdge(res, kgtypes.EdgeContains, file, symbolNodeID),
		"control failed: expected file-to-symbol CONTAINS %q -> %q", file, symbolNodeID)
}

// TestGoReceiverContainmentResolvesInPackage is the catcher for the ONE
// containment source that is not positional.
//
// A Go method's container is its RECEIVER TYPE, which declParentName takes from
// extractGoReceiver — a SIBLING declaration, not an AST ancestor. So the
// chunker cannot address it by slot and the edge arrives carrying a name and a
// Ref instead. Go's own rule closes it: a receiver type is declared in the SAME
// PACKAGE, so the source resolves at package scope.
//
// Measured on this repo: 5,379 Go methods carry a receiver; 4,029 (74.9%)
// declare the receiver type in the same file and 1,350 (25.1%) in another file
// of the same package. That second population is why a per-file positional
// lookup is not an acceptable implementation — it would pass the first subtest
// here and silently drop a quarter of Go receiver containment.
func TestGoReceiverContainmentResolvesInPackage(t *testing.T) {
	t.Run("same_file_receiver", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{
			{path: "svc/svc.go", src: "" +
				"package svc\n\ntype Svc struct{}\n\nfunc (s Svc) Do() int { return 1 }\n"},
		})

		requireFileContainment(t, res, "svc/svc.go", "svc/svc.go:Svc")
		requireFileContainment(t, res, "svc/svc.go", "svc/svc.go:Svc.Do")

		require.Equal(t, [][2]string{{"svc/svc.go:Svc", "svc/svc.go:Svc.Do"}},
			parentMemberContains(res),
			"the receiver type must contain its method, addressed exactly")
	})

	t.Run("cross_file_receiver_same_package", func(t *testing.T) {
		// THE 1,350-EDGE CASE: the type in one file, the method in another,
		// both in one directory and one package. A same-file-only
		// implementation passes the subtest above and fails this one.
		res := populateFixture(t, []fixtureFile{
			{path: "svc/types.go", src: "" +
				"package svc\n\ntype Svc struct{ n int }\n"},
			{path: "svc/methods.go", src: "" +
				"package svc\n\nfunc (s Svc) Do() int { return s.n }\n"},
		})

		requireFileContainment(t, res, "svc/types.go", "svc/types.go:Svc")
		requireFileContainment(t, res, "svc/methods.go", "svc/methods.go:Svc.Do")

		require.Equal(t, [][2]string{{"svc/types.go:Svc", "svc/methods.go:Svc.Do"}},
			parentMemberContains(res),
			"the receiver must resolve to the type declared in the SIBLING file of the same package")
	})

	t.Run("receiver_not_declared_emits_nothing", func(t *testing.T) {
		// The catcher for the lookup ESCAPING ITS SCOPE. `other` declares a
		// type named Missing; `svc` does not. The Go rule says a receiver
		// resolves in its OWN package, so the svc method must bind to nothing
		// — emphatically not to other/other.go:Missing.
		res := populateFixture(t, []fixtureFile{
			{path: "other/other.go", src: "" +
				"package other\n\ntype Missing struct{}\n"},
			{path: "svc/methods.go", src: "" +
				"package svc\n\nfunc (m Missing) Do() int { return 2 }\n"},
		})

		requireFileContainment(t, res, "other/other.go", "other/other.go:Missing")
		requireFileContainment(t, res, "svc/methods.go", "svc/methods.go:Missing.Do")

		require.Empty(t, parentMemberContains(res),
			"a receiver declared in no file of this package must bind to NOTHING, "+
				"and above all not to a same-named type in another directory")
	})
}
