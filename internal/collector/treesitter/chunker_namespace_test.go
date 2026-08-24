// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileNamespace_DerivedFromDir(t *testing.T) {
	cases := []struct {
		name     string
		filePath string
		lang     Language
		want     string
	}{
		{"python in a directory", "pkg/animals.py", LangPython, "python:pkg"},
		{"python at the repo root", "main.py", LangPython, "python:_"},
		{"dots in the directory are sanitized", "src/v1.2/x.py", LangPython, "python:v1_2"},
		{"typescript carries its own prefix", "web/shapes.ts", LangTypeScript, "typescript:web"},
		{"go keeps the bare directory", "pkg/svc.go", LangGo, "pkg"},
		{"go at the repo root", "main.go", LangGo, "_"},
		{"go dots in the directory are sanitized", "src/v1.2/x.go", LangGo, "v1_2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fileNamespace(tc.filePath, tc.lang)
			assert.Equal(t, tc.want, got)
			// A dot would be read as the namespace/symbol separator by
			// parser/edges.go and split the token in half.
			assert.NotContains(t, got, ".", "namespace must never contain a dot")
		})
	}

	// The language prefix is what keeps a Go package name and a same-named
	// directory holding another language in disjoint keyspaces.
	assert.NotEqual(t, fileNamespace("store/x.go", LangGo), fileNamespace("store/x.py", LangPython))
}

func TestClassLikeTypes_ExcludesGoNodeKinds(t *testing.T) {
	// Present: the kinds the parent-to-member edge depends on, each named
	// with the LANGUAGE that owns it — the admission is per (language, kind),
	// so a bare kind is no longer a question the table can answer.
	for _, tc := range []struct {
		lang Language
		kind string
	}{
		{LangRuby, "class"},
		{LangRuby, "module"},
		{LangPython, "class_definition"},
		{LangTypeScript, "class_declaration"},
	} {
		assert.True(t, classLikeByLang[tc.lang][tc.kind],
			"classLikeByLang[%q] must hold %q", tc.lang, tc.kind)
	}

	// Absent: Go's containers, in EVERY language's row. Their exclusion is
	// what makes Go unchanged by construction rather than by measurement, and
	// checking every row is what keeps a future admission of one of these
	// kinds under some other language from reaching Go's declarations.
	for _, lang := range RegisteredLanguages() {
		for _, kind := range []string{"type_declaration", "type_spec", "struct_type", "interface_type"} {
			assert.False(t, classLikeByLang[lang][kind],
				"classLikeByLang[%q] must not hold Go's %q", lang, kind)
		}

		// method_definition and method_declaration are function-like, not
		// class-like, and are shared with Go — moving either would change Go.
		for _, kind := range []string{"method_definition", "method_declaration"} {
			assert.False(t, classLikeByLang[lang][kind],
				"%q belongs in functionLikeTypes only, but classLikeByLang[%q] holds it", kind, lang)
		}
	}
	for _, kind := range []string{"method_definition", "method_declaration"} {
		assert.True(t, functionLikeTypes[kind], "%q must stay function-like", kind)
	}
}

func TestChunkPython_ClassMethodParentName(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	src := []byte(`class Animal:
    def speak(self):
        return "..."

class Dog:
    def speak(self):
        return "woof"

def speak_all():
    return "many"
`)
	result, err := chunker.ChunkFile(context.Background(), "pkg/animals.py", src)
	require.NoError(t, err)

	parents := map[string][]string{}
	for _, c := range result.Chunks {
		if c.ChunkType == "function_definition" {
			parents[c.Name] = append(parents[c.Name], c.ParentName)
			// The chunker never suffixes a Name; the hash suffix is
			// DeduplicateChunks' post-hoc rename, which distinct parents avoid.
			assert.NotContains(t, c.Name, "#", "chunk name %q carries a hash suffix", c.Name)
		}
	}

	sort.Strings(parents["speak"])
	assert.Equal(t, []string{"Animal", "Dog"}, parents["speak"],
		"each class's speak takes its own class as parent")
	// Control: a module-level def has no enclosing scope, so it stays empty —
	// the ascent did not start returning a parent for everything.
	assert.Equal(t, []string{""}, parents["speak_all"])
}

func TestFileContext_GoPackageWinsOverDir(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	// The package clause (svc) differs from the directory (pkg), so a
	// namespace derived from the path instead of the clause is falsifiable.
	goSrc := []byte(`package svc

func Open() error {
	return nil
}
`)
	goResult, err := chunker.ChunkFile(context.Background(), "pkg/svc.go", goSrc)
	require.NoError(t, err)
	goChunks := filterChunks(goResult.Chunks, "function_declaration")
	require.NotEmpty(t, goChunks)
	assert.Equal(t, "svc", goChunks[0].Context.PackageName)

	// Python has no package clause, so the derived namespace stands.
	pySrc := []byte(`def speak():
    return "woof"
`)
	pyResult, err := chunker.ChunkFile(context.Background(), "pkg/animals.py", pySrc)
	require.NoError(t, err)
	pyChunks := filterChunks(pyResult.Chunks, "function_definition")
	require.NotEmpty(t, pyChunks)
	assert.Equal(t, "python:pkg", pyChunks[0].Context.PackageName)

	// Both are non-empty: the recordSymbol gate at parser/populate.go early-returns
	// on an empty namespace, which is what dropped every non-Go edge.
	for _, ns := range []string{goChunks[0].Context.PackageName, pyChunks[0].Context.PackageName} {
		assert.NotEmpty(t, ns)
		assert.NotContains(t, ns, ".")
	}
}
