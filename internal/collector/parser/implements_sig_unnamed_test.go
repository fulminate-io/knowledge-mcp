// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSigKeyAllUnnamedParameterList is the reproduction for the mis-parsed
// all-unnamed parameter list.
//
// THE FIXTURE IS THE REVIEWER'S PROBE, TRANSCRIBED FROM THE FROZEN AGENT CORPUS
// RATHER THAN INVENTED. `InstancesClient.SetInstanceLabels` is declared with
// NAMED parameters at cmd/agent/internal/domains/jobs/gce_compute.go:42 and
// `stubInstancesClient.SetInstanceLabels` satisfies it with an ALL-UNNAMED list
// at cmd/agent/internal/bootstrap/jobsystem_backend_selection_test.go:83. The
// two spellings denote one signature, so their keys must be one string.
//
// WHY NO EXISTING FIXTURE CAUGHT IT: every signature fixture in this tree writes
// both sides with names. An interface written with named parameters against a
// stub written without them is a shape no unit test in the repository
// constructs, and the corruption is invisible to any fixture that does not
// construct it — the derivation still produced a plausible key, of the right
// parameter COUNT, and simply matched nothing.
func TestSigKeyAllUnnamedParameterList(t *testing.T) {
	const jobsSrc = `package jobs

import "context"

type InstancesClient interface {
	SetInstanceLabels(ctx context.Context, project, zone, name string, labels map[string]string) error
}
`
	const bootstrapSrc = `package bootstrap

import "context"

type stubInstancesClient struct{}

func (stubInstancesClient) SetInstanceLabels(context.Context, string, string, string, map[string]string) error {
	return nil
}
`

	ix := indexResults(t, chunkFixture(t, []fixtureFile{
		{path: "jobs/gce_compute.go", src: jobsSrc},
		{path: "bootstrap/selection.go", src: bootstrapSrc},
	}))

	spec := recFor(t, ix, "jobs/gce_compute.go:InstancesClient.SetInstanceLabels")
	impl := recFor(t, ix, "bootstrap/selection.go:stubInstancesClient.SetInstanceLabels")

	// CONTROL: both sides resolved a key at all, so the equality below cannot
	// hold between two empty strings.
	require.NotEmpty(t, spec.SigKey, "control: the named interface spec resolved a key")
	require.NotEmpty(t, impl.SigKey, "control: the unnamed method resolved a key")

	// THE KEY IS PINNED, NOT MERELY COMPARED. Asserting only equality would be
	// satisfied by a renderer that corrupted BOTH sides identically; the literal
	// states which five parameters the signature actually has.
	const want = "(ext:context.Context,ext:string,ext:string,ext:string,map[ext:string]ext:string)(ext:error)"
	assert.Equal(t, want, spec.SigKey,
		"the NAMED spelling renders five parameters, the last a map")
	assert.Equal(t, want, impl.SigKey,
		"the ALL-UNNAMED spelling denotes the same five parameters and must render the same key")
	assert.Equal(t, spec.SigKey, impl.SigKey,
		"a named interface spec and an unnamed implementer of one signature must render ONE key")
}

// TestGoUnnamedParameterShapes pins the rendering of every parameter-list shape
// the Go grammar mis-brackets, each against the NAMED spelling of the same
// signature — which is the only comparison that proves a rendering correct
// rather than merely stable.
//
// THE FUSING SHAPES ARE EXACTLY TWO, and that is a measured claim rather than an
// assumption. Executing the grammar over the type vocabulary shows generic,
// variadic, qualified, pointer, slice, function, struct, interface and BOTH
// directional channel forms each parse as their own single node inside an
// unnamed list. Only `map` and `chan` — the two type-leading RESERVED WORDS —
// are lexed as a bare `identifier` in a name position and fused with the node
// that follows them.
func TestGoUnnamedParameterShapes(t *testing.T) {
	const src = `package shapes

type Named interface {
	Map(a string, m map[string]string) error
	Chan(a string, c chan int) error
	NestedMap(a string, m map[string]map[string]int) error
	SliceValue(a string, m map[string][]byte) error
	Trailing(a string, m map[string]string, z int) error
	Leading(m map[string]int, a string, n map[string]bool) error
	Plain(a string, b []byte) error
	Directional(a string, c chan<- int) error
}

type Unnamed interface {
	Map(string, map[string]string) error
	Chan(string, chan int) error
	NestedMap(string, map[string]map[string]int) error
	SliceValue(string, map[string][]byte) error
	Trailing(string, map[string]string, int) error
	Leading(map[string]int, string, map[string]bool) error
	Plain(string, []byte) error
	Directional(string, chan<- int) error
}
`

	ix := indexResults(t, chunkFixture(t, []fixtureFile{{path: "shapes/s.go", src: src}}))

	for _, method := range []string{
		"Map", "Chan", "NestedMap", "SliceValue",
		"Trailing", "Leading", "Plain", "Directional",
	} {
		t.Run(method, func(t *testing.T) {
			named := recFor(t, ix, "shapes/s.go:Named."+method)
			unnamed := recFor(t, ix, "shapes/s.go:Unnamed."+method)
			require.NotEmpty(t, named.SigKey, "control: the named spelling resolved a key")
			assert.Equal(t, named.SigKey, unnamed.SigKey,
				"the named and unnamed spellings of one signature must render one key")
		})
	}

	// KNOWN-NEGATIVE CONTROL. Every assertion above is an EQUALITY, and a
	// renderer that collapsed every signature to one string would satisfy all of
	// them. These two signatures differ, so their keys must differ.
	t.Run("distinct_signatures_stay_distinct", func(t *testing.T) {
		mapped := recFor(t, ix, "shapes/s.go:Unnamed.Map")
		channed := recFor(t, ix, "shapes/s.go:Unnamed.Chan")
		assert.NotEqual(t, mapped.SigKey, channed.SigKey,
			"a map parameter and a chan parameter are different types")
	})
}

// TestGoNamedParameterListUnaffected is the catcher for the direction the fix
// could break rather than the one it repairs.
//
// A GENUINELY NAMED PARAMETER WHOSE TYPE IS A SIZED ARRAY PRODUCES THE IDENTICAL
// TREE SHAPE to the mis-parse — `c [maxLen]byte` is (identifier, array_type)
// with an identifier in the array's length slot, exactly like the `map` fusion —
// so any rule keyed on that SHAPE corrupts correct code. The rule is keyed on
// the reserved word instead, and this fixture is what proves the difference is
// observed: `maxLen` is not a keyword, so the declaration must still render as
// ONE parameter of an array type.
func TestGoNamedParameterListUnaffected(t *testing.T) {
	const src = `package named

const maxLen = 16

type Sized struct{}

type Contract interface {
	Fixed(a, b string, c [maxLen]byte) error
}

type Impl struct{}

func (Impl) Fixed(a, b string, c [maxLen]byte) error { return nil }
`

	ix := indexResults(t, chunkFixture(t, []fixtureFile{{path: "named/n.go", src: src}}))

	spec := recFor(t, ix, "named/n.go:Contract.Fixed")
	impl := recFor(t, ix, "named/n.go:Impl.Fixed")

	const want = "(ext:string,ext:string,[]ext:byte)(ext:error)"
	assert.Equal(t, want, spec.SigKey,
		"a named parameter of a sized-array type is ONE parameter — the array length is dropped, "+
			"matching the prototype's ast.ArrayType arm, and the element is the leaf")
	assert.Equal(t, spec.SigKey, impl.SigKey,
		"and the concrete method renders identically")
}
