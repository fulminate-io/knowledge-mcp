// SPDX-License-Identifier: Apache-2.0

// count_parity_test.go — the correctness gate for the body-free count path.
// COUNT CORRECTNESS IS SACRED: a light match that miscounts is worse than a
// slow one. Match (full RawMatch + absorbMatch dedup) is the independent
// oracle; Count (light {span,kind} + absorbLightMatch) must reproduce it
// exactly — Total, ByFile, and ByKind including the empty-string kind a
// placeholder root binds — across placeholder roots, a multi-variant pattern
// binding two distinct kinds, a where-filtered run, and the same-span variant
// collapse the dedup exists for.

package ast

import (
	"context"
	"sync"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// oracleTally builds the count oracle from a Match result set the way Count's
// tally would: len is the total, and the two maps count file and CompiledKind
// exactly as recordLightCountTally counts relPath and lightMatch.kind.
func oracleTally(raws []RawMatch) (int, map[string]int, map[string]int) {
	byFile := map[string]int{}
	byKind := map[string]int{}
	for _, r := range raws {
		byFile[r.FilePath]++
		byKind[r.CompiledKind]++
	}
	return len(raws), byFile, byKind
}

// assertCountMatchesOracle runs Match then Count over the SAME dir / pattern /
// where / scope and asserts the Count tally reproduces the Match-derived oracle
// field for field. Returns the Match result set so a caller can add
// known-positive assertions (that the config was not vacuous).
func assertCountMatchesOracle(t *testing.T, dir string, lang treesitter.Language, cp *CompiledPattern, where *WhereNode, label string) []RawMatch {
	t.Helper()
	raws, _, merr := Match(context.Background(), dir, lang, cp, where, Scope{})
	require.NoError(t, merr, "%s: Match", label)
	ct, _, cerr := Count(context.Background(), dir, lang, cp, where, Scope{})
	require.NoError(t, cerr, "%s: Count", label)

	refTotal, refByFile, refByKind := oracleTally(raws)
	assert.Equal(t, refTotal, ct.Total, "%s: Total", label)
	assert.Equal(t, refByFile, ct.ByFile, "%s: ByFile", label)
	assert.Equal(t, refByKind, ct.ByKind, "%s: ByKind (incl empty-string kind)", label)
	return raws
}

// collectFileLightCounts is the count-path twin of collectFileMatches: it runs
// one compiled pattern's variants over a single parsed source through the real
// per-file count path (matchContext.collectCounts), without the worker pool. It
// exists for the same reason collectFileMatches does — Match/Count RE-COMPILE
// from cp.Source per worker, so a hand-assembled multi-variant set (the dedupe
// fixture) can only be exercised through this direct path.
func collectFileLightCounts(t *testing.T, cp *CompiledPattern, lang treesitter.Language, src []byte) []lightMatch {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), src, lang)
	require.NoError(t, err)
	defer tree.Close()

	cache := map[string][]patternVariant{}
	cacheMu := &sync.Mutex{}
	defer closeSubPatternCache(cache, cacheMu)

	mc := matchContext{
		cp:         cp,
		relPath:    "fixture",
		src:        src,
		outerScope: newOuterScope(lang, cache, cacheMu),
		caps:       newCaptures(),
		nodes:      map[string]*sitter.Node{},
	}
	got, err := mc.collectCounts(context.Background(), tree.RootNode())
	require.NoError(t, err)
	return got
}

func TestCount_LightMatchParity(t *testing.T) {
	// (a) placeholder root: `$_` binds the empty-string CompiledKind, so the
	// parity must survive the empty ByKind key. Also the largest total.
	t.Run("placeholder_root_empty_kind", func(t *testing.T) {
		dir := benchCountCorpus(t)
		lang := treesitter.Language("go")
		cp, err := Compile(mustParse(t, "$_"), lang, "")
		require.NoError(t, err)
		defer cp.Close()

		raws := assertCountMatchesOracle(t, dir, lang, cp, nil, "placeholder_root")
		_, _, refByKind := oracleTally(raws)
		require.Positive(t, refByKind[""], "placeholder root must bind the empty kind or this case is vacuous")
	})

	// (b) multi-variant, two distinct kinds: `$T $N = $V;` in Java is
	// grammatical as a class field AND as a method-body local, so it compiles
	// to two query-driven variants binding two DIFFERENT RootKinds at two
	// distinct spans. This drives collectCounts across multiple variants and a
	// ByKind carrying two non-empty keys — the case that would expose a light
	// path that dropped a variant or mis-stamped a kind.
	t.Run("multi_variant_two_kinds", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"Sample.java": javaFieldAndLocal})
		cp, err := Compile(mustParse(t, "$T $N = $V;"), treesitter.LangJava, "")
		require.NoError(t, err)
		defer cp.Close()

		raws := assertCountMatchesOracle(t, dir, treesitter.LangJava, cp, nil, "java_field_and_local")
		_, _, refByKind := oracleTally(raws)
		require.Equal(t, 1, refByKind["field_declaration"], "the class field must be counted")
		require.Equal(t, 1, refByKind["local_variable_declaration"], "the method-body local must be counted")
	})

	// (c) where-clause run AND its nil-where control: the where!=nil branch of
	// tryMatchLight (matchTreeWithNodes + evalWhere) must count exactly the
	// filtered set. The regex keeps only the `x` receiver, so where drops one of
	// two defer sites — the nil-where control proves the filter is doing work.
	t.Run("where_clause_filters", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": `package main
func A() { defer x.Close() }
func B() { defer y.Close() }
`})
		lang := treesitter.Language("go")
		cp, err := Compile(mustParse(t, "defer $X.Close()"), lang, "")
		require.NoError(t, err)
		defer cp.Close()

		nilRaws := assertCountMatchesOracle(t, dir, lang, cp, nil, "defer_nil_where")
		require.Len(t, nilRaws, 2, "both defer sites match without a filter (known-positive control)")

		where, err := ParseWhere([]byte(`{"matches":{"of":"X","regex":"^x$"}}`))
		require.NoError(t, err)
		whereRaws := assertCountMatchesOracle(t, dir, lang, cp, where, "defer_where_x_only")
		require.Len(t, whereRaws, 1, "the regex must drop the y receiver, so where counts one")
	})

	// (d) same-span variant collapse: two variants finding the SAME outer span
	// must collapse to one count, exactly as Match collapses them to one
	// RawMatch (an unmerged union would double the count and, on replace, refuse
	// the file). Exercised through the direct per-file path because Match/Count
	// re-compile from Source and would discard the hand-doubled variant set.
	t.Run("same_span_dedup_collapse", func(t *testing.T) {
		src := []byte(`package sample
func A() { defer x.Close() }
func B() { defer y.Close() }
`)
		lang := treesitter.LangGo
		single, err := Compile(mustParse(t, "defer $X.Close()"), lang, "")
		require.NoError(t, err)
		defer single.Close()
		require.Len(t, single.Variants, 1, "the dedupe baseline needs one variant")

		baseRaws := collectFileMatches(t, single, lang, src)
		require.Len(t, baseRaws, 2, "known-positive: the source carries two defer sites")

		second, err := Compile(mustParse(t, "defer $X.Close()"), lang, "")
		require.NoError(t, err)
		defer second.Close()
		doubled := &CompiledPattern{
			Source:   single.Source,
			Variants: append(append([]compiledVariant{}, single.Variants...), second.Variants...),
		}
		require.Len(t, doubled.Variants, 2)

		gotRaws := collectFileMatches(t, doubled, lang, src)
		gotLights := collectFileLightCounts(t, doubled, lang, src)

		// The naive union of two variants over two sites would be four; the
		// collapse takes both sides back to two. Count must collapse with Match.
		require.Len(t, gotRaws, 2, "Match must collapse the doubled variant to two")
		require.Len(t, gotLights, 2, "Count must collapse the doubled variant to two, not four")

		refTotal, _, refByKind := oracleTally(gotRaws)
		lightByKind := map[string]int{}
		for _, lm := range gotLights {
			lightByKind[lm.kind]++
		}
		assert.Len(t, gotLights, refTotal, "light total must equal the deduped Match total")
		assert.Equal(t, refByKind, lightByKind, "light ByKind must equal the deduped Match ByKind")
		assert.Equal(t, map[string]int{"defer_statement": 2}, lightByKind, "both collapsed matches keep the defer_statement kind")
	})
}
