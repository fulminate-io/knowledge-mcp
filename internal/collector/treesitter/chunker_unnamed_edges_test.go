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
// The C fixture is the ordinary shape: a bare `struct Point p;` inside a
// function body is chunked as an unnamed `declaration` that carries a type
// reference. The total edge count is the known positive beside the zero — an
// emitter that stopped producing anything at all would satisfy "no empty
// FromID" while breaking everything, and only the count can tell the two
// apart. Both numbers come from a run, not from a description.
//
// The file-to-declaration CONTAINS edge for an unnamed declaration
// legitimately carries an empty ToID; that is deliberate and out of scope
// here, which is why this asserts on FromID alone.
func TestUnnamedDeclEmitsNoEmptyFromIDEdges(t *testing.T) {
	const path = "pkg/g.c"
	result := chunkFile(t, path,
		"struct Point { int x; };\nvoid Point(void) {}\nstruct Point mk(void) { struct Point p; return p; }\n")

	for _, e := range result.Edges {
		assert.NotEmpty(t, e.FromID,
			"edge %s -> %q must not be emitted from an unnamed declaration", e.Type, e.ToID)
	}
	assert.Len(t, result.Edges, 10, "every other edge in the fixture is still emitted")
}
