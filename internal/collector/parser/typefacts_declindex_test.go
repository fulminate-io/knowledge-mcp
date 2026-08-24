// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recFor returns the indexed declaration record for one node ID.
func recFor(t *testing.T, ix *declIndex, nodeID string) *declRec {
	t.Helper()
	rec, ok := ix.byID[nodeID]
	require.True(t, ok, "declaration %q is not in the index", nodeID)
	return rec
}

// TestTypeFactsResolveThroughDeclaringFileBinds covers the two claims Step 3
// makes: that all four Go RESULT SHAPES are read correctly, and that the type
// text is resolved through the DECLARING file's own reference site.
//
// The result shapes are pinned individually because the failure they guard is
// SILENT. An ordinal rule — "the result list is the third parameter_list" —
// gets the three-list method right and gets a result-less method right by
// accident, while dropping the result of a method whose result is a BARE TYPE.
// A dropped result is a missing call-return binding in the largest fan-out
// stratum, invisible rather than loud, so method_with_bare_type_result is the
// catcher and no_results_is_empty pins the accidentally-right case so a later
// refactor toward ordinals cannot pass on that one alone.
func TestTypeFactsResolveThroughDeclaringFileBinds(t *testing.T) {
	const src = `package svc

import other "example.com/other"

func single() Alpha {
	return Alpha{}
}

func (s *Server) Handle(p Req, q Extra) (Beta, error) {
	return Beta{}, nil
}

func (s *Server) Bare(p Req) Gamma {
	return Gamma{}
}

func (s *Server) Silent() {
}

func external() other.Thing {
	return other.Thing{}
}

func New() *Thing {
	return &Thing{}
}

func NewValue() Thing {
	return Thing{}
}

func NewBox() *Box[int] {
	return &Box[int]{}
}

func NewSlice() []Delta {
	return nil
}

func mixed() ([]Delta, Alpha) {
	return nil, Alpha{}
}

func mixedNamed() (items []Delta, a Alpha) {
	return nil, Alpha{}
}

type Holder struct {
	Field  Delta
	A, B   Epsilon
	Ptr    *Thing
	Items  []Delta
	hidden other.Thing
}
`
	ix := indexResults(t, chunkFixture(t, []fixtureFile{{path: "svc/decls.go", src: src}}))

	t.Run("single_unnamed_result", func(t *testing.T) {
		// SHAPE 1, bare type on a function: the node after the parameter list
		// is a single type node, and it is ONE result.
		rec := recFor(t, ix, "svc/decls.go:single")
		require.Len(t, rec.ResultTypes, 1, "a single bare-type result is one result")
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Alpha"}, rec.ResultTypes[0],
			"an unqualified type resolves into the declaring file's own scope")
	})

	t.Run("method_with_params_and_results", func(t *testing.T) {
		// SHAPE 2, unnamed list on a METHOD — the arrangement with THREE
		// parameter_lists, where the receiver holds the first slot.
		rec := recFor(t, ix, "svc/decls.go:Server.Handle")
		require.Len(t, rec.ResultTypes, 2, "the result LIST is read, not the parameter list")
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Beta"}, rec.ResultTypes[0])
		assert.Equal(t, "error", rec.ResultTypes[1].Name,
			"position is preserved even for a type with no in-repo declaration")
	})

	t.Run("method_with_bare_type_result", func(t *testing.T) {
		// SHAPE 1 on a METHOD, and THE CATCHER: there is no third
		// parameter_list here, so an ordinal rule concludes "no results" and
		// silently drops a declared result type.
		rec := recFor(t, ix, "svc/decls.go:Server.Bare")
		require.Len(t, rec.ResultTypes, 1,
			"a method whose result is a BARE TYPE still has one result — the ordinal rule's blind spot")
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Gamma"}, rec.ResultTypes[0])
	})

	t.Run("no_results_is_empty", func(t *testing.T) {
		// The accidentally-right case, pinned so a refactor toward ordinals
		// cannot pass on it alone: the node after the parameter list is the
		// BLOCK, which is what "no results" actually looks like.
		rec := recFor(t, ix, "svc/decls.go:Server.Silent")
		assert.Empty(t, rec.ResultTypes, "a declaration returning nothing records no results")
	})

	t.Run("unbound_qualifier_declines", func(t *testing.T) {
		// The declaring file imports `other`, but this index is built without
		// the binds pass, so the qualifier resolves to no scope. The DECLINE is
		// the zero typeRef — never a guessed scope.
		rec := recFor(t, ix, "svc/decls.go:external")
		require.Len(t, rec.ResultTypes, 1, "the result is still recorded positionally")
		assert.Equal(t, typeRef{}, rec.ResultTypes[0],
			"a qualifier with no bind declines rather than inventing a scope")

		// KNOWN-POSITIVE CONTROL, and it is what makes the decline above mean
		// something. Without it, a resolveTypeText that returned the zero
		// typeRef for EVERYTHING would satisfy the assertion above while
		// proving nothing. An unqualified type in the very same file resolves.
		ctl := recFor(t, ix, "svc/decls.go:single")
		require.Len(t, ctl.ResultTypes, 1)
		assert.Equal(t, "dir:svc", ctl.ResultTypes[0].Scope,
			"control: resolution DOES produce a scope when the text is resolvable")
	})

	t.Run("pointer_result_binds_through_the_star", func(t *testing.T) {
		// THE CLASS RULE: every type text that will be resolved goes through
		// goQualTypeText — one discipline, both paths. A pointer result must
		// therefore reach the SAME typeRef as the value-returning sibling,
		// because `*Thing` and `Thing` name the same declaration and a method
		// call on either binds to it. Recording the star verbatim produces the
		// name "*Thing", which matches no declaration and declines silently.
		ptr := recFor(t, ix, "svc/decls.go:New")
		val := recFor(t, ix, "svc/decls.go:NewValue")
		require.Len(t, ptr.ResultTypes, 1)
		require.Len(t, val.ResultTypes, 1)

		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Thing"}, ptr.ResultTypes[0],
			"a pointer result binds through the star")
		assert.Equal(t, val.ResultTypes[0], ptr.ResultTypes[0],
			"the pointer and value forms of one type resolve identically")

		// Type arguments are stripped for the same reason, matching the
		// simulation: `*Box[int]` names the declaration Box.
		box := recFor(t, ix, "svc/decls.go:NewBox")
		require.Len(t, box.ResultTypes, 1)
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Box"}, box.ResultTypes[0],
			"a generic instantiation binds to its base declaration")
	})

	t.Run("container_result_declines", func(t *testing.T) {
		// A slice value has NO METHODS, so binding one would be a wrong target.
		// The decline must be the ZERO typeRef — an honest "not resolvable" —
		// rather than a populated ref carrying an unmatched name like
		// "[]Delta", which merely FAILS TO MATCH and is indistinguishable from
		// a real lookup miss.
		rec := recFor(t, ix, "svc/decls.go:NewSlice")
		require.Len(t, rec.ResultTypes, 1,
			"the result is still recorded positionally, so ResultIndex stays correct")
		assert.Equal(t, typeRef{}, rec.ResultTypes[0],
			"a container result declines to the ZERO typeRef, not to an unmatched name")

		// THE POSITION AXIS, which the value assertions above cannot see. A
		// declining result must hold ITS SLOT, because Phase 3 reads
		// ResultTypes[qt.ResultIndex] — so dropping index 0 would slide Alpha
		// down into it and bind the []Delta variable to Alpha, a WRONG TARGET
		// rather than a silent under-bind. Both multi-result branches are
		// covered: mixed() is the UNNAMED list, mixedNamed() the NAMED list,
		// and they are separate code paths in goDeclaredResults.
		for _, decl := range []string{"mixed", "mixedNamed"} {
			rec := recFor(t, ix, "svc/decls.go:"+decl)
			require.Lenf(t, rec.ResultTypes, 2, "%s: a declining result holds its slot", decl)
			assert.Equalf(t, typeRef{}, rec.ResultTypes[0], "%s: the container declines IN PLACE at index 0", decl)
			assert.Equalf(t, typeRef{Scope: "dir:svc", Name: "Alpha"}, rec.ResultTypes[1], "%s: the resolvable result stays at index 1", decl)
		}

		// KNOWN-POSITIVE CONTROL: a resolvable result in the same file still
		// produces a scope, so the zero above is specific rather than universal.
		ctl := recFor(t, ix, "svc/decls.go:NewValue")
		assert.Equal(t, "dir:svc", ctl.ResultTypes[0].Scope,
			"control: resolution DOES produce a scope when the text is resolvable")
	})

	t.Run("struct_field_types_are_recorded", func(t *testing.T) {
		rec := recFor(t, ix, "svc/decls.go:Holder")
		require.NotNil(t, rec.FieldTypes, "a struct declaration records its field types")

		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Delta"}, rec.FieldTypes["Field"])
		// Several names sharing one type each get an entry — a first-name rule
		// would silently drop B.
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Epsilon"}, rec.FieldTypes["A"])
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Epsilon"}, rec.FieldTypes["B"])

		// A field whose type carries an unbound qualifier is omitted, which for
		// a name-keyed map means the same thing as a zero entry.
		_, ok := rec.FieldTypes["hidden"]
		assert.False(t, ok, "a field type with an unbound qualifier declines")

		// THE FIELD SIDE OF THE CLASS RULE — the same two cases as the result
		// side, because the field hop resolves field types exactly as the
		// call-return hop resolves result types.
		assert.Equal(t, typeRef{Scope: "dir:svc", Name: "Thing"}, rec.FieldTypes["Ptr"],
			"a pointer field binds through the star, like a pointer result")

		_, sliceOK := rec.FieldTypes["Items"]
		assert.False(t, sliceOK,
			"a container-typed field is OMITTED — a slice value has no methods to bind")
	})
}
