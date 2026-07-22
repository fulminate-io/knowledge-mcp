// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"strings"
	"testing"
)

func TestPromptLine(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   string
		want  string
	}{
		{name: "typed value overrides default", input: "openai\n", def: "anthropic", want: "openai"},
		{name: "empty input returns default", input: "\n", def: "anthropic", want: "anthropic"},
		{name: "whitespace trims to empty → default", input: "   \n", def: "anthropic", want: "anthropic"},
		{name: "exhausted stream returns default", input: "", def: "claude-cli", want: "claude-cli"},
		{name: "surrounding whitespace trimmed", input: "  gemini \n", def: "anthropic", want: "gemini"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				sc := bufio.NewScanner(strings.NewReader(tc.input))
				if got := promptLine(sc, "provider", tc.def); got != tc.want {
					t.Errorf("promptLine(%q, def=%q) = %q; want %q", tc.input, tc.def, got, tc.want)
				}
			})
			if !strings.Contains(out, "provider ["+tc.def+"]") {
				t.Errorf("prompt line must echo label + default; got %q", out)
			}
		})
	}
}

func TestPromptYesNo(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   bool
		want  bool
	}{
		{name: "y → true", input: "y\n", def: false, want: true},
		{name: "yes → true", input: "yes\n", def: false, want: true},
		{name: "n → false", input: "n\n", def: true, want: false},
		{name: "no → false", input: "no\n", def: true, want: false},
		{name: "empty → default (false)", input: "\n", def: false, want: false},
		{name: "empty → default (true)", input: "\n", def: true, want: true},
		{name: "unrecognized → default", input: "maybe\n", def: true, want: true},
		{name: "exhausted stream → default", input: "", def: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = captureStdout(t, func() {
				sc := bufio.NewScanner(strings.NewReader(tc.input))
				if got := promptYesNo(sc, "proceed?", tc.def); got != tc.want {
					t.Errorf("promptYesNo(%q, def=%v) = %v; want %v", tc.input, tc.def, got, tc.want)
				}
			})
		})
	}
}
