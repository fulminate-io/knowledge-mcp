// SPDX-License-Identifier: Apache-2.0

// replace_test.go — package-level unit tests for the apply engine
// (interpolateTemplate + buildFileEdits + applyEditsToSource + ApplyReplace).
// Handler-path coverage lives in
// cmd/knowledge/internal/tools/ast_replace_test.go.

package ast

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterpolateTemplate pins that $X substitutes
// caps[X].Text, $$$ARGS interpolates its verbatim span, $$ emits a single
// literal $, a wildcard reference is a usage error, and an unbound name is a
// usage error naming the capture.
func TestInterpolateTemplate(t *testing.T) {
	caps := map[string]Capture{
		"X":    {Text: "db"},
		"ARGS": {Text: "a, b, c"},
	}

	t.Run("named_capture_round_trip", func(t *testing.T) {
		out, err := interpolateTemplate("safeClose($X)", caps)
		require.NoError(t, err)
		assert.Equal(t, "safeClose(db)", out)
	})

	t.Run("sequence_capture_verbatim_span", func(t *testing.T) {
		out, err := interpolateTemplate("f($$$ARGS)", caps)
		require.NoError(t, err)
		assert.Equal(t, "f(a, b, c)", out)
	})

	t.Run("double_dollar_is_literal", func(t *testing.T) {
		out, err := interpolateTemplate("price := $$5 + $X", caps)
		require.NoError(t, err)
		assert.Equal(t, "price := $5 + db", out)
	})

	t.Run("empty_sequence_emits_nothing", func(t *testing.T) {
		out, err := interpolateTemplate("f($$$ARGS)", map[string]Capture{"ARGS": {Text: ""}})
		require.NoError(t, err)
		assert.Equal(t, "f()", out)
	})

	t.Run("node_wildcard_reference_is_usage_error", func(t *testing.T) {
		_, err := interpolateTemplate("x = $_", caps)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wildcard")
	})

	t.Run("seq_wildcard_reference_is_usage_error", func(t *testing.T) {
		_, err := interpolateTemplate("f($$$_)", caps)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wildcard")
	})

	t.Run("unbound_name_is_usage_error", func(t *testing.T) {
		_, err := interpolateTemplate("$MISSING", caps)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MISSING")
	})

	t.Run("triple_dollar_not_swallowed_by_escape", func(t *testing.T) {
		// $$$ARGS must be read as a sequence ref, not $$ (escape) + $ARGS.
		out, err := interpolateTemplate("$$$ARGS", caps)
		require.NoError(t, err)
		assert.Equal(t, "a, b, c", out)
	})
}

// rawMatchAt builds a RawMatch on file with a single outer "match" span
// [start,end). Replacement template "X" interpolates to literal "X" (no
// captures), which is sufficient for the grouping/sort/overlap tests.
func rawMatchAt(file string, start, end uint32) RawMatch {
	return RawMatch{
		FilePath: file,
		Captures: map[string]Capture{"match": {StartByte: start, EndByte: end}},
	}
}

// TestBuildFileEdits pins that it groups by file, DESC-sorts each
// file's edits by Start, refuses files with intersecting/nested ranges, and
// yields disjoint matches in strictly-descending Start order.
func TestBuildFileEdits(t *testing.T) {
	t.Run("disjoint_matches_desc_sorted", func(t *testing.T) {
		matches := []RawMatch{
			rawMatchAt("a.go", 10, 20),
			rawMatchAt("a.go", 40, 50),
			rawMatchAt("a.go", 0, 5),
		}
		edits, refused, err := buildFileEdits(matches, "X")
		require.NoError(t, err)
		assert.Empty(t, refused)
		require.Len(t, edits["a.go"], 3)
		// Strictly descending by Start.
		assert.Equal(t, uint32(40), edits["a.go"][0].Start)
		assert.Equal(t, uint32(10), edits["a.go"][1].Start)
		assert.Equal(t, uint32(0), edits["a.go"][2].Start)
	})

	t.Run("intersecting_matches_refused", func(t *testing.T) {
		matches := []RawMatch{
			rawMatchAt("a.go", 10, 30),
			rawMatchAt("a.go", 25, 40), // 25 < 30 -> intersects
		}
		edits, refused, err := buildFileEdits(matches, "X")
		require.NoError(t, err)
		assert.Equal(t, []string{"a.go"}, refused)
		assert.NotContains(t, edits, "a.go", "refused file must yield no edits")
	})

	t.Run("nested_matches_refused", func(t *testing.T) {
		matches := []RawMatch{
			rawMatchAt("a.go", 10, 50), // outer
			rawMatchAt("a.go", 20, 30), // fully nested inside outer
		}
		_, refused, err := buildFileEdits(matches, "X")
		require.NoError(t, err)
		assert.Equal(t, []string{"a.go"}, refused)
	})

	t.Run("adjacent_disjoint_not_refused", func(t *testing.T) {
		// End == next Start is touching, NOT overlapping (half-open ranges).
		matches := []RawMatch{
			rawMatchAt("a.go", 10, 20),
			rawMatchAt("a.go", 20, 30),
		}
		_, refused, err := buildFileEdits(matches, "X")
		require.NoError(t, err)
		assert.Empty(t, refused, "touching half-open ranges must not be refused")
	})

	t.Run("per_file_isolation", func(t *testing.T) {
		// Overlap in a.go must not poison b.go's clean edits.
		matches := []RawMatch{
			rawMatchAt("a.go", 10, 30),
			rawMatchAt("a.go", 25, 40),
			rawMatchAt("b.go", 0, 5),
		}
		edits, refused, err := buildFileEdits(matches, "X")
		require.NoError(t, err)
		assert.Equal(t, []string{"a.go"}, refused)
		require.Len(t, edits["b.go"], 1)
	})

	t.Run("malformed_template_fails_whole_op", func(t *testing.T) {
		matches := []RawMatch{rawMatchAt("a.go", 10, 20)}
		_, _, err := buildFileEdits(matches, "$_")
		require.Error(t, err, "wildcard reference is a usage error and fails the op")
	})
}

// TestApplyEditsToSource pins that a two-match file rewrites
// byte-identically (offsets valid via right-to-left order), and a template
// producing broken Go makes applyEditsToSource return a re-parse error with
// the rewritten bytes NOT returned.
func TestApplyEditsToSource(t *testing.T) {
	ctx := context.Background()

	t.Run("two_match_right_to_left_splice", func(t *testing.T) {
		src := []byte("package p\n\nfunc f() { a(); b() }\n")
		// Replace a() -> x() and b() -> y(). Both call_expressions.
		aStart := uint32(strings.Index(string(src), "a()"))
		bStart := uint32(strings.Index(string(src), "b()"))
		edits := []fileEdit{
			// DESC-sorted by Start (b first, then a).
			{Start: bStart, End: bStart + 3, Replacement: "y()"},
			{Start: aStart, End: aStart + 3, Replacement: "x()"},
		}
		out, err := applyEditsToSource(ctx, src, edits, "go")
		require.NoError(t, err)
		assert.Equal(t, "package p\n\nfunc f() { x(); y() }\n", string(out))
	})

	t.Run("broken_go_rejected_by_reparse_gate", func(t *testing.T) {
		src := []byte("package p\n\nfunc f() { a() }\n")
		aStart := uint32(strings.Index(string(src), "a()"))
		// Replace a() with an unbalanced-brace fragment.
		edits := []fileEdit{
			{Start: aStart, End: aStart + 3, Replacement: "a({"},
		}
		out, err := applyEditsToSource(ctx, src, edits, "go")
		require.Error(t, err, "unbalanced braces must trip the HasError gate")
		assert.Contains(t, err.Error(), "re-parse")
		assert.Nil(t, out, "rejected rewrite must NOT return bytes")
	})

	t.Run("out_of_bounds_edit_errors", func(t *testing.T) {
		src := []byte("package p\n")
		edits := []fileEdit{{Start: 5, End: 1000, Replacement: "x"}}
		_, err := applyEditsToSource(ctx, src, edits, "go")
		require.Error(t, err)
	})
}

// TestUnifiedDiff pins that a changed file yields a non-empty
// unified diff with ---/+++/@@ hunk headers and -/+ lines; an unchanged file
// yields an empty diff.
func TestUnifiedDiff(t *testing.T) {
	t.Run("changed_file_has_hunk_headers_and_lines", func(t *testing.T) {
		oldSrc := []byte("line1\nline2\nline3\n")
		newSrc := []byte("line1\nCHANGED\nline3\n")
		diff, err := unifiedDiff("pkg/x.go", oldSrc, newSrc)
		require.NoError(t, err)
		assert.NotEmpty(t, diff)
		assert.Contains(t, diff, "--- a/pkg/x.go")
		assert.Contains(t, diff, "+++ b/pkg/x.go")
		assert.Contains(t, diff, "@@")
		assert.Contains(t, diff, "-line2")
		assert.Contains(t, diff, "+CHANGED")
	})

	t.Run("unchanged_file_is_empty", func(t *testing.T) {
		src := []byte("a\nb\nc\n")
		diff, err := unifiedDiff("x.go", src, src)
		require.NoError(t, err)
		assert.Empty(t, diff)
	})
}
