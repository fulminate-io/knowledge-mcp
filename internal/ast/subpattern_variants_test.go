// SPDX-License-Identifier: Apache-2.0

// subpattern_variants_test.go — a where-leaf's sub-pattern gets the same union
// treatment as an outer pattern.
//
// WHY IT MATTERS. Leaving the sub-pattern path on the retired first-wrapper
// cascade would make the same pattern text mean one thing at the top level and
// another inside a where-leaf — a preservation island where a filter silently
// answers a different question from the one the caller asked.
//
// THE TWO LEGS ARE EACH SATISFIABLE BY EXACTLY ONE VARIANT. Java's
// `$T $N = $V;` compiles to a field_declaration under the class-body wrapper
// and to a local_variable_declaration under the statement wrapper. The fixture
// puts a field in one class and a local in another, so the field-only class can
// be reached only by the member variant and the local-only class only by the
// stmt variant. A negative-control class carrying neither must not match, so a
// leaf that answered true unconditionally would fail too.

package ast

import (
	"context"
	"sync"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// The three cells. In the first the only `$T $N = $V;` is a FIELD, in the
// second the only one is a method-body LOCAL, and the third carries neither.
const (
	javaOnlyField = `class OnlyField {
  int total = 0;
}
`
	javaOnlyLocal = `class OnlyLocal {
  void other() {
    int step = 1;
  }
}
`
	javaNeither = `class Neither {
  void nothing() {
    helper();
  }
}
`
)

// classContains evaluates a contains_pattern leaf against the class declaration
// in src, through the real evalSubPattern path (compile cache included) rather
// than through a where-tree wrapped around an outer pattern — Java has no
// class-body sequence spelling that compiles, so the leaf has to be driven
// directly.
func classContains(t *testing.T, src, subPattern string) bool {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(src), treesitter.LangJava)
	require.NoError(t, err)
	defer tree.Close()

	var target *sitter.Node
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		if target == nil && n != nil && n.Type() == "class_declaration" {
			target = n
		}
	})
	require.NotNil(t, target, "the fixture must carry a class declaration to search under")

	cache := map[string][]patternVariant{}
	cacheMu := &sync.Mutex{}
	defer closeSubPatternCache(cache, cacheMu)

	scope := newOuterScope(treesitter.LangJava, cache, cacheMu)
	scope.src = []byte(src)
	scope.captures.byName["$match"] = nodeToCapture(target, scope.src)
	scope.nodeByName["$match"] = target

	ok, err := evalSubPattern(context.Background(),
		&SubPatternLeaf{Of: "$match", Pattern: subPattern}, scope, descendantSearch)
	require.NoError(t, err)
	return ok
}

func TestSubPatternVariants(t *testing.T) {
	t.Run("sub_pattern_compiles_to_the_same_union_as_an_outer_pattern", func(t *testing.T) {
		cache := map[string][]patternVariant{}
		cacheMu := &sync.Mutex{}
		defer closeSubPatternCache(cache, cacheMu)
		scope := newOuterScope(treesitter.LangJava, cache, cacheMu)

		variants, err := getOrCompileSubPattern(context.Background(), scope, "$T $N = $V;")
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(variants), 2,
			"a sub-pattern must inherit the union; one candidate here is the cascade the outer pattern just stopped using")

		kinds := map[string][]string{}
		for _, v := range variants {
			kinds[v.RootKind] = v.Contexts
		}
		require.Equal(t, []string{contextMember}, kinds["field_declaration"])
		require.Equal(t, []string{contextStmt, contextDecl}, kinds["local_variable_declaration"])

		// Cached by source: the second call must not compile again.
		again, err := getOrCompileSubPattern(context.Background(), scope, "$T $N = $V;")
		require.NoError(t, err)
		require.Len(t, again, len(variants))
		require.Same(t, variants[0].Tree, again[0].Tree, "the cache is keyed by sub-pattern source")
	})

	t.Run("either_variant_can_satisfy_the_leaf", func(t *testing.T) {
		const subPattern = "$T $N = $V;"
		require.True(t, classContains(t, javaOnlyField, subPattern),
			"the field is reachable only through the member variant of the sub-pattern")
		require.True(t, classContains(t, javaOnlyLocal, subPattern),
			"the local is reachable only through the stmt variant of the sub-pattern")
		require.False(t, classContains(t, javaNeither, subPattern),
			"the known negative: a class carrying neither construct must not satisfy the leaf")
	})

	t.Run("the_cache_closes_every_variant_it_allocated", func(t *testing.T) {
		// A leaked tree-sitter Tree is invisible to the suite unless something
		// asserts it, and widening the cached value from one tree to a slice is
		// exactly the change that leaves the tail of that slice open.
		variants, narrowed, err := compilePatternVariants(context.Background(), mustParse(t, "$T $N = $V;"), javaLangConfig, "")
		require.NoError(t, err)
		t.Cleanup(func() { closeVariants(narrowed) })
		require.GreaterOrEqual(t, len(variants), 2, "the close leg needs a multi-variant set to be worth running")
		for _, v := range variants {
			require.NotNil(t, v.Tree, "premise: every variant holds an open tree before the close")
		}

		cacheMu := &sync.Mutex{}
		closeSubPatternCache(map[string][]patternVariant{"$T $N = $V;": variants}, cacheMu)

		for i, v := range variants {
			require.Nilf(t, v.Tree, "variant %d (%v) was left open by the cache close", i, v.Wrappers)
		}
	})
}
