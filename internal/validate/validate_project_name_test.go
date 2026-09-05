// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestProjectName covers the project-name guard: it delegates to Name for
// the empty and newline arms, and adds an 80-RUNE cap counted in runes, not
// bytes. Nothing is clamped — an over-cap name is refused with the limit
// named, which is the whole point of failing fast client-side instead of
// spending a network round trip on a rejection Linear would issue anyway.
//
// The cap is deliberately NOT inside Name: Name has ten call sites and only
// the create_project one is capped. The paired control lives in
// cmd/knowledge/internal/tools (a 131-rune create_ticket name still creates).
func TestProjectName(t *testing.T) {
	multibyte := strings.Repeat("é", ProjectNameMaxRunes) // 80 runes, 160 bytes
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "empty_delegates_to_Name", in: "", wantErr: "is required"},
		{name: "whitespace_delegates_to_Name", in: "   ", wantErr: "is required"},
		{name: "newline_delegates_to_Name", in: "a\nb", wantErr: "newline"},
		{name: "short", in: "A project", wantErr: ""},
		{name: "exactly_at_cap", in: strings.Repeat("a", ProjectNameMaxRunes), wantErr: ""},
		{name: "one_over_cap", in: strings.Repeat("a", ProjectNameMaxRunes+1), wantErr: "80"},
		{name: "multibyte_at_cap_passes", in: multibyte, wantErr: ""},
		{name: "multibyte_one_over_cap", in: multibyte + "é", wantErr: "80"},
		// THE PADDED CLASS. Nothing trims the name between this guard and the
		// tracker's input.name, so the string the tracker measures includes the
		// surrounding whitespace and the cap must measure it too.
		{name: "padded_at_cap_is_over_once_padding_counts",
			in:      " " + strings.Repeat("a", ProjectNameMaxRunes) + " ",
			wantErr: "80"},
		{name: "padded_but_within_cap_passes",
			in:      " " + strings.Repeat("a", ProjectNameMaxRunes-2) + " ",
			wantErr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ProjectName("create_project", tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ProjectName(%d runes) unexpected error: %v", utf8.RuneCountInString(tc.in), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ProjectName(%d runes) want error containing %q, got nil", utf8.RuneCountInString(tc.in), tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ProjectName error = %q, want substring %q", err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "create_project") {
				t.Errorf("ProjectName error must name the calling tool: got %q", err.Error())
			}
		})
	}
}

// TestProjectName_MultibyteCapIsRunesNotBytes is the control that separates a
// rune cap from a byte cap: 80 multibyte runes are 160 bytes, so a byte-cap
// implementation refuses a name the rune cap accepts. The measurement behind
// the cap counted runes.
func TestProjectName_MultibyteCapIsRunesNotBytes(t *testing.T) {
	in := strings.Repeat("é", ProjectNameMaxRunes)
	if got := len(in); got <= ProjectNameMaxRunes {
		t.Fatalf("control failed: the fixture is %d bytes, which does not exceed the cap, so it cannot tell runes from bytes", got)
	}
	if err := ProjectName("create_project", in); err != nil {
		t.Errorf("ProjectName(%d runes / %d bytes) = %v, want nil — the cap counts RUNES", utf8.RuneCountInString(in), len(in), err)
	}
}

// TestProjectName_NothingIsClamped asserts the guard is a refusal, not a
// truncation: it returns only an error, never a rewritten name. A clamp would
// silently ship a different project name than the caller asked for.
func TestProjectName_NothingIsClamped(t *testing.T) {
	over := strings.Repeat("a", ProjectNameMaxRunes+1)
	err := ProjectName("create_project", over)
	if err == nil {
		t.Fatalf("expected a refusal for an over-cap name, got nil")
	}
	if !strings.Contains(err.Error(), over) {
		t.Errorf("the refusal should quote the offending name so the caller can see what was rejected: %q", err.Error())
	}
}

// TestProjectName_CapCountsTheStringThatIsSent is the end-to-end claim behind
// the padded cases above, stated once so a later reader does not "simplify"
// the guard by trimming before it counts.
//
// NOTHING TRIMS THE NAME between this guard and the tracker. The create_project
// intercept hands validate.ProjectName the caller's raw name and then passes
// that same raw value to the backend, which sends it as input.name. So a guard
// that measured a trimmed copy would accept a name the tracker then rejects at
// its own limit — after a network round trip, which is exactly what failing
// fast client-side exists to avoid.
//
// The alternative, trimming the name here so the shorter value is what gets
// sent, is deliberately NOT taken: silently sending a different name than the
// caller asked for is a coercion, and this path refuses and explains instead.
func TestProjectName_CapCountsTheStringThatIsSent(t *testing.T) {
	body := strings.Repeat("a", ProjectNameMaxRunes)
	padded := "   " + body + "   "

	// Control: the same body without padding is accepted, so a refusal of the
	// padded form is attributable to the padding and nothing else.
	if err := ProjectName("create_project", body); err != nil {
		t.Fatalf("control failed: an unpadded %d-rune name must pass, got %v",
			utf8.RuneCountInString(body), err)
	}
	if utf8.RuneCountInString(padded) <= ProjectNameMaxRunes {
		t.Fatalf("control failed: the padded fixture is %d runes, not over the %d-rune cap, so it cannot exercise the class",
			utf8.RuneCountInString(padded), ProjectNameMaxRunes)
	}

	err := ProjectName("create_project", padded)
	if err == nil {
		t.Fatalf("a %d-rune padded name passed the %d-rune cap; the tracker receives all %d runes and would reject it",
			utf8.RuneCountInString(padded), ProjectNameMaxRunes, utf8.RuneCountInString(padded))
	}
	if !strings.Contains(err.Error(), "80") {
		t.Errorf("the refusal must name the limit: %q", err.Error())
	}
}
