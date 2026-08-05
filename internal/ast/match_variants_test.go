// SPDX-License-Identifier: Apache-2.0

// match_variants_test.go — what the walk does with a multi-variant pattern:
// stamps every match with the variant that found it, and collapses two
// variants that found the same span into one result.
//
// THE DEDUPE LEG IS THE ONE THAT MATTERS FOR THE WRITE PATH. Two RawMatches
// with identical outer spans reach replace.go::buildFileEdits, which reads
// identical or nested spans as an overlap and refuses the WHOLE FILE. A test
// asserting only the stamp would pass against an implementation that silently
// breaks every replace over a two-context pattern.
//
// The dedupe leg drives the same measurement two ways — one variant, then the
// same variant twice — because a count that is merely small proves nothing: a
// collapsed count and a walk that found nothing are the same number. The
// single-variant run is the known positive the doubled run is compared against.

package ast

import (
	"context"
	"sync"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// javaFieldAndLocal carries the two constructs Java's member and statement
// contexts read `$T $N = $V;` as: a class field and a method-body local. Under
// the retired first-wrapper-wins cascade only the field was reachable.
const javaFieldAndLocal = `class Sample {
  int total = 0;

  void run() {
    int step = 1;
    total = step;
  }
}
`

// collectFileMatches runs one compiled pattern's variants over a single parsed
// source through the real per-file path (matchContext.collectMatches), without
// the worker pool. Match itself RE-COMPILES from cp.Source per worker, so a
// hand-assembled variant set can only be exercised here.
func collectFileMatches(t *testing.T, cp *CompiledPattern, lang treesitter.Language, src []byte) []RawMatch {
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
	got, err := mc.collectMatches(context.Background(), tree.RootNode())
	require.NoError(t, err)
	return got
}

func TestMatchVariants_StampAndDedupe(t *testing.T) {
	t.Run("stamps_name_the_context_each_match_came_from", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"Sample.java": javaFieldAndLocal})

		cp, err := Compile(mustParse(t, "$T $N = $V;"), treesitter.LangJava, "")
		require.NoError(t, err)
		defer cp.Close()
		require.GreaterOrEqual(t, len(cp.Variants), 2,
			"this pattern is grammatical as a field AND as a local; the union must carry both")

		raws, stats, err := Match(context.Background(), dir, treesitter.LangJava, cp, nil, Scope{})
		require.NoError(t, err)
		require.Equal(t, 1, stats.FilesScanned)

		kinds := map[string]string{}
		contexts := map[string][]string{}
		for _, rm := range raws {
			require.NotEmptyf(t, rm.CompiledKind, "match at line %d carries no compiled kind", rm.StartLine)
			require.NotEmptyf(t, rm.CompiledContexts, "match at line %d carries no compiled contexts", rm.StartLine)
			kinds[rm.Captures["N"].Text] = rm.CompiledKind
			contexts[rm.Captures["N"].Text] = rm.CompiledContexts
		}
		require.Equal(t, "field_declaration", kinds["total"],
			"the class field must be stamped with the construct that expressed it")
		require.Equal(t, []string{contextMember}, contexts["total"],
			"only the class-body wrapper produces a field, so its stamp names exactly one context")
		require.Equal(t, "local_variable_declaration", kinds["step"],
			"the method-body local is what the cascade could never reach")
		require.Equal(t, []string{contextStmt, contextDecl}, contexts["step"],
			"java's stmt and top wrappers compile the local identically, so the merged stamp names both")
	})

	t.Run("same_span_matches_from_two_variants_collapse", func(t *testing.T) {
		const src = `package sample

func A() {
	defer x.Close()
}

func B() {
	defer y.Close()
}
`
		single, err := Compile(mustParse(t, "defer $X.Close()"), treesitter.LangGo, "")
		require.NoError(t, err)
		defer single.Close()
		require.Len(t, single.Variants, 1, "the fixture for the dedupe needs a one-variant baseline")

		baseline := collectFileMatches(t, single, treesitter.LangGo, []byte(src))
		require.Len(t, baseline, 2, "the known positive: this source carries two defer sites")

		// The same variant compiled twice. Every node either one matches, the
		// other matches too, at exactly the same outer span — which is the
		// collision the per-file dedupe exists for.
		second, err := Compile(mustParse(t, "defer $X.Close()"), treesitter.LangGo, "")
		require.NoError(t, err)
		doubled := &CompiledPattern{
			Source:   single.Source,
			Variants: append(append([]compiledVariant{}, single.Variants...), second.Variants...),
		}
		defer func() { second.Close() }()
		require.Len(t, doubled.Variants, 2)

		got := collectFileMatches(t, doubled, treesitter.LangGo, []byte(src))
		require.Len(t, got, len(baseline),
			"two variants finding the same spans must collapse to one match each; replace refuses a whole file over a self-overlap")
		for i, rm := range got {
			require.Equal(t, "defer_statement", rm.CompiledKind)
			// The surviving stamp is the EARLIEST candidate's, which is the
			// baseline's — dedupe keeps the first variant in candidate order,
			// never whichever cursor happened to emit the span first.
			require.Equal(t, baseline[i].CompiledContexts, rm.CompiledContexts)
		}
	})

	t.Run("absorbed_spans_seed_the_dropped_budget", func(t *testing.T) {
		// An absorbed token is pattern text the match threw away, so it earns
		// no alignment entry and the splice has to be told about it. The
		// carrier is RawMatch.DroppedSpans, seeded from the variant.
		cfg := withWrappers(tsLangConfig, tsMemberWrapper)
		variants := compileVariantsForTest(t, "private readonly $N: $T;", cfg)
		require.Len(t, variants, 1)
		require.Len(t, variants[0].Absorbed, 1)

		cp := &CompiledPattern{Source: "private readonly $N: $T;"}
		cv := compiledVariant{patternVariant: variants[0]}
		initRootQuery(&cv, treesitter.LangTypeScript)
		cp.Variants = []compiledVariant{cv}

		const src = `class Widget {
  private readonly name: string;
}
`
		got := collectFileMatches(t, cp, treesitter.LangTypeScript, []byte(src))
		require.Len(t, got, 1, "the member pattern must find the class member")
		require.Equal(t, "public_field_definition", got[0].CompiledKind)
		require.Equal(t, []string{contextMember}, got[0].CompiledContexts)
		require.NotEmpty(t, got[0].DroppedSpans,
			"the absorbed separator must reach the write path, or an identity template re-emits it beside a source that already has one")
		require.Equal(t, variants[0].Absorbed[0], got[0].DroppedSpans[0],
			"the seeded span is the variant's absorbed span, in the pattern-side coordinate space the splice reads")
	})
}
