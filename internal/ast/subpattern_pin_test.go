// SPDX-License-Identifier: Apache-2.0

// subpattern_pin_test.go — the context pin scopes the OUTER pattern only.
//
// A where-leaf sub-pattern compiles to its full union regardless of what the
// caller pinned. This test is the behavioral half of that decision, and it is
// the one test that goes red if a future reader threads the outer pin through
// where_subpattern.go as an obvious tidy-up — which is exactly why the argument
// there is the named subPatternPinNone rather than a bare "".
//
// THE FIXTURE IS THE CASE THAT MAKES THE DECISION NON-OBVIOUS. The outer pattern
// is pinned to the member context; its leaf is `return $X;`, which java compiles
// to a field_declaration under member (a field whose type leaf is the literal
// text "return", matching nothing in real source) and to a return_statement
// under stmt. The premise leg below MEASURES that narrowing rather than
// asserting it from memory, so the reader can see what inheritance would cost:
// the leaf would compile to the field variant alone, match nothing, and the
// whole query would return a silent zero — the exact failure class the union
// exists to eliminate.
//
// THE COUNT IS TWO-SIDED ON PURPOSE. The fixture carries two methods and only
// one of them returns, so the expected answer is 1 rather than 0 or 2. A leaf
// that answered false unconditionally (the inheritance bug) gives 0; a leaf that
// answered true unconditionally gives 2. Neither can pass as 1.

package ast

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// javaPinFixture holds one method that returns and one that does not. Both are
// class members, so both are reachable by the member-pinned outer pattern and
// only the leaf can tell them apart.
const javaPinFixture = `class WithReturn {
  String value() {
    return field;
  }
}
class WithoutReturn {
  void act() {
    helper();
  }
}
`

func TestContextPinDoesNotInheritIntoSubPatterns(t *testing.T) {
	const (
		outerPattern = "$T $N() { $$$B; }"
		leafPattern  = "return $X;"
	)

	t.Run("premise_the_member_pin_would_strand_the_leaf_if_it_were_inherited", func(t *testing.T) {
		// Unpinned, the leaf's own text compiles to both readings.
		union, err := Compile(mustParse(t, leafPattern), treesitter.LangJava, "")
		require.NoError(t, err)
		defer union.Close()
		kinds := map[string]bool{}
		for _, v := range union.Variants {
			kinds[v.RootKind] = true
		}
		require.True(t, kinds["return_statement"], "the unpinned leaf must reach the statement reading")
		require.True(t, kinds["field_declaration"], "and the member reading, or this premise measures nothing")

		// Pinned to member, only the field reading survives — which is the
		// variant that matches no real return statement.
		pinned, err := Compile(mustParse(t, leafPattern), treesitter.LangJava, contextMember)
		require.NoError(t, err)
		defer pinned.Close()
		require.Len(t, pinned.Variants, 1)
		require.Equal(t, "field_declaration", pinned.Variants[0].RootKind,
			"inheriting the pin would compile the leaf to this variant alone")
	})

	t.Run("a_member_pinned_outer_pattern_still_matches_through_an_unpinned_leaf", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"Sample.java": javaPinFixture})

		cp, err := Compile(mustParse(t, outerPattern), treesitter.LangJava, contextMember)
		require.NoError(t, err)
		defer cp.Close()

		// The known-positive control on the walk: without the leaf the pinned
		// outer pattern reaches BOTH methods, so a later count of 1 is the
		// leaf discriminating rather than the walk finding less.
		all, _, err := Match(context.Background(), dir, treesitter.LangJava, cp, nil, Scope{})
		require.NoError(t, err)
		require.Len(t, all, 2, "the pinned outer pattern must reach both class members before filtering")

		where, err := ParseWhere([]byte(`{"contains_pattern":{"of":"$match","pattern":"return $X;"}}`))
		require.NoError(t, err)

		filtered, _, err := Match(context.Background(), dir, treesitter.LangJava, cp, where, Scope{})
		require.NoError(t, err)
		require.Len(t, filtered, 1,
			"the leaf must compile to its full union under a member-pinned outer pattern; "+
				"0 means the pin was inherited and stranded the leaf on the field_declaration variant")
		require.Equal(t, "value", filtered[0].Captures["N"].Text,
			"the surviving match must be the method that actually returns")
		require.Equal(t, contextMember, filtered[0].CompiledContexts[0],
			"and the outer match must still come from the pinned context")
	})
}
