// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// swiftCrossFileEdges returns every edge running from a declaration in one
// fixture file to a declaration in the other.
func swiftCrossFileEdges(t *testing.T, fromFile, toFile string, files []fixtureFile) []string {
	t.Helper()
	res := populateFixture(t, files)
	var out []string
	for _, e := range res.Edges {
		if strings.HasPrefix(e.FromId, fromFile+":") && strings.HasPrefix(e.ToId, toFile+":") {
			out = append(out, e.FromId+" --"+e.Type+"--> "+e.ToId)
		}
	}
	return out
}

// TestF0SwiftCrossFileResolvesInModule is the BEHAVIORAL catcher for swift's
// module scope: two files of one module must see each other's declarations,
// with no import between them, because swift has no file-level import within a
// module.
//
// IT CARRIES ITS OWN NEGATIVE CONTROL. The same two files, moved to a path
// OUTSIDE the layout convention, must resolve nothing across the file boundary
// — that is what proves the module scope is doing the work rather than some
// other rung that would have bound the reference anyway.
func TestF0SwiftCrossFileResolvesInModule(t *testing.T) {
	const greeterSrc = "class Greeter {\n    func greet() -> Int {\n        return 1\n    }\n}\n"
	const serverSrc = "class Server {\n    func run() -> Int {\n        return Greeter().greet()\n    }\n}\n"

	inModule := swiftCrossFileEdges(t,
		"Sources/Greeting/Server.swift", "Sources/Greeting/Greeter.swift",
		[]fixtureFile{
			{path: "Sources/Greeting/Greeter.swift", src: greeterSrc},
			{path: "Sources/Greeting/Server.swift", src: serverSrc},
		})
	require.NotEmptyf(t, inModule,
		"no edge crosses the file boundary inside one swift module: two files under Sources/<Module>/ share a resolution unit, and a reference to a sibling declaration must bind through it")

	// THE NEGATIVE CONTROL, byte-identical sources at a path the convention
	// does not describe. Without it, an edge found above could have come from
	// any rung and this test would prove nothing about the scope.
	outsideModule := swiftCrossFileEdges(t,
		"m/Server.swift", "m/Greeter.swift",
		[]fixtureFile{
			{path: "m/Greeter.swift", src: greeterSrc},
			{path: "m/Server.swift", src: serverSrc},
		})
	require.Emptyf(t, outsideModule,
		"a tree outside the layout convention must keep the narrow file scope, so the same reference stays unbound: %v", outsideModule)
}
