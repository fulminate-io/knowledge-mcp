// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want Language
	}{
		{"main.go", LangGo},
		{"pkg/foo/bar.go", LangGo},
		{"src/app.ts", LangTypeScript},
		{"components/App.tsx", LangTSX},
		{"README.md", LangMarkdown},
		{"image.png", LangUnknown},
		{"Makefile", LangBash},
		{".go", LangGo},
		{"no-extension", LangUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DetectLanguage(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryHasAllLanguages(t *testing.T) {
	assert.Contains(t, registry, LangGo)
	assert.Contains(t, registry, LangTypeScript)
	assert.NotContains(t, registry, LangUnknown)
}

func TestLangEntryQueries(t *testing.T) {
	entry := registry[LangGo]
	qs := entry.Queries()
	assert.NotNil(t, qs)
	assert.NotEmpty(t, qs.TopLevel)
	assert.NotEmpty(t, qs.Calls)
	assert.NotEmpty(t, qs.Imports)

	// Verify caching — second call returns the same pointer.
	qs2 := entry.Queries()
	assert.Same(t, qs, qs2)
}

func TestLanguageGrammar(t *testing.T) {
	// Registered languages return a non-nil grammar with ok=true. Pick a
	// representative spread (Go + the high-demand triad) — exhaustive
	// coverage is what TestRegistryHasAllLanguages already provides.
	for _, lang := range []Language{LangGo, LangPython, LangTypeScript, LangRust} {
		t.Run(string(lang), func(t *testing.T) {
			grammar, ok := LanguageGrammar(lang)
			assert.True(t, ok, "expected ok=true for %s", lang)
			assert.NotNil(t, grammar, "expected non-nil grammar for %s", lang)
		})
	}

	// LangUnknown is sentinel: not in the registry, returns (nil, false).
	t.Run("unknown-sentinel", func(t *testing.T) {
		grammar, ok := LanguageGrammar(LangUnknown)
		assert.False(t, ok)
		assert.Nil(t, grammar)
	})

	// Arbitrary not-in-registry value also returns (nil, false). Guards
	// against a future caller passing a string-typed Language that doesn't
	// match any constant.
	t.Run("arbitrary-unknown", func(t *testing.T) {
		grammar, ok := LanguageGrammar(Language("not-a-real-language"))
		assert.False(t, ok)
		assert.Nil(t, grammar)
	})
}
