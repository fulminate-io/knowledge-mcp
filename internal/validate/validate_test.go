// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSummary covers the contract of Summary: empty / whitespace-only /
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
		// Rune-vs-byte: the em-dash (U+2014) is 3 bytes. A 500-em-dash
		// summary is 1500 bytes but exactly 500 runes — accepted, proving
		// the cap counts runes not bytes (the byte-count regression is gone).
		{name: "500_runes_multibyte", summary: strings.Repeat("—", 500), wantErr: ""},
		// 501 em-dashes = 1503 bytes / 501 runes — rejected with a
		// rune-accurate count.
		{name: "501_runes_multibyte", summary: strings.Repeat("—", 501), wantErr: "exceeds 500 characters"},
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

// TestSummary_RuneCountReported confirms the over-cap error reports the
// count in RUNES, not bytes: a 501-em-dash summary (1503 bytes, 501 runes)
// must report "got 501", proving the byte-count regression is gone.
func TestSummary_RuneCountReported(t *testing.T) {
	err := Summary("test_tool", "summary", strings.Repeat("—", 501))
	if err == nil {
		t.Fatal("expected non-nil error for a 501-rune summary")
	}
	if !strings.Contains(err.Error(), "got 501") {
		t.Errorf("error must report the rune count (got 501), not the byte count: got %q", err.Error())
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

// TestStepDescription covers the contract of StepDescription: empty /
// whitespace-only / 1-character inputs return a structured error naming the
// calling tool and the field path; valid inputs (>= MinStepDescriptionLen=2
// chars after trimming) return nil. The 2-char floor is what rejects
// single-character and empty descriptions.
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

// TestClampSummary covers the forgiving-clamp contract: empty/whitespace-only
// summaries still hard-reject (emptiness cannot be clamped), under-cap and
// exactly-cap summaries pass through trimmed with no warning, and over-cap
// summaries are rune-safe word-boundary clamped to SummaryMaxLen with a
// non-fatal warning naming the tool and field path. The multibyte case proves
// the clamp never splits a UTF-8 rune.
func TestClampSummary(t *testing.T) {
	tests := []struct {
		name        string
		summary     string
		wantErr     string // substring expected in the error, or "" for nil
		wantWarn    bool   // whether a non-empty warning is expected
		wantClamped string // expected clamped value when err is nil; "" means "don't check exact value"
	}{
		{name: "empty", summary: "", wantErr: "is required"},
		{name: "whitespace only", summary: "   ", wantErr: "is required"},
		{name: "under cap passthrough", summary: "a concise summary", wantWarn: false, wantClamped: "a concise summary"},
		{name: "trims then passes through", summary: "  a concise summary  ", wantWarn: false, wantClamped: "a concise summary"},
		{name: "exactly 500 passthrough", summary: strings.Repeat("a", 500), wantWarn: false, wantClamped: strings.Repeat("a", 500)},
		{name: "501 ascii clamps with warning", summary: strings.Repeat("a", 501), wantWarn: true},
		{name: "501 multibyte rune-safe clamp", summary: strings.Repeat("—", 501), wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clamped, warning, err := ClampSummary("create_research", "questions[0].summary", tt.summary)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				if clamped != "" || warning != "" {
					t.Errorf("on error want empty clamped+warning, got clamped=%q warning=%q", clamped, warning)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantWarn && warning == "" {
				t.Errorf("expected a non-empty clamp warning, got empty")
			}
			if !tt.wantWarn && warning != "" {
				t.Errorf("expected no warning, got %q", warning)
			}
			if tt.wantWarn {
				// The warning must name the tool, the field path, and say it was clamped.
				if !strings.Contains(warning, "create_research") || !strings.Contains(warning, "questions[0].summary") {
					t.Errorf("warning must name the tool + field path: %q", warning)
				}
				if !strings.Contains(warning, "clamped") {
					t.Errorf("warning must say the summary was clamped: %q", warning)
				}
				// The clamped value must be within the cap and valid UTF-8.
				if rc := utf8.RuneCountInString(clamped); rc > SummaryMaxLen {
					t.Errorf("clamped rune count %d exceeds cap %d", rc, SummaryMaxLen)
				}
				if !utf8.ValidString(clamped) {
					t.Errorf("clamped value byte-sliced a multibyte rune (not valid UTF-8): %q", clamped)
				}
			}
			if tt.wantClamped != "" && clamped != tt.wantClamped {
				t.Errorf("clamped=%q want=%q", clamped, tt.wantClamped)
			}
		})
	}
}

// TestClampSummary_WordBoundary confirms an over-cap multi-word ascii summary is
// cut at a word boundary: the clamped result has no trailing space, is a prefix
// of the trimmed input, and is at most SummaryMaxLen runes.
func TestClampSummary_WordBoundary(t *testing.T) {
	// 100 words of "word " (5 chars each) = 500 chars, then one more word pushes
	// over the cap so the last space before rune 500 is the boundary.
	in := strings.Repeat("word ", 110) // 550 runes, spaces throughout
	clamped, warning, err := ClampSummary("create_plan", "summary", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warning == "" {
		t.Fatal("expected a clamp warning for a 550-rune summary")
	}
	if rc := utf8.RuneCountInString(clamped); rc > SummaryMaxLen {
		t.Errorf("clamped rune count %d exceeds cap %d", rc, SummaryMaxLen)
	}
	if strings.HasSuffix(clamped, " ") {
		t.Errorf("clamp should cut at a word boundary, leaving no trailing space: %q", clamped)
	}
	if !strings.HasPrefix(strings.TrimSpace(in), clamped) {
		t.Errorf("clamped value must be a prefix of the trimmed input: %q", clamped)
	}
}

// TestRunSelectorGuard pins the command-shape rule that closes the
// selector-matched-nothing vacuity class. The two arms are asymmetric on
// purpose: -run has runner-line markers to assert against, -list emits no marker
// at all and can only be checked by counting.
//
// list_takes_precedence_over_run is the interaction catcher. A command carrying
// BOTH flags LISTS and never runs, so an implementation that checked -run
// independently would demand a PASS line the runner cannot emit and reject a
// command that is already sound.
func TestRunSelectorGuard(t *testing.T) {
	const listWithCount = `N=$(go test ./internal/validate/ -list '^TestSummary$' | grep -c '^Test'); ` +
		`test -n "$N" && test "$N" -eq 1`
	tests := []struct {
		name    string
		command string
		wantErr string // substring expected in the error, or "" for nil
	}{
		{
			name:    "bare_go_test_run_is_rejected",
			command: "go test ./internal/recipe/ -run TestSomething",
			wantErr: "asserts nothing about WHETHER THE SELECTOR MATCHED",
		},
		{
			name: "pass_line_assertion_is_accepted",
			command: "go test ./internal/recipe/ -run '^TestSomething$' -v > /tmp/x.log 2>&1; " +
				"grep -q '^--- PASS: TestSomething ' /tmp/x.log",
		},
		{
			name: "fail_line_assertion_is_accepted",
			command: "go test ./internal/recipe/ -run '^TestSomething$' -v > /tmp/x.log 2>&1; " +
				"grep -q '^--- FAIL: TestSomething ' /tmp/x.log",
		},
		{
			name: "run_line_assertion_is_accepted",
			command: "go test ./internal/recipe/ -run '^TestSomething$' -v 2>&1 | " +
				"grep -q '=== RUN   TestSomething'",
		},
		{
			name:    "go_test_without_run_is_untouched",
			command: "go test ./internal/validate/ -count=1",
		},
		{
			// The flag token alone is not the trigger — only a `go test` carrying it.
			name:    "run_flag_without_go_test_is_untouched",
			command: "manual: re-run the collector with -run once the daemon is up",
		},
		{
			name:    "empty_command_is_untouched",
			command: "",
		},
		{
			name:    "error_names_the_field_path_and_quotes_the_command",
			command: "go test ./internal/recipe/ -run TestSomething",
			wantErr: "phases[0].steps[1].criteria[2].command",
		},
		{
			name:    "bare_go_test_list_is_rejected",
			command: "go test ./internal/validate/ -list '^TestSummary$'",
			wantErr: "asserts nothing about HOW MANY matched",
		},
		{
			name:    "list_with_a_count_comparison_is_accepted",
			command: listWithCount,
		},
		{
			name: "list_takes_precedence_over_run",
			command: `N=$(go test -tags=integration -run TestLeakSingleCycle -list '.*' ./test/integration/ ` +
				`| grep -c '^Test'); test -n "$N" && test "$N" -eq 1`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RunSelectorGuard("create_plan", "phases[0].steps[1].criteria[2].command", tt.command)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected acceptance, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a rejection containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
			if !strings.Contains(err.Error(), "create_plan") {
				t.Errorf("error must name the calling tool: %q", err.Error())
			}
			if !strings.Contains(err.Error(), tt.command) {
				t.Errorf("error must quote the offending command: %q", err.Error())
			}
		})
	}
}
