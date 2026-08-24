// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUnnamedDeclEmitsNoEmptyFromIDEdges pins the guard over the call and
// type-reference emitters. A declaration whose query binds no @name reaches
// emitDeclarationEdges with symbolName "", so qualifiedName returns "" and any
// edge built from it carries an EMPTY FromID — waste rather than corruption,
// since resolution always drops it, but emitted on every unnamed declaration
// that contains a call or a type reference.
//
// THE UNNAMED C DECLARATION IS NOW A PROTOTYPE, AND THAT MOVED. This fixture
// used to rely on a bare `struct Point p;` inside a function body being an
// unnamed `declaration`; C variable declarations are named by
// resolveDeclNameC now, so that shape no longer exercises the guard. A function
// PROTOTYPE still does: the resolver declines it deliberately, because a
// prototype and its definition share a name in the same file and naming it
// would make every call to that function ambiguous. So `struct Point make(int);`
// is the unnamed declaration here, and it is the right subject precisely
// because it DOES carry a type reference to Point — one that produces no edge
// at all, which is the guard doing its job. Its only edge is the
// file-to-declaration CONTAINS with the empty ToID noted below.
//
// The total edge count is the known positive beside the zero — an emitter that
// stopped producing anything at all would satisfy "no empty FromID" while
// breaking everything, and only the count can tell the two apart. Both numbers
// come from a run, not from a description.
//
// The file-to-declaration CONTAINS edge for an unnamed declaration
// legitimately carries an empty ToID; that is deliberate and out of scope
// here, which is why this asserts on FromID alone.
func TestUnnamedDeclEmitsNoEmptyFromIDEdges(t *testing.T) {
	const path = "pkg/g.c"
	result := chunkFile(t, path,
		"struct Point { int x; };\nvoid Point(void) {}\nstruct Point make(int n);\n"+
			"struct Point mk(void) { struct Point p; return p; }\n")

	// CONTROL: the fixture really does contain an unnamed declaration, so the
	// zero below is the guard holding rather than the subject having vanished.
	unnamed := 0
	for _, c := range result.Chunks {
		if c.ChunkType == "declaration" && c.Name == "" {
			unnamed++
		}
	}
	assert.Positive(t, unnamed,
		"the fixture must still contain an unnamed declaration, or this guard is asserting over nothing")

	for _, e := range result.Edges {
		assert.NotEmpty(t, e.FromID,
			"edge %s -> %q must not be emitted from an unnamed declaration", e.Type, e.ToID)
	}
	assert.Len(t, result.Edges, 8, "every other edge in the fixture is still emitted")
}
