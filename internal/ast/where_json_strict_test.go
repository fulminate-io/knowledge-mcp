// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"strings"
	"testing"
)

// TestParseWhere_RejectsUnknownTopLevelKey pins the strict-mode rejection
// surfaced by the Phase 3 sweep. Pre-fix behavior: malformed where-trees
// like {"unknown_field": "..."} parsed cleanly to an empty WhereNode and
// silently returned the unfiltered match set. Strict-mode now rejects
// unknown keys with an error naming the typo.
func TestParseWhere_RejectsUnknownTopLevelKey(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantSub string // substring expected in the error message
	}{
		{
			name:    "bogus top-level key",
			json:    `{"unknown_leaf_field": "function_declaration"}`,
			wantSub: `unknown field "unknown_leaf_field"`,
		},
		{
			name:    "missing kind wrapper",
			json:    `{"of": "X", "is": "function_declaration"}`,
			wantSub: `unknown field "of"`,
		},
		{
			name:    "missing kind wrapper inside all",
			json:    `{"all": [{"of": "X", "is": "function_declaration"}]}`,
			wantSub: `unknown field "of"`,
		},
		{
			name:    "typo on leaf inner field",
			json:    `{"kind": {"of": "X", "is": "function_declaration", "extra_field_typo": "boom"}}`,
			wantSub: `unknown field "extra_field_typo"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWhere([]byte(tc.json))
			if err == nil {
				t.Fatalf("ParseWhere(%q) returned nil error; expected unknown-field rejection", tc.json)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain expected substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestParseWhere_AcceptsValidShapes confirms strict mode does not
// regress the documented happy-path payloads.
func TestParseWhere_AcceptsValidShapes(t *testing.T) {
	cases := []string{
		`{}`,
		`null`,
		`{"all": [{"kind": {"of": "X", "is": "function_declaration"}}]}`,
		`{"any": [{"matches": {"of": "X", "regex": "^Test"}}, {"equals": {"of": "X", "value": "main"}}]}`,
		`{"not": {"kind": {"of": "X", "is": ["call_expression", "selector_expression"]}}}`,
		`{"kind": {"of": "X", "is": ["function_declaration", "method_declaration"]}}`,
		`{"same_node": {"captures": ["A", "B"]}}`,
		`{"same_text": {"captures": ["A", "$outer.B"]}}`,
		`{"inside_pattern": {"of": "X", "pattern": "func $_($_) {$$$_}"}}`,
		`{"contains_pattern": {"of": "X", "pattern": "defer $_.Close()", "as": "DEF"}}`,
	}
	for _, payload := range cases {
		t.Run(payload, func(t *testing.T) {
			if _, err := ParseWhere([]byte(payload)); err != nil {
				t.Fatalf("ParseWhere(%q) errored unexpectedly: %v", payload, err)
			}
		})
	}
}
