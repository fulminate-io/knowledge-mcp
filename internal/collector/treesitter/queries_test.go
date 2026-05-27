// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compileQuery(t *testing.T, pattern string, lang *sitter.Language) *sitter.Query {
	t.Helper()
	q, err := sitter.NewQuery([]byte(pattern), lang)
	require.NoError(t, err, "query should compile: %s", pattern)
	return q
}

func TestGoQueries(t *testing.T) {
	qs := goQueries()
	lang := registry[LangGo].lang

	t.Run("TopLevel", func(t *testing.T) {
		q := compileQuery(t, qs.TopLevel, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("Calls", func(t *testing.T) {
		q := compileQuery(t, qs.Calls, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("Imports", func(t *testing.T) {
		q := compileQuery(t, qs.Imports, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("TypeRefs", func(t *testing.T) {
		q := compileQuery(t, qs.TypeRefs, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})
}

// TestAllLanguageQueriesCompile validates that every registered language's
// S-expression queries compile successfully against its tree-sitter grammar.
func TestAllLanguageQueriesCompile(t *testing.T) {
	for lang, entry := range registry {
		t.Run(string(lang), func(t *testing.T) {
			qs := entry.Queries()
			require.NotNil(t, qs, "QuerySet should not be nil for %s", lang)

			if qs.TopLevel != "" {
				q, err := sitter.NewQuery([]byte(qs.TopLevel), entry.lang)
				require.NoError(t, err, "%s TopLevel query failed to compile", lang)
				q.Close()
			}

			if qs.Calls != "" {
				q, err := sitter.NewQuery([]byte(qs.Calls), entry.lang)
				require.NoError(t, err, "%s Calls query failed to compile", lang)
				q.Close()
			}

			if qs.Imports != "" {
				q, err := sitter.NewQuery([]byte(qs.Imports), entry.lang)
				require.NoError(t, err, "%s Imports query failed to compile", lang)
				q.Close()
			}

			if qs.TypeRefs != "" {
				q, err := sitter.NewQuery([]byte(qs.TypeRefs), entry.lang)
				require.NoError(t, err, "%s TypeRefs query failed to compile", lang)
				q.Close()
			}
		})
	}
}

func TestTSQueries(t *testing.T) {
	qs := tsQueries()
	lang := registry[LangTypeScript].lang

	t.Run("TopLevel", func(t *testing.T) {
		q := compileQuery(t, qs.TopLevel, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("Calls", func(t *testing.T) {
		q := compileQuery(t, qs.Calls, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("Imports", func(t *testing.T) {
		q := compileQuery(t, qs.Imports, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})

	t.Run("TypeRefs", func(t *testing.T) {
		q := compileQuery(t, qs.TypeRefs, lang)
		defer q.Close()
		assert.Positive(t, q.CaptureCount())
	})
}
