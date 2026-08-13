// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const csharpFileScopedSrc = `namespace App.FileScoped;

public class Other
{
    public void N() {}
}

public struct Pt
{
    public void X() {}
}
`

const csharpBlockSrc = `namespace App.Models
{
    public class User
    {
        public string GetName() { return "n"; }
    }
}
`

const phpSemicolonSrc = `<?php
namespace App\Models;
class User { function getName() {} }
function helper() {}
`

const phpBracedNamespaceSrc = `<?php
namespace App\Braced {
  class Inner { function m() {} }
}
`

const phpNoNamespaceSrc = `<?php
class Loose { function m() {} }
`

const goPackageSrc = `package svc

func Open() error { return nil }
`

// TestCsharpFileScopedNamespace covers the form the .NET 6+ templates default
// to. Its namespace is a distinct node kind, so before it had a pattern of its
// own it was not chunked at all.
func TestCsharpFileScopedNamespace(t *testing.T) {
	result := chunkFile(t, "pkg/f.cs", csharpFileScopedSrc)

	assert.Equal(t, [][2]string{
		{"file_scoped_namespace_declaration", "App.FileScoped"},
		{"class_declaration", "Other"},
		{"method_declaration", "N"},
		{"struct_declaration", "Pt"},
		{"method_declaration", "X"},
	}, chunkKindNames(result))

	// THE TYPES BELOW IT STAY UNPARENTED, and that is the point rather than a
	// gap in the fixture: the namespace is their SIBLING, so no upward walk
	// reaches it. Their methods still resolve their own containers, which is
	// the known-positive proving the ascent runs at all on this file.
	parents := map[string]string{}
	for _, c := range result.Chunks {
		parents[c.Name] = c.ParentName
	}
	assert.Empty(t, parents["Other"], "a file-scoped namespace never parents its types")
	assert.Empty(t, parents["Pt"], "a file-scoped namespace never parents its types")
	assert.Equal(t, "Other", parents["N"])
	assert.Equal(t, "Pt", parents["X"])
}

// TestDeclaredFileNamespace covers the symbol namespace a file takes. The two
// sibling forms name their own namespace better than the parent directory does,
// because two files of one namespace routinely live in different directories
// and resolution keyed on the directory cannot see they are the same namespace.
// The braced, block, namespace-less and Go rows are the controls that the
// override is narrow.
func TestDeclaredFileNamespace(t *testing.T) {
	packageNameOf := func(t *testing.T, path, src string) string {
		t.Helper()
		result := chunkFile(t, path, src)
		require.NotEmpty(t, result.Chunks, "fixture must chunk, or the assertion below is vacuous")
		return result.Chunks[0].Context.PackageName
	}

	t.Run("php_semicolon", func(t *testing.T) {
		// The declared namespace replaces the directory. Backslashes need no
		// treatment — no separator in edge resolution is a backslash.
		assert.Equal(t, `php:App\Models`, packageNameOf(t, "pkg/s.php", phpSemicolonSrc))
	})

	t.Run("php_braced", func(t *testing.T) {
		// CONTROL: the braced form is a true ancestor and the enclosing-scope
		// ascent already qualifies its members, so reading it here as well
		// would qualify the same declaration twice.
		assert.Equal(t, "php:pkg", packageNameOf(t, "pkg/b.php", phpBracedNamespaceSrc))
	})

	t.Run("php_none", func(t *testing.T) {
		assert.Equal(t, "php:pkg", packageNameOf(t, "pkg/n.php", phpNoNamespaceSrc))
	})

	t.Run("csharp_filescoped", func(t *testing.T) {
		// The dot is sanitized to an underscore: edge resolution reads
		// everything before the FIRST dot as the namespace token, so an
		// unsanitized "App.FileScoped" would be split in half.
		assert.Equal(t, "csharp:App_FileScoped", packageNameOf(t, "pkg/f.cs", csharpFileScopedSrc))
	})

	t.Run("csharp_block", func(t *testing.T) {
		// CONTROL: block form, same reason as php_braced.
		assert.Equal(t, "csharp:pkg", packageNameOf(t, "pkg/b.cs", csharpBlockSrc))
	})

	t.Run("go_control", func(t *testing.T) {
		// CONTROL: Go is untouched by construction — the new scan is gated on
		// the language and the package-clause scan is left where it was.
		assert.Equal(t, "svc", packageNameOf(t, "pkg/svc.go", goPackageSrc))
	})

	// A namespace token may never contain a dot or a colon beyond the single
	// language separator, or edge resolution splits it in the wrong place.
	for _, ns := range []string{
		packageNameOf(t, "pkg/s.php", phpSemicolonSrc),
		packageNameOf(t, "pkg/f.cs", csharpFileScopedSrc),
	} {
		assert.NotContains(t, ns, ".", "namespace %q must carry no dot", ns)
		assert.Equal(t, 1, countColons(ns), "namespace %q must carry exactly one language separator", ns)
	}
}

func countColons(s string) int {
	n := 0
	for i := range len(s) {
		if s[i] == ':' {
			n++
		}
	}
	return n
}
