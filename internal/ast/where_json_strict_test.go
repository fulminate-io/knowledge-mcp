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
		`{"flows_to": {"from": "P", "to": "ARG", "within": "$match"}}`,
	}
	for _, payload := range cases {
		t.Run(payload, func(t *testing.T) {
			if _, err := ParseWhere([]byte(payload)); err != nil {
				t.Fatalf("ParseWhere(%q) errored unexpectedly: %v", payload, err)
			}
		})
	}
}

// TestParseWhere_FlowsToLeafVocabulary pins the flows_to leaf's decode contract
// BY BEHAVIOUR rather than by grepping source, because a source grep is
// satisfied by a doc comment while a caller's actual experience is decided by
// what the decoders do.
//
// The third assertion is the one that earns its place: it is the only check
// that the ParseWhere error's valid-key list was actually updated. That string
// is where a caller learns the key exists when they get it wrong, and nothing
// else in the package would notice if it went stale.
func TestParseWhere_FlowsToLeafVocabulary(t *testing.T) {
	t.Run("well-formed payload parses", func(t *testing.T) {
		w, err := ParseWhere([]byte(`{"flows_to": {"from": "P", "to": "ARG", "within": "FN"}}`))
		if err != nil {
			t.Fatalf("a well-formed flows_to payload must parse: %v", err)
		}
		if w == nil || w.FlowsTo == nil {
			t.Fatal("the flows_to leaf must be populated on the parsed node")
		}
		if w.FlowsTo.From != "P" || w.FlowsTo.To != "ARG" || w.FlowsTo.Within != "FN" {
			t.Fatalf("all three fields must round-trip, got %+v", *w.FlowsTo)
		}
	})

	t.Run("typo'd inner field is rejected and named", func(t *testing.T) {
		// The inner decoder runs DisallowUnknownFields, so a near-miss like
		// "wthin" must not be silently dropped — a dropped `within` would turn a
		// scope-less leaf into one the evaluator has to guess at.
		_, err := ParseWhere([]byte(`{"flows_to": {"from": "P", "to": "ARG", "wthin": "FN"}}`))
		if err == nil {
			t.Fatal("a typo'd inner field must be rejected, not silently dropped")
		}
		if !strings.Contains(err.Error(), "wthin") {
			t.Fatalf("the error must NAME the offending field so the typo is findable; got: %v", err)
		}
	})

	t.Run("unknown top-level key error advertises flows_to", func(t *testing.T) {
		_, err := ParseWhere([]byte(`{"no_such_leaf": {"of": "X"}}`))
		if err == nil {
			t.Fatal("an unknown top-level key must be rejected")
		}
		if !strings.Contains(err.Error(), "flows_to") {
			t.Fatalf("the valid-key list must advertise flows_to, or a caller who "+
				"typo'd it can never discover the real spelling; got: %v", err)
		}
	})
}
