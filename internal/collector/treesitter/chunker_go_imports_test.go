// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goImportFixture carries ALL FIVE Go import shapes, each with a DISTINCT
// concrete path so no two expectations below are satisfiable by one value: a
// plain import, an aliased one, a dot import, a blank import, and the
// single-line form outside the block.
const goImportFixture = `package app

import (
	"example.com/plain"
	al "example.com/aliased"
	. "example.com/dotted"
	_ "example.com/blank"
)

import solo "example.com/solo"

func use() {}
`

// TestGoImportBindings pins the arm's ImportBindings table by FULL-SLICE
// EQUALITY in source order, so a spurious sixth binding fails rather than being
// tolerated by a containment check.
func TestGoImportBindings(t *testing.T) {
	ctx, _ := chunkImportFixture(t, "app/imports.go", goImportFixture)

	assert.Equal(t, []ImportBinding{
		{Specifier: "example.com/plain", Local: "", Kind: ImportNamespace},
		{Specifier: "example.com/aliased", Local: "al", Kind: ImportNamespace},
		{Specifier: "example.com/dotted", Local: ".", Kind: ImportNamespace},
		{Specifier: "example.com/blank", Local: "_", Kind: ImportSideEffect},
		{Specifier: "example.com/solo", Local: "solo", Kind: ImportNamespace},
	}, ctx.ImportBindings,
		"Local is carried VERBATIM — \"\", \".\" and \"_\" are three distinct cases and none may be normalised into another")

	// The ctx.Imports half: exactly the five quote-stripped paths, and no local
	// name among them. An arm that appended the local name instead of the path
	// would emit a bogus IMPORTS edge for each.
	assert.Equal(t, []string{
		"example.com/plain",
		"example.com/aliased",
		"example.com/dotted",
		"example.com/blank",
		"example.com/solo",
	}, importSpecifiers(ctx.Imports))
	for _, unwanted := range []string{"al", ".", "_", "solo"} {
		assert.NotContains(t, importSpecifiers(ctx.Imports), unwanted,
			"a local binding name is not a dependency and must never reach importSpecifiers(ctx.Imports)")
	}
}

// TestGoImportEdgesUnchanged IS THE NAMED CATCHER for the arm omitting its
// ctx.Imports append. An arm OWNS every capture for its language, so that
// omission emits ZERO IMPORTS edges for every Go file and loses framework
// detection — with no compile error, because the field is simply never written.
func TestGoImportEdgesUnchanged(t *testing.T) {
	_, edges := chunkImportFixture(t, "app/imports.go", goImportFixture)

	targets := importEdgeTargets(edges)
	require.Len(t, targets, 5, "one IMPORTS edge per import statement, exactly as before the arm existed")
	assert.Equal(t, []string{
		"example.com/aliased",
		"example.com/blank",
		"example.com/dotted",
		"example.com/plain",
		"example.com/solo",
	}, targets)

	for _, e := range edges {
		if e.Type != EdgeImports {
			continue
		}
		assert.NotContains(t, []string{"al", ".", "_", "solo"}, e.ToID,
			"an IMPORTS edge target is a dependency path, never an alias, a dot or an underscore")
	}
}
