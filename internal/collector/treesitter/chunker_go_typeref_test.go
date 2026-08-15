// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usesTypeTargetSet returns the set of USES_TYPE edge targets a file emitted.
func usesTypeTargetSet(edges []Edge) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		if e.Type == EdgeUsesType {
			out[e.ToID] = true
		}
	}
	return out
}

// TestGoQualifiedTypeRef pins BOTH halves of the qualified-type change: the
// query captures qualified_type, and extractTypeRefEdges keeps only the
// OUTERMOST capture per type expression. Capturing without the outermost rule
// doubles every cross-package type reference; the rule without the capture is
// inert.
//
// The fixture uses DISTINCT concrete names so no two assertions collapse onto
// one value, and covers three different nesting shapes — a field, a slice
// element and a generic base — because each is a separate way for an inner
// bare capture to leak through.
func TestGoQualifiedTypeRef(t *testing.T) {
	_, edges := chunkImportFixture(t, "app/types.go", `package app

import (
	"example.com/mod/store"
	"example.com/mod/pkg"
)

type Local struct{}

type Holder struct {
	F store.Node
	G Local
	H []pkg.Thing
	I pkg.Generic[T]
}

func mk(w store.Node) (Local, error) { return Local{}, nil }
`)

	targets := usesTypeTargetSet(edges)
	require.NotEmpty(t, targets, "the fixture emitted no USES_TYPE edges at all, so every assertion below would be vacuous")

	for _, want := range []string{"store.Node", "pkg.Thing", "pkg.Generic", "Local", "T"} {
		assert.True(t, targets[want], "expected a USES_TYPE target %q; got %v", want, targets)
	}

	// THE CATCHER FOR THE OUTERMOST RULE BEING OMITTED, over three different
	// nesting shapes. Each bare name is the inner type_identifier of a
	// qualified type that was already captured whole.
	for _, unwanted := range []string{"Node", "Thing", "Generic"} {
		assert.False(t, targets[unwanted],
			"a bare %q means the inner capture of an already-accepted qualified type survived", unwanted)
	}

	// `error` is skipped by the builtins map, so its absence says the builtin
	// skip is still doing its job on a fixture that demonstrably produced edges.
	assert.False(t, targets["error"], "builtin types are not USES_TYPE targets")
}
