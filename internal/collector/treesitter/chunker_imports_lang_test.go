// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportCaptureCarriesNames asserts that each language's import capture
// carries BOTH the source path and the locally bound name, which is what the
// resolution ladder's import rungs need and what a path-only capture destroyed.
//
// EVERY ALIAS FIXTURE USES A LOCAL NAME THAT DIFFERS FROM THE LAST PATH
// SEGMENT. `import a.b.D as D` would pass against a capture that never read the
// alias at all, so the assertion would prove nothing; `as E` cannot.
//
// Swift and java have no alias form, so they assert the path-and-name split
// instead, plus the shape that binds NOTHING — a module import and a wildcard
// import name no member, and inventing one would assert a name the source never
// wrote.
func TestImportCaptureCarriesNames(t *testing.T) {
	t.Run("java", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/A.java",
			"import a.b.C;\nimport x.y.*;\nclass A { void m() {} }\n")

		got := bindingFor(t, ctx, "C")
		assert.Equal(t, "a.b", got.Specifier)
		assert.Equal(t, "C", got.Imported)

		wild := wildcardBindingFor(t, ctx, "x.y")
		assert.Empty(t, wild.Local, "a wildcard import binds no single name")
	})

	t.Run("kotlin", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/K.kt",
			"import a.b.C\nimport a.b.D as E\nclass K { fun m() {} }\n")

		got := bindingFor(t, ctx, "E")
		assert.Equal(t, "a.b", got.Specifier)
		assert.Equal(t, "D", got.Imported, "the alias must not overwrite the declared name")

		plain := bindingFor(t, ctx, "C")
		assert.Equal(t, "a.b", plain.Specifier)
		assert.Equal(t, "C", plain.Imported)
	})

	t.Run("scala", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/S.scala",
			"import a.b.C\nimport a.{D => F}\nobject S { def m(): Int = 1 }\n")

		got := bindingFor(t, ctx, "F")
		assert.Equal(t, "a", got.Specifier)
		assert.Equal(t, "D", got.Imported)

		plain := bindingFor(t, ctx, "C")
		assert.Equal(t, "a.b", plain.Specifier)
	})

	t.Run("swift", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/S.swift",
			"import Foundation\nimport struct Ext.Helper\nclass S { func m() {} }\n")

		got := bindingFor(t, ctx, "Helper")
		assert.Equal(t, "Ext", got.Specifier)
		assert.Equal(t, "Helper", got.Imported)

		mod := wildcardBindingFor(t, ctx, "Foundation")
		assert.Empty(t, mod.Local, "a plain swift import names a module, not a member")
	})

	t.Run("csharp", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/C.cs",
			"using Foo.Bar;\nusing X = Foo.Baz;\nclass C { void M() {} }\n")

		got := bindingFor(t, ctx, "X")
		assert.Equal(t, "Foo", got.Specifier)
		assert.Equal(t, "Baz", got.Imported)

		ns := wildcardBindingFor(t, ctx, "Foo.Bar")
		assert.Empty(t, ns.Local, "a plain using names a namespace, not a member")
	})

	t.Run("rust", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/main.rs",
			"use x::y as z;\nuse a::{b, d as e};\nfn main() {}\n")

		got := bindingFor(t, ctx, "z")
		assert.Equal(t, "x", got.Specifier)
		assert.Equal(t, "y", got.Imported)

		nested := bindingFor(t, ctx, "e")
		assert.Equal(t, "a", nested.Specifier)
		assert.Equal(t, "d", nested.Imported)

		plain := bindingFor(t, ctx, "b")
		assert.Equal(t, "a", plain.Specifier)
	})

	t.Run("php", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/p.php",
			"<?php\nnamespace App;\nuse Foo\\Bar;\nuse Foo\\Baz as Qux;\nclass P { function m() {} }\n")

		got := bindingFor(t, ctx, "Qux")
		assert.Equal(t, "Foo", got.Specifier)
		assert.Equal(t, "Baz", got.Imported)

		plain := bindingFor(t, ctx, "Bar")
		assert.Equal(t, "Foo", plain.Specifier)
	})

	t.Run("python", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "a/m.py",
			"from x.y import a as b\nimport json as j\ndef f():\n    pass\n")

		got := bindingFor(t, ctx, "b")
		assert.Equal(t, "x.y", got.Specifier)
		assert.Equal(t, "a", got.Imported)

		mod := bindingFor(t, ctx, "j")
		assert.Equal(t, "json", mod.Specifier)
	})
}

// wildcardBindingFor returns the binding for a specifier that binds NO local
// name. bindingFor cannot serve this shape: every no-name binding carries the
// same empty Local, so they are told apart by their specifier instead.
func wildcardBindingFor(t *testing.T, ctx ChunkContext, specifier string) ImportBinding {
	t.Helper()
	for _, b := range ctx.ImportBindings {
		if b.Specifier == specifier && b.Local == "" {
			return b
		}
	}
	t.Fatalf("no name-binding-free ImportBinding for %q; got %+v", specifier, ctx.ImportBindings)
	return ImportBinding{}
}

// TestImportCaptureKeepsFrameworkStrings is the characterization guard on the
// OTHER half of what these arms touch. Every arm owns ctx.Imports as well as
// the binding table, and each of those entries becomes an IMPORTS edge and is
// what DetectFrameworks matches on — so an arm that recorded only bindings
// would silently empty a language's framework detection and its import edges.
func TestImportCaptureKeepsFrameworkStrings(t *testing.T) {
	ctx, edges := chunkImportFixture(t, "a/test_m.py",
		"import pytest\nfrom pytest import fixture\ndef test_x():\n    pass\n")

	require.NotEmpty(t, importEdgeTargets(edges), "python must still emit IMPORTS edges")
	assert.Contains(t, ctx.Imports, "pytest",
		"the bare module path is what `s == \"pytest\"` framework rules match")
	assert.Contains(t, ctx.Frameworks, FrameworkPyPyTest)
}
