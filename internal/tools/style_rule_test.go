// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_test.go covers the shared style-rule vocabulary in style_rule.go:
// the severity ladder, the scope predicate's absent-or-matches disjunction, the
// path-spelling refusals, and the capped render columns.
//
// EVERY TEST HERE NAMES THE PRODUCTION SYMBOL IT OBSERVES. A test that reached
// the asserted value through a locally re-derived copy of the rule would go
// green with the production path broken, which is the failure class that cost a
// sibling ticket three review rounds.
//
// NAMING THE SYMBOL IS NOT THE SAME AS DISCRIMINATING, and the cap assertions
// below are the cautionary half: they compare a capped column against
// styleIndexColumnCap and styleIndexEllipsis, the very constants under test, so
// changing either moved BOTH sides and left them green. The literal-valued
// boundary table that actually pins those two figures lives in kgtypes
// (style_render_test.go), beside the constants themselves; what remains here is
// the arm that proves this package's render still routes through them.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestStyleRuleSeverity_LadderAndRefusal pins requirement 1's severity clause:
// the CHECK contract's four values are admitted and everything else is refused
// naming the value AND the whole ladder — never defaulted.
func TestStyleRuleSeverity_LadderAndRefusal(t *testing.T) {
	for _, ok := range []foundation.Severity{
		foundation.SeverityInfo, foundation.SeverityNotice,
		foundation.SeverityWarning, foundation.SeverityCritical,
	} {
		got, err := styleRuleSeverity("rule list: rules[0]", string(ok))
		require.NoError(t, err, "%q is on the check contract's ladder", ok)
		assert.Equal(t, ok, got)
	}

	// THE PRACTICE SIDE'S OWN OLDER VALUES ARE REFUSED, which is the whole point
	// of putting a style rule on the check scale: `high` and `medium` are carried
	// by 22 of the 24 existing practice/go pattern findings and have no check
	// counterpart, so a shaped rule authored on that scale could not be mirrored
	// into its sister check without a re-mapping step nothing owns.
	for _, bad := range []string{"high", "medium", "", "Warning", "CRITICAL", "fatal"} {
		got, err := styleRuleSeverity("rule list: rules[3] (\"no-bare-http-client\")", bad)
		require.Error(t, err, "severity %q must be refused, not defaulted", bad)
		assert.Empty(t, string(got), "a refused severity returns no value to default with")
		assert.Contains(t, err.Error(), bad, "the refusal must name the offending value")
		assert.Contains(t, err.Error(), "rules[3]", "the refusal must name the caller's own locator")
		for _, rung := range []string{"info", "notice", "warning", "critical"} {
			assert.Contains(t, err.Error(), rung,
				"the refusal must name the WHOLE ladder so the author can pick one")
		}
	}
}

// TestStyleScopeMatches_AbsentOrMatches is the selection × input-class matrix.
//
// THE UNSCOPED ROWS ARE THE CELLS THAT CATCH A PREDICATE IMPLEMENTATION. A
// server-side metadata predicate on the scope keys would exclude every rule that
// carries none of them, and those are the rules that apply everywhere.
func TestStyleScopeMatches_AbsentOrMatches(t *testing.T) {
	unscoped := styleRuleScope{}
	repoOnly := styleRuleScope{Repo: "knowledge", RepoSet: true}
	pathsOnly := styleRuleScope{Paths: []string{"cmd/knowledge"}, PathsSet: true}
	both := styleRuleScope{
		Repo: "knowledge", RepoSet: true,
		Paths: []string{"cmd/knowledge"}, PathsSet: true,
	}

	cases := []struct {
		name  string
		scope styleRuleScope
		repo  string
		paths []string
		want  bool
	}{
		{"unscoped/no selectors", unscoped, "", nil, true},
		{"unscoped/non-matching repo", unscoped, "other-repo", nil, true},
		{"unscoped/non-matching paths", unscoped, "", []string{"totally/elsewhere.go"}, true},
		{"unscoped/both non-matching", unscoped, "other-repo", []string{"totally/elsewhere.go"}, true},

		{"repo-scoped/matching", repoOnly, "knowledge", nil, true},
		{"repo-scoped/differing", repoOnly, "agent", nil, false},
		{"repo-scoped/caller named no repo", repoOnly, "", nil, true},

		{"path-scoped/one path matches", pathsOnly, "", []string{"cmd/knowledge/main.go"}, true},
		{"path-scoped/no path matches", pathsOnly, "", []string{"cmd/knowledge-server/main.go"}, false},
		{"path-scoped/caller named no paths", pathsOnly, "", nil, true},
		{"path-scoped/caller named an empty list", pathsOnly, "", []string{}, true},
		{"path-scoped/one of several matches", pathsOnly, "",
			[]string{"docs/x.md", "cmd/knowledge/main.go"}, true},

		{"both/both match", both, "knowledge", []string{"cmd/knowledge/main.go"}, true},
		{"both/repo differs", both, "agent", []string{"cmd/knowledge/main.go"}, false},
		{"both/no path matches", both, "knowledge", []string{"README.md"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, styleScopeMatches(tc.scope, tc.repo, tc.paths))
		})
	}
}

// TestStyleScopeMatches_SegmentBoundary pins that the path leg matches at
// PATH-SEGMENT boundaries, through the same predicate the corpus walk applies.
//
// A bare strings.HasPrefix passes the first row and fails the second, so the
// sibling row is what makes the first one evidence of anything.
func TestStyleScopeMatches_SegmentBoundary(t *testing.T) {
	scope := styleRuleScope{Paths: []string{"pkg"}, PathsSet: true}

	assert.True(t, styleScopeMatches(scope, "", []string{"pkg/x.go"}),
		"`pkg` must admit a file under pkg/")
	assert.True(t, styleScopeMatches(scope, "", []string{"pkg"}),
		"`pkg` must admit the directory itself")
	assert.False(t, styleScopeMatches(scope, "", []string{"pkgextra/x.go"}),
		"`pkg` must NOT admit the sibling pkgextra — that is a string prefix, not a segment prefix")
}

// TestStyleScopePathRefusal_Spellings covers every path spelling the import
// refuses, and the near-misses it must accept.
func TestStyleScopePathRefusal_Spellings(t *testing.T) {
	for _, good := range []string{
		"cmd", "cmd/knowledge", "cmd/knowledge/internal/tools/style_rule.go",
		"a,b/with-comma.go", "dir with spaces/x.go",
	} {
		assert.Empty(t, styleScopePathRefusal(good), "%q is a legitimate repo-relative path", good)
	}
	for _, bad := range []struct{ path, wants string }{
		{"", "is empty"},
		{"   ", "is empty"},
		{"/abs/path", "absolute"},
		{"..", "`..` segment"},
		{"../sibling", "`..` segment"},
		{"cmd/../etc", "`..` segment"},
		{"cmd/..", "`..` segment"},
		{"./cmd", "walk's repo-relative spelling"},
		{"cmd/", "walk's repo-relative spelling"},
		{"cmd//knowledge", "walk's repo-relative spelling"},
	} {
		refusal := styleScopePathRefusal(bad.path)
		require.NotEmpty(t, refusal, "%q must be refused", bad.path)
		assert.Contains(t, refusal, bad.wants,
			"the refusal for %q must say why, not merely that", bad.path)
	}
}

// TestStyleScopePaths_RoundTripCarriesACommaVerbatim is the encoding's whole
// justification: a repo-relative path may legally contain a comma, so a comma
// join would split one legitimate path into two that do not exist.
func TestStyleScopePaths_RoundTripCarriesACommaVerbatim(t *testing.T) {
	in := []string{"a,b/with-comma.go", "plain/path.go", "line\nbreak.go"}
	encoded, err := styleScopePathsEncode(in)
	require.NoError(t, err)
	out, err := styleScopePathsDecode(encoded)
	require.NoError(t, err)
	assert.Equal(t, in, out, "every path survives the encoding byte for byte")
	assert.Len(t, out, 3, "a three-path scope decodes as three paths, not four")
}

// TestStyleScopeFromMetadata_AbsentIsNotEmpty pins that an ABSENT optional key
// reads as absent rather than as an empty value — the distinction requirement 1
// asks for and the one the whole absent-or-matches rule rests on.
func TestStyleScopeFromMetadata_AbsentIsNotEmpty(t *testing.T) {
	bare, err := styleScopeFromMetadata(map[string]string{})
	require.NoError(t, err)
	assert.False(t, bare.RepoSet, "no style_scope_repo key means no repo scope")
	assert.False(t, bare.PathsSet, "no style_scope_paths key means no path scope")

	scoped, err := styleScopeFromMetadata(map[string]string{
		kgtypes.MetaKeyStyleScopeRepo:  "knowledge",
		kgtypes.MetaKeyStyleScopePaths: `["cmd/knowledge"]`,
	})
	require.NoError(t, err)
	assert.True(t, scoped.RepoSet)
	assert.Equal(t, "knowledge", scoped.Repo)
	assert.Equal(t, []string{"cmd/knowledge"}, scoped.Paths)

	// A MALFORMED ARRAY IS AN ERROR, not an empty scope. Reading it as empty
	// would answer "this rule applies everywhere", which is the widest possible
	// answer to a question the data could not answer.
	_, err = styleScopeFromMetadata(map[string]string{
		kgtypes.MetaKeyStyleScopePaths: "cmd/knowledge,internal",
	})
	require.Error(t, err, "a comma-joined value is not a JSON array and must be refused")
	assert.Contains(t, err.Error(), kgtypes.MetaKeyStyleScopePaths)
}

// TestCapStyleColumn_BoundaryClasses drives the cap at the cap, one under, one
// over, and where the cut falls mid-rune.
func TestCapStyleColumn_BoundaryClasses(t *testing.T) {
	t.Run("one under the cap is untouched", func(t *testing.T) {
		s := strings.Repeat("a", styleIndexColumnCap-1)
		assert.Equal(t, s, capStyleColumn(s))
	})
	t.Run("exactly at the cap is untouched", func(t *testing.T) {
		s := strings.Repeat("a", styleIndexColumnCap)
		assert.Equal(t, s, capStyleColumn(s))
		assert.NotContains(t, capStyleColumn(s), styleIndexEllipsis,
			"a value AT the cap is not a truncation and must not be marked as one")
	})
	t.Run("one over the cap truncates visibly", func(t *testing.T) {
		s := strings.Repeat("a", styleIndexColumnCap+1)
		got := capStyleColumn(s)
		assert.Len(t, got, styleIndexColumnCap, "the capped column is exactly the cap wide")
		assert.True(t, strings.HasSuffix(got, styleIndexEllipsis),
			"a truncation must be VISIBLE, not silent")
	})
	t.Run("a cut falling mid-rune moves back to a boundary", func(t *testing.T) {
		// Three-byte runes, so the naive cut at cap-3 lands inside one.
		s := strings.Repeat("界", styleIndexColumnCap)
		got := capStyleColumn(s)
		assert.LessOrEqual(t, len(got), styleIndexColumnCap, "still bounded in BYTES")
		assert.True(t, strings.HasSuffix(got, styleIndexEllipsis))
		assert.Equal(t, got, strings.ToValidUTF8(got, "�"),
			"the cut must land on a rune boundary — a split rune is invalid UTF-8")
	})
	t.Run("the longest repo-relative path in this tree truncates", func(t *testing.T) {
		// 137 bytes: the longest repo-relative path measured over this
		// repository. One real path already exceeds the column cap, which is why
		// the scope column truncates by design — the full scope is recoverable
		// from the by-id lookup.
		p := strings.Repeat("p", 137)
		require.Len(t, p, 137)
		got := capStyleColumn(p)
		assert.Len(t, got, styleIndexColumnCap)
		assert.True(t, strings.HasSuffix(got, styleIndexEllipsis))
	})
}

// TestStyleScopeColumn_AbsentRendersADash pins that "applies everywhere" and "no
// column" are different things a reader can tell apart.
func TestStyleScopeColumn_AbsentRendersADash(t *testing.T) {
	assert.Equal(t, "-", styleScopeColumn(styleRuleScope{}),
		"a rule with neither scope key renders `-`, never an empty column")
	assert.Equal(t, "repo=knowledge",
		styleScopeColumn(styleRuleScope{Repo: "knowledge", RepoSet: true}))
	assert.Equal(t, "paths=cmd,internal",
		styleScopeColumn(styleRuleScope{Paths: []string{"cmd", "internal"}, PathsSet: true}))
	assert.Equal(t, "repo=knowledge paths=cmd",
		styleScopeColumn(styleRuleScope{
			Repo: "knowledge", RepoSet: true, Paths: []string{"cmd"}, PathsSet: true,
		}), "each half names itself, because a bare value could not say which it was")
}

// TestStyleIndexBound_TheZeroRowRenderIsWithinItsOwnBound closes the input class
// the block-bound helper could not answer for.
//
// THE HELPER RETURNED FALSE FOR ZERO ROWS, and "zero rows" is the FIRST render
// input class this arm defines. A zero-row index is not an empty string: it is
// the one-line empty state, deliberately, so an empty answer stays
// distinguishable from a call that produced no output. Against a budget of
// `rows × styleIndexRowBound` that line had a budget of 0, so the one helper the
// render points at contradicted the render for its own base case — and nothing
// was red, because no assertion had ever asked it.
//
// THE MUTATION: delete the zero-row arm of styleIndexBlockIsWithinBound, or
// raise styleIndexEmpty's wording past styleIndexEmptyBound, and this goes red.
func TestStyleIndexBound_TheZeroRowRenderIsWithinItsOwnBound(t *testing.T) {
	// THE TWO FIGURES AS LITERALS, so this arm pins them instead of comparing
	// each against itself. The sweep that found the block bound checking its own
	// answer key found the same shape here: len(styleIndexEmpty) measured
	// against styleIndexEmptyBound is two constants agreeing, and a reworded
	// empty state with a widened bound satisfies it in silence.
	assert.Equal(t, "No style rules match.", styleIndexEmpty,
		"the zero-row body's exact wording; a reader distinguishes it from no output at all")
	assert.Equal(t, 32, styleIndexEmptyBound,
		"the zero-row bound's documented figure: the line is 21 bytes, declared as 32")

	assert.True(t, styleIndexBlockIsWithinBound(nil, nil),
		"a zero-row index renders %q (%d bytes) and must be within ITS bound (%d), not within a sum over zero rows",
		styleIndexEmpty, len(styleIndexEmpty), styleIndexEmptyBound)
	assert.True(t, styleIndexBlockIsWithinBound([]string{}, []string{}),
		"an empty slice and a nil slice are the same zero-row render")

	assert.LessOrEqual(t, len(styleIndexEmpty), styleIndexEmptyBound,
		"the declared empty-state bound must actually bound the empty-state line")

	// THE KNOWN POSITIVE. Without it, "the bound holds" is satisfied just as well
	// by a helper that returns true for everything — which is the failure mode
	// one arm away from the one being fixed.
	overID := "over"
	over := []string{strings.Repeat("x", styleIndexRowBound(overID)+1)}
	assert.False(t, styleIndexBlockIsWithinBound([]string{overID}, over),
		"a row block over its budget must still report FALSE — otherwise the bound reports nothing")

	// And the ROW bound is unchanged by the zero-row arm: a real row block is
	// still measured against the sum of its rows' own bounds.
	assert.True(t, styleIndexBlockIsWithinBound(
		[]string{overID}, []string{strings.Repeat("x", styleIndexRowBound(overID))}),
		"a row exactly at the per-row bound is within it")
}
