// SPDX-License-Identifier: Apache-2.0

// member_narrowing_test.go — the grammar-derived member-keyword narrowing.
//
// A statement keyword that is ALSO a legal method name — javascript `if`,
// followed by a parameter-shaped parenthesis and a braced body — compiles to a
// member variant (method_definition) whose NAME is the keyword, alongside the
// if_statement variant every caller meant. The narrowing drops that member
// reading when another surviving variant reads the same bytes as an anonymous
// keyword token, keeps it behind an explicit context:"member" pin, and never
// fires on Java's field_declaration (whose keyword lands in the type position,
// not the root's name field) so the landed two-variant contract stays green.
//
// FIVE subtests exactly — the count is pinned by a criterion.

package ast

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// compileForNarrowing parses + compiles under lang with the given pin and
// registers Close. It returns the CompiledPattern so a subtest can read both
// Describe (kept) and DescribeNarrowed (dropped).
func compileForNarrowing(t *testing.T, lang treesitter.Language, source, pin string) *CompiledPattern {
	t.Helper()
	pat, err := Parse(source)
	require.NoErrorf(t, err, "pattern %q must parse", source)
	cp, err := Compile(pat, lang, pin)
	require.NoErrorf(t, err, "pattern %q must compile under %s (pin %q)", source, lang, pin)
	t.Cleanup(cp.Close)
	return cp
}

// hasMemberContext reports whether any described variant carries the member
// context.
func hasMemberContext(vs []CompiledVariant) bool {
	for _, v := range vs {
		if slices.Contains(v.Contexts, contextMember) {
			return true
		}
	}
	return false
}

func TestMemberKeywordNarrowing(t *testing.T) {
	const jsIf = "if ($C) { $$$B }"

	t.Run("js_if_drops_the_member_variant", func(t *testing.T) {
		cp := compileForNarrowing(t, treesitter.LangJavaScript, jsIf, "")
		compiled := cp.Describe()
		assert.False(t, hasMemberContext(compiled),
			"the member (method_definition) reading of `if` must be dropped from the kept union; got %+v", compiled)
		narrowed := cp.DescribeNarrowed()
		require.Len(t, narrowed, 1, "exactly one member variant should be narrowed away")
		assert.Contains(t, narrowed[0].Contexts, contextMember, "the narrowed entry is the member reading")
	})

	t.Run("js_if_pinned_member_keeps_it", func(t *testing.T) {
		// The escape hatch: an explicit member pin asks for the member reading —
		// legal for a class member genuinely named `if` — so the filter is skipped.
		cp := compileForNarrowing(t, treesitter.LangJavaScript, jsIf, contextMember)
		assert.True(t, hasMemberContext(cp.Describe()),
			"context:\"member\" must keep the member variant the unpinned compile drops")
		assert.Empty(t, cp.DescribeNarrowed(), "a member pin narrows nothing — it wants the member reading")
	})

	t.Run("java_return_keeps_both_variants", func(t *testing.T) {
		// Java `return $X;` is a return_statement (stmt) AND a field_declaration
		// (member) whose keyword `return` lands in the TYPE position, not the
		// root's name field, so the name-field-scoped rule never fires here.
		cp := compileForNarrowing(t, treesitter.LangJava, "return $X;", "")
		assert.Empty(t, cp.DescribeNarrowed(), "the member field_declaration must NOT be narrowed — its name is not the keyword")
		assert.True(t, hasMemberContext(cp.Describe()), "the member variant must survive so the landed two-variant contract holds")
	})

	t.Run("ts_class_member_pattern_unaffected", func(t *testing.T) {
		// A genuine member pattern compiles to ONE variant, so there is no second
		// variant to compare against and the rule cannot fire by construction.
		cp := compileForNarrowing(t, treesitter.LangTypeScript, "private readonly $N: $T;", "")
		assert.Empty(t, cp.DescribeNarrowed(), "a legitimate member pattern must not be narrowed")
		assert.True(t, hasMemberContext(cp.Describe()), "the member reading is the whole point of this pattern")
	})

	t.Run("narrowing_reason_is_caller_visible", func(t *testing.T) {
		// The SUCCESS path, where every real caller lives: an unpinned compile
		// must carry the narrowed disclosure with a non-empty reason naming both
		// the member context and the pin remedy, while the surviving compiled
		// entries carry no reason.
		cp := compileForNarrowing(t, treesitter.LangJavaScript, jsIf, "")
		narrowed := cp.DescribeNarrowed()
		require.NotEmpty(t, narrowed, "the narrowed disclosure must reach the caller on the success path")
		assert.NotEmpty(t, narrowed[0].Reason, "the narrowed entry must carry a reason")
		assert.Contains(t, narrowed[0].Reason, "member", "the reason names the member reading")
		assert.Contains(t, narrowed[0].Reason, "context", "the reason names the context pin remedy")
		for _, v := range cp.Describe() {
			assert.Empty(t, v.Reason, "a surviving compiled entry has nothing to explain and must carry no reason")
		}
	})
}
