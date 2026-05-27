// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"
)

// TestSummary covers the contract of Summary (ticket
// dfd1f4e2a0777c6711e363f2ec3edefc): empty / whitespace-only /
// over-cap inputs return a structured error naming the calling tool
// and field path; valid inputs return nil. The 500-char cap is
// enforced against the trimmed length.
func TestSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		wantErr string // substring expected in the error, or "" for nil
	}{
		{name: "empty", summary: "", wantErr: "is required"},
		{name: "whitespace_only_spaces", summary: "   ", wantErr: "is required"},
		{name: "whitespace_only_tabs", summary: "\t\t", wantErr: "is required"},
		{name: "whitespace_only_newlines", summary: "\n\n", wantErr: "is required"},
		{name: "single_char", summary: "x", wantErr: ""},
		{name: "exactly_500", summary: strings.Repeat("a", 500), wantErr: ""},
		{name: "501_chars", summary: strings.Repeat("a", 501), wantErr: "exceeds 500 characters"},
		{name: "leading_trailing_whitespace_under_cap", summary: "   actual content   ", wantErr: ""},
		{name: "leading_trailing_whitespace_over_cap", summary: "  " + strings.Repeat("a", 501) + "  ", wantErr: "exceeds 500 characters"},
		{name: "valid_realistic_summary", summary: "Search-optimized one-line summary describing what the node is about.", wantErr: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Summary("test_tool", "summary", tc.summary)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Summary(%q) unexpected error: %v", tc.summary, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Summary(%q) want error containing %q, got nil", tc.summary, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Summary(%q) error = %q, want substring %q", tc.summary, err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "test_tool") {
				t.Errorf("Summary error must name calling tool: got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "summary") {
				t.Errorf("Summary error must name the field path: got %q", err.Error())
			}
		})
	}
}

// TestSummary_FieldPathThreaded confirms a non-trivial fieldPath (e.g.
// "phases[1].steps[0].summary") is preserved in the error message —
// the pre-req-A spec calls for nested validation in create_plan to
// name the offending position.
func TestSummary_FieldPathThreaded(t *testing.T) {
	err := Summary("create_plan", "phases[1].steps[0].summary", "")
	if err == nil {
		t.Fatal("expected non-nil error for empty summary")
	}
	if !strings.Contains(err.Error(), "phases[1].steps[0].summary") {
		t.Errorf("error must contain nested field path: got %q", err.Error())
	}
}

// TestStepDescription covers the contract of StepDescription (ticket
// caecceee11feb1a699159b26dccb487d fix #5): empty / whitespace-only /
// 1-character inputs return a structured error naming the calling tool
// and the field path; valid inputs (>= MinStepDescriptionLen=2 chars
// after trimming) return nil. The 2-char floor matches the ticket's
// "single-character or empty descriptions" wording exactly.
func TestStepDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantErr     string // substring expected in the error, or "" for nil
	}{
		{name: "empty", description: "", wantErr: "is required"},
		{name: "whitespace_only_spaces", description: "   ", wantErr: "is required"},
		{name: "whitespace_only_tabs", description: "\t\t", wantErr: "is required"},
		{name: "whitespace_only_newlines", description: "\n\n", wantErr: "is required"},
		{name: "single_char_x", description: "x", wantErr: "must be at least 2 characters"},
		{name: "single_char_padded", description: "  x  ", wantErr: "must be at least 2 characters"},
		{name: "two_chars", description: "ok", wantErr: ""},
		{name: "tbd_three_chars", description: "tbd", wantErr: ""},
		{name: "real_short_title", description: "fix bug", wantErr: ""},
		{name: "long_realistic_description", description: "Add StepDescription validator + wire into mutate(create, type:'step')", wantErr: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := StepDescription("test_tool", "description", tc.description)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("StepDescription(%q) unexpected error: %v", tc.description, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("StepDescription(%q) want error containing %q, got nil", tc.description, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("StepDescription(%q) error = %q, want substring %q", tc.description, err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "test_tool") {
				t.Errorf("StepDescription error must name calling tool: got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "description") {
				t.Errorf("StepDescription error must name the field path: got %q", err.Error())
			}
		})
	}
}

// TestStepDescription_FieldPathThreaded confirms the create_plan
// nested field path "phases[i].steps[j].description" is preserved in
// the error message so the planner can identify which step failed.
func TestStepDescription_FieldPathThreaded(t *testing.T) {
	err := StepDescription("create_plan", "phases[1].steps[0].description", "x")
	if err == nil {
		t.Fatal("expected non-nil error for placeholder description 'x'")
	}
	if !strings.Contains(err.Error(), "phases[1].steps[0].description") {
		t.Errorf("error must contain nested field path: got %q", err.Error())
	}
}

// TestName covers the contract of Name: empty / whitespace inputs
// return a structured error naming the calling tool and field path;
// any non-empty trimmed input passes.
func TestName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "empty", in: "", wantErr: "is required"},
		{name: "spaces", in: "   ", wantErr: "is required"},
		{name: "tabs", in: "\t\t", wantErr: "is required"},
		{name: "newlines", in: "\n\n", wantErr: "is required"},
		{name: "single_char", in: "x", wantErr: ""},
		{name: "real_label", in: "Architecture decision: use composite-DB", wantErr: ""},
		{name: "padded", in: "   actual   ", wantErr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Name("test_tool", tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Name(%q) unexpected error: %v", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Name(%q) want error containing %q, got nil", tc.in, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Name(%q) error = %q, want substring %q", tc.in, err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "test_tool") {
				t.Errorf("Name error must name calling tool: got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "name") {
				t.Errorf("Name error must name the field path: got %q", err.Error())
			}
		})
	}
}
