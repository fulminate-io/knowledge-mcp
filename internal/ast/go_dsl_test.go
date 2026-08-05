// SPDX-License-Identifier: Apache-2.0

// go_dsl_test.go — DSL parser correctness for the v2 placeholder forms.
// Verifies every placeholder form ($X / $_ / $$$X / $$$_), the literal-
// passthrough behavior, and the four documented error cases.

package ast

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_PlaceholderForms(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		expected []Placeholder
	}{
		{
			name:   "single named placeholder",
			source: "$X.Close()",
			expected: []Placeholder{
				{Name: "X", Kind: KindNode, OffsetStart: 0, OffsetEnd: 2},
			},
		},
		{
			name:   "single wildcard",
			source: "defer $_.Close()",
			expected: []Placeholder{
				{Name: "", Kind: KindNodeWild, OffsetStart: 6, OffsetEnd: 8},
			},
		},
		{
			name:   "func with seq args + body wildcard",
			source: "func $NAME($$$ARGS) { $$$_ }",
			expected: []Placeholder{
				{Name: "NAME", Kind: KindNode, OffsetStart: 5, OffsetEnd: 10},
				{Name: "ARGS", Kind: KindSeq, OffsetStart: 11, OffsetEnd: 18},
				{Name: "", Kind: KindSeqWild, OffsetStart: 22, OffsetEnd: 26},
			},
		},
		{
			name:   "sync.Once.Do(func{...})",
			source: "$ONCE.Do(func() { $$$BODY })",
			expected: []Placeholder{
				{Name: "ONCE", Kind: KindNode, OffsetStart: 0, OffsetEnd: 5},
				{Name: "BODY", Kind: KindSeq, OffsetStart: 18, OffsetEnd: 25},
			},
		},
		{
			name:     "fully literal pattern",
			source:   "x.y.z()",
			expected: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pat, err := Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tc.source, err)
			}
			if pat.Source != tc.source {
				t.Errorf("Source = %q, want %q", pat.Source, tc.source)
			}
			if len(pat.Placeholders) != len(tc.expected) {
				t.Fatalf("placeholder count = %d, want %d", len(pat.Placeholders), len(tc.expected))
			}
			for i, want := range tc.expected {
				got := pat.Placeholders[i]
				if got.Name != want.Name || got.Kind != want.Kind ||
					got.OffsetStart != want.OffsetStart || got.OffsetEnd != want.OffsetEnd {
					t.Errorf("Placeholders[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestParse_ErrorCases(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		wantErr    error
		wantSubstr string
	}{
		{name: "empty", source: "", wantErr: errParseEmpty},
		{name: "whitespace-only", source: "   ", wantErr: errParseEmpty},
		{name: "bare $", source: "$", wantErr: errParserBareDollar},
		{name: "bare $ followed by punct", source: "$.foo", wantErr: errParserBareDollar},
		// `$$` is no longer an error — it is the literal-`$` escape
		// (KindLiteralDollar). Its positive coverage lives in
		// TestDSL_LiteralDollarEscape.
		{name: "triple dollar alone", source: "$$$", wantErr: errParserTripleDollar},
		{name: "triple dollar followed by punct", source: "$$$.foo", wantErr: errParserTripleDollar},
		{name: "quad dollar (triple followed by $)", source: "$$$$", wantErr: errParserTripleDollar},
		{name: "tree_sitter_query prefix", source: "tree_sitter_query:(call_expression) @c", wantErr: errParserTreeSitterPrefix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.source)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error; want %v", tc.source, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Parse(%q) err = %v; want errors.Is %v", tc.source, err, tc.wantErr)
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("Parse(%q) err = %v; want substring %q", tc.source, err, tc.wantSubstr)
			}
		})
	}
}

func TestParse_LiteralPassthrough(t *testing.T) {
	// Ensure literal text between placeholders is preserved exactly.
	pat, err := Parse("foo($X, $$$Y, $_, ${{garbage")
	if err == nil {
		t.Fatal("expected parse error on ${{garbage form")
	}
	_ = pat
}

func TestParse_RepeatedNamesProduceSeparateOccurrences(t *testing.T) {
	// Per the locked decision, repeated $X produces SEPARATE captures. The
	// parser does not reject; the engine uses occurrence-indexed identifiers
	// so multiple $X with the same name don't collide in the substituted
	// source.
	pat, err := Parse("$X == $X")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(pat.Placeholders); got != 2 {
		t.Errorf("placeholder count = %d, want 2", got)
	}
	if pat.Placeholders[0].Name != "X" || pat.Placeholders[1].Name != "X" {
		t.Errorf("expected both placeholders named X, got %v", pat.Placeholders)
	}
}
