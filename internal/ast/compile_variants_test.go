// SPDX-License-Identifier: Apache-2.0

// compile_variants_test.go — the union compile and its three hosting rules.
//
// Each subtest here pins one measured case, and the cases are chosen so that
// an implementation satisfying one of them naively fails another:
//
//   - java_return_yields_member_and_stmt — the union itself. Under a
//     first-wrapper-wins cascade this pattern produces ONE candidate (a field
//     declaration whose type leaf is the literal `return`) and matches nothing.
//   - hosting_rejects_wrapper_bytes — rule 1's rejection half, with a
//     known-positive control proving the wrapper PARSES and is turned away by
//     hosting rather than by a parse failure.
//   - absorbs_trailing_anonymous — rule 2. Without it the class-member wrapper
//     is undeliverable, because tree-sitter-typescript puts the member's `;` in
//     the class_body list rather than in the member node.
//   - multi_sibling_* — rule 3. A bare sibling sequence has no enclosing
//     construct, so its root is the wrapper's own block; rules 1 and 2 both
//     decline it and rules 1+2 alone would turn a pattern that finds real sites
//     in this repo into a hard compile error.
//   - compile_failure_names_every_wrapper_and_reason — the genuine-
//     inexpressibility message, reached through a pattern that PARSES and is
//     hosting-rejected, which is the only way to produce the did-not-host
//     reason at all.
//
// THE WRAPPERS ARE REBUILT LOCALLY HERE rather than read from the registry,
// and that stays true now that both are registered. Each case below measures
// ONE wrapper's behavior in isolation — hosting_rejects_wrapper_bytes needs a
// config where the expression wrapper is the only candidate, so that a compile
// error proves the hosting rule turned it away rather than that some other
// wrapper picked up the slack. withWrappers REPLACES the wrapper list, so
// these configs never see what the registry holds.

package ast

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// tsMemberWrapper and csharpExprWrapper are byte-for-byte copies of the
// TypeScript member and C# expression wrappers the registry holds. They are
// declared here so each hosting rule can be measured against one wrapper at a
// time; if either registration's Prefix/Suffix changes, the copy changes with
// it or these cases stop measuring the shipped grammar behavior.
var (
	tsMemberWrapper   = ContextWrapper{Name: "member", Context: contextMember, Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"}
	csharpExprWrapper = ContextWrapper{Name: "expr", Context: contextExpr, Prefix: "class __MetaWrapper__ {\n  void M() {\n    var __metaValue__ = ", Suffix: ";\n  }\n}\n"}
)

// withWrappers copies cfg with an alternative wrapper list, leaving the
// registry untouched.
func withWrappers(cfg LangConfig, wrappers ...ContextWrapper) LangConfig {
	cfg.Wrappers = wrappers
	return cfg
}

// compileVariantsForTest compiles and registers the Close of every variant.
func compileVariantsForTest(t *testing.T, source string, cfg LangConfig) []patternVariant {
	t.Helper()
	variants, narrowed, err := compilePatternVariants(context.Background(), mustParse(t, source), cfg, "")
	require.NoErrorf(t, err, "compilePatternVariants(%q, %s)", source, cfg.Lang)
	t.Cleanup(func() { closeVariants(variants); closeVariants(narrowed) })
	return variants
}

// variantNames renders a candidate set as wrappers[contexts]=rootKind, for
// failure messages that say what the union actually produced. Both lists are
// plural because a deduped variant records every wrapper that reached its tree.
func variantNames(variants []patternVariant) string {
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		out = append(out, strings.Join(v.Wrappers, "+")+"["+strings.Join(v.Contexts, "+")+"]="+v.RootKind)
	}
	return strings.Join(out, " ")
}

func TestCompileVariants_UnionAndHosting(t *testing.T) {
	t.Run("java_return_yields_member_and_stmt", func(t *testing.T) {
		variants := compileVariantsForTest(t, "return $X;", javaLangConfig)
		require.Lenf(t, variants, 2,
			"the union must keep both contexts this pattern is grammatical in; got %s", variantNames(variants))

		require.Equal(t, []string{"decl"}, variants[0].Wrappers, "candidates come back in cfg.Wrappers order")
		require.Equal(t, []string{contextMember}, variants[0].Contexts)
		require.Equal(t, "field_declaration", variants[0].RootKind,
			"the class-body wrapper reads `return $X;` as a field whose type leaf is the literal return")

		// tree-sitter-java's program accepts a bare return statement, so the
		// top-level wrapper compiles the identical tree and is merged in rather
		// than discarded — the second context is a fact about the grammar, not
		// about which wrapper was registered first.
		require.Equal(t, []string{"stmt", "top"}, variants[1].Wrappers)
		require.Equal(t, []string{contextStmt, contextDecl}, variants[1].Contexts)
		require.Equal(t, "return_statement", variants[1].RootKind,
			"the statement context is the one the caller wrote, and the cascade never reached it")
	})

	t.Run("hosting_rejects_wrapper_bytes", func(t *testing.T) {
		cfg := withWrappers(csharpLangConfig, csharpExprWrapper)
		subst, _ := substitutePlaceholders(mustParse(t, "$A = $B;"), cfg)

		// The known-positive control. Without it a rejection by the hosting
		// test is indistinguishable from a wrapper that simply failed to
		// parse, and the subtest would pass against an implementation that
		// never gained a hosting test at all.
		root, tree := parseUnderWrapper(t, cfg.Lang, csharpExprWrapper, subst)
		defer tree.Close()
		require.False(t, root.HasError(),
			"the premise of this case is that the wrapper PARSES: the statement's own `;` becomes an empty statement")

		userStart := uint32(len(csharpExprWrapper.Prefix))
		userEnd := userStart + uint32(len(subst))
		eff, _ := smallestNodeCovering(root, userStart, userEnd, 0)
		require.NotNil(t, eff)
		require.Equal(t, "local_declaration_statement", eff.Type(),
			"the root this wrapper produces is the wrapper's own declaration, not the caller's assignment")

		_, hosted := hostsPattern(root, userStart, userEnd)
		require.False(t, hosted,
			"a root spanning wrapper bytes does not host the pattern; admitting it would pollute the union")

		_, _, err := compilePatternVariants(context.Background(), mustParse(t, "$A = $B;"), cfg, "")
		require.Error(t, err, "with only the expression wrapper registered, this pattern has no hosting context")
	})

	t.Run("absorbs_trailing_anonymous", func(t *testing.T) {
		cfg := withWrappers(tsLangConfig, tsMemberWrapper)
		variants := compileVariantsForTest(t, "private readonly $N: $T;", cfg)
		require.Lenf(t, variants, 1, "one wrapper is registered here; got %s", variantNames(variants))

		v := variants[0]
		require.Equal(t, []string{"member"}, v.Wrappers)
		require.Equal(t, "public_field_definition", v.RootKind,
			"rule 2 roots the pattern at the member, not at the class_body that owns the `;`")
		require.Lenf(t, v.Absorbed, 1, "the trailing `;` is the one token the container owns")
		require.Equal(t, ";", v.Tree.SubstitutedSource[v.Absorbed[0].Start:v.Absorbed[0].End],
			"the absorbed span must be the separator itself — the splice spends it against the template")
		require.Empty(t, v.OutOfSpan, "rule 2 sets aside pattern text, never wrapper text")
	})

	t.Run("multi_sibling_compiles_to_block_root", func(t *testing.T) {
		variants := compileVariantsForTest(t, "$A()\n$B()", goLangConfig)
		require.Lenf(t, variants, 1, "only the statement wrapper hosts a bare sibling pair; got %s", variantNames(variants))
		require.Equal(t, []string{"stmt"}, variants[0].Wrappers)
		require.Equal(t, "block", variants[0].RootKind,
			"a bare sibling sequence has no enclosing construct, so the wrapper's block IS the root")
		require.NotEmpty(t, variants[0].OutOfSpan,
			"rule 3 records the wrapper punctuation inside the root, and it is what keeps the dedupe key honest")
		require.Empty(t, variants[0].Absorbed,
			"out-of-span wrapper text must never be recorded as absorbed pattern text: the splice would delete real template bytes")
	})

	t.Run("multi_sibling_matches_real_sites", func(t *testing.T) {
		cp, err := Compile(mustParse(t, "$A()\n$B()"), treesitter.LangGo, "")
		require.NoError(t, err)
		defer cp.Close()

		// Anchor at the MODULE root (two levels above this package), not the git
		// root: the module can live at a repo subpath or at the repo root, and
		// module-relative prefixes are the one spelling that names these
		// packages in both layouts.
		moduleRoot := filepath.Join("..", "..")
		raws, stats, err := Match(context.Background(), moduleRoot, treesitter.LangGo, cp, nil, Scope{
			PackagePrefixes: []string{
				"internal/collector/cloud/gcp/",
				"internal/pipeline/",
			},
		})
		require.NoError(t, err)
		require.Positive(t, stats.FilesScanned, "the scoped walk must actually reach files")

		files := map[string]int{}
		for _, rm := range raws {
			files[rm.FilePath]++
		}
		// A FLOOR, not an equality: an unrelated new adjacent-call pair
		// elsewhere under these prefixes must not false-fail the gate.
		require.GreaterOrEqual(t, files["internal/collector/cloud/gcp/collector_clients.go"], 1,
			"the known real site in collector_clients.go stopped matching; rule 3 destroyed a working capability")
		require.GreaterOrEqual(t, files["internal/pipeline/pipeline.go"], 1,
			"the known real site in pipeline.go stopped matching; rule 3 destroyed a working capability")

		for _, rm := range raws {
			require.NotEmpty(t, rm.Captures["A"].Text, "capture A must bind the first call")
			require.NotEmpty(t, rm.Captures["B"].Text, "capture B must bind the second call")
		}
	})

	t.Run("dedupe_collapses_identical_candidates", func(t *testing.T) {
		// go `$F($X)` is hosted by the statement AND the expression wrapper,
		// and both compile it to the same call_expression. Two identical
		// candidates would double every match and hand replace.go two
		// identical spans it reads as an overlap, refusing the whole file.
		variants := compileVariantsForTest(t, "$F($X)", goLangConfig)
		require.Lenf(t, variants, 1,
			"structurally identical candidates must collapse to one; got %s", variantNames(variants))
		require.Equal(t, []string{"stmt", "expr"}, variants[0].Wrappers,
			"the earliest hosting wrapper is the one kept, and the collapsed one is MERGED into it rather than discarded")
		require.Equal(t, []string{contextStmt, contextExpr}, variants[0].Contexts,
			"a fragment grammatical in two contexts says so; naming only the first reports registration order as a property of the pattern")
		require.Equal(t, "call_expression", variants[0].RootKind)
		require.NotNil(t, variants[0].Tree.Tree, "the surviving candidate's tree must stay open")
	})

	t.Run("dedupe_key_sees_anonymous_tokens", func(t *testing.T) {
		// The key must not be sitter.Node.String(): that renders NAMED nodes
		// only, and this engine COMPARES anonymous tokens, so two candidates
		// with identical named structure and different anonymous children
		// match differently and must not collapse.
		variants := compileVariantsForTest(t, "$X.Close()", goLangConfig)
		require.NotEmpty(t, variants)
		root := variants[0].Tree.Root

		var b strings.Builder
		serializePattern(root, &b)
		serialized := b.String()
		sexpr := root.String()

		for _, tok := range []string{"(.)", "(()", "())"} {
			require.Containsf(t, serialized, tok,
				"the serialization must carry the anonymous token %q", tok)
			require.NotContainsf(t, sexpr, tok,
				"premise check: sitter.Node.String() renders named nodes only, so %q must be absent from it", tok)
		}
	})

	t.Run("compile_failure_names_every_wrapper_and_reason", func(t *testing.T) {
		classBody, ok := wrapperNamed(csharpLangConfig, "decl")
		require.True(t, ok, "csharp no longer registers the class-body wrapper this case is built on")
		cfg := withWrappers(csharpLangConfig, classBody, csharpExprWrapper)

		_, _, err := compilePatternVariants(context.Background(), mustParse(t, "$A = $B;"), cfg, "")
		require.Error(t, err)
		require.ErrorIs(t, err, errCompileNoWrapper, "the sentinel is what callers match on")
		msg := err.Error()
		t.Logf("compile failure: %s", msg)

		require.Contains(t, msg, "tried decl,expr", "the message keeps naming every wrapper attempted")
		require.Contains(t, msg, "decl[member]:", "every wrapper is reported with its Name and its Context")
		require.Contains(t, msg, "expr[expr]:", "every wrapper is reported with its Name and its Context")
		require.Contains(t, msg, "parse error",
			"the class-body wrapper cannot parse an assignment statement, and that reason must survive")
		require.Contains(t, msg, "parsed but did not host",
			"the expression wrapper PARSES and is turned away by hosting — the reason the union needed a new vocabulary for")
		require.Contains(t, msg, "local_declaration_statement",
			"a did-not-host reason names the root the wrapper produced, so the caller can see what it compiled to")
	})

	t.Run("context_pin_excludes_and_says_so", func(t *testing.T) {
		// The pin's own argument plumbing lands with the union; the caller-
		// facing param is Phase 3. What is pinned here is that a pin narrows
		// the candidate set and that an excluded wrapper is reported as
		// excluded rather than as a parse failure.
		variants, narrowed, err := compilePatternVariants(context.Background(), mustParse(t, "return $X;"), javaLangConfig, contextStmt)
		require.NoError(t, err)
		t.Cleanup(func() { closeVariants(variants); closeVariants(narrowed) })
		require.Lenf(t, variants, 1, "the pin keeps only the stmt context; got %s", variantNames(variants))
		require.Equal(t, "return_statement", variants[0].RootKind)

		_, _, err = compilePatternVariants(context.Background(), mustParse(t, "return $X;"), javaLangConfig, contextExpr)
		require.Error(t, err, "no java wrapper carries the expr context today, so an expr pin has nothing to compile")
		require.Contains(t, err.Error(), `excluded by context pin "expr"`,
			"an excluded wrapper must say it was excluded, not that it failed to parse")
	})
}

// parseUnderWrapper parses substituted text under one wrapper and returns the
// root plus the tree the caller must close. Used by the hosting subtests to
// establish their own premises against the live grammar.
func parseUnderWrapper(t *testing.T, lang treesitter.Language, w ContextWrapper, subst string) (*sitter.Node, *sitter.Tree) {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(w.Prefix+subst+w.Suffix), lang)
	require.NoError(t, err)
	root := tree.RootNode()
	require.NotNil(t, root)
	return root, tree
}
