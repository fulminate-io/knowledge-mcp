// SPDX-License-Identifier: Apache-2.0

// Package validate exposes the create-time validation guards the
// client-side intercepts run when they claim mutate(create) for
// project-domain types (plan, phase, step, finding, research, rule, ...).
// The guards live client-side because the client now owns the entire
// create path — the server-side ToolService that once shared this
// contract was deleted, leaving the EngineService wire as the
// only consumer.
//
// The exported names (Name, Summary, StepDescription) match the
// previously unexported names modulo capitalization. Behavior is
// preserved bit-for-bit so the golden capture is unchanged after
// relocation.
package validate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SummaryMaxLen caps the length of search-optimized summaries on every
// embed-only-knowledge node creator path. Summaries should be a single
// concise line; 500 chars is the upper bound.
const SummaryMaxLen = 500

// MinStepDescriptionLen is the lower bound for step Description length.
// 2 chars matches the "single-character or empty descriptions"
// wording — rejects "x" (1
// char) and "" while accepting any real two-character title. Keep this
// value tight; the goal is stopping the actual offenders that escape
// into the live graph as orphan placeholder steps, not enforcing prose
// minimums.
const MinStepDescriptionLen = 2

// Summary returns nil when summary is acceptable for an embed-only-
// knowledge node, else a structured error naming the calling tool and
// the failure reason. Callers gate on NodeType.Summarizable() to decide
// WHEN to invoke Summary; this helper does NOT consult any NodeType
// map. Embed-only nodes require an author-supplied summary at creation
// time because pipeline v2 stops auto-summarizing them.
func Summary(toolName, fieldPath, summary string) error {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return fmt.Errorf("%s: %s is required and must be non-empty (search-optimized one-line summary)", toolName, fieldPath)
	}
	if utf8.RuneCountInString(trimmed) > SummaryMaxLen {
		return fmt.Errorf("%s: %s exceeds %d characters (got %d). Search-optimized summaries should be a single concise line", toolName, fieldPath, SummaryMaxLen, utf8.RuneCountInString(trimmed))
	}
	return nil
}

// clampAtWord truncates s to at most maxLen RUNES at a word boundary, never
// splitting a multibyte UTF-8 sequence (so the result is always valid UTF-8).
// No ellipsis is appended — it returns a clean prefix. This is a re-home of the
// client-side tools.truncateAtWordCreate clamp idiom into the validate package:
// the validate package cannot import tools (tools imports validate), so the
// logic is duplicated here to give ClampSummary a cycle-free home. The two
// copies are deliberately kept in lock-step.
func clampAtWord(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	prefix := string([]rune(s)[:maxLen])
	idx := strings.LastIndex(prefix, " ")
	if idx <= 0 {
		return prefix
	}
	return prefix[:idx]
}

// ClampSummary is the forgiving counterpart to Summary for AUTHOR-supplied
// summaries: an over-cap summary is rune-safe word-boundary CLAMPED to
// SummaryMaxLen and a non-fatal warning is returned, so the write succeeds
// instead of hard-rejecting. An EMPTY summary still hard-rejects — emptiness
// cannot be clamped — with a message byte-identical to Summary's required
// message, so empty-summary behavior is unchanged. An in-range summary passes
// through trimmed with no warning. Counting matches Summary/DerivedSummary
// (TrimSpace then RuneCountInString). Callers assign the clamped value back into
// the field the node builder reads, and surface a non-empty warning to the user.
func ClampSummary(toolName, fieldPath, summary string) (clamped string, warning string, err error) {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return "", "", fmt.Errorf("%s: %s is required and must be non-empty (search-optimized one-line summary)", toolName, fieldPath)
	}
	n := utf8.RuneCountInString(trimmed)
	if n > SummaryMaxLen {
		c := clampAtWord(trimmed, SummaryMaxLen)
		w := fmt.Sprintf("%s: %s exceeded %d characters (got %d) and was clamped to %d at a word boundary. Author shorter summaries to avoid losing detail", toolName, fieldPath, SummaryMaxLen, n, utf8.RuneCountInString(c))
		return c, w, nil
	}
	return trimmed, "", nil
}

// runePrefix returns up to the first n runes of s. When s is longer than n
// runes the result is truncated to n runes and an ellipsis is appended,
// signaling there is more text. It is rune-safe (never byte-slices, so it
// never splits a multibyte rune) — callers use it to quote a bounded snippet
// of an over-long summary in an error message.
func runePrefix(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

// DerivedSummary validates an AUTO-DERIVED summary (one the caller built by
// concatenating other fields rather than authoring directly) against the same
// rune cap as Summary, but produces an actionable error: it names the indexed
// field path, states the summary was auto-derived from derivedFrom, reports the
// rune count and how far over the cap it is, and quotes a bounded prefix of the
// offending text so the author can see WHICH derivation overflowed. The caller
// passes the ALREADY-DERIVED string plus a human-readable list of the source
// fields, so this validator never learns the derivation shapes.
//
// Trim-then-count parity: the rune count matches Summary (validate.go) and the
// server validateSummary backstop, which both TrimSpace before counting. There
// is no empty-check branch — a derived summary always carries its literal
// prefix, so emptiness is not a reachable failure here.
func DerivedSummary(toolName, fieldPath, derivedFrom, derived string) error {
	trimmed := strings.TrimSpace(derived)
	n := utf8.RuneCountInString(trimmed)
	if n > SummaryMaxLen {
		return fmt.Errorf("%s: %s is an auto-derived summary (derived from %s) that exceeds %d characters (got %d, over by %d). Shorten the source fields. Derived prefix: %q",
			toolName, fieldPath, derivedFrom, SummaryMaxLen, n, n-SummaryMaxLen, runePrefix(trimmed, 80))
	}
	return nil
}

// goTestRunFlag / goTestListFlag / numericCompare detect the two `go test`
// selector flags and the numeric comparison that makes a -list command
// falsifiable. Matching is by FLAG TOKEN, never bare substring, so a command
// merely containing the letters "run" or "list" is untouched. Compiled once at
// package level: a create_plan payload runs the guard once per criterion.
var (
	goTestRunFlag  = regexp.MustCompile(`(^|\s)-run(\s|=)`)
	goTestListFlag = regexp.MustCompile(`(^|\s)-list(\s|=)`)
	numericCompare = regexp.MustCompile(`(^|\s)-(eq|ne|ge|gt|le|lt)(\s|$)`)
)

// commandQuoteLen bounds how much of an offending command the error quotes.
// Longer than the summary guards' 80 because a command's tail is usually where
// the missing assertion would go, and a clipped tail hides exactly that.
const commandQuoteLen = 200

// RunSelectorGuard rejects a criterion command that selects tests by name but
// asserts nothing about whether the selector matched anything. Both `go test`
// selector flags exit 0 on a selector that matches NOTHING — -run prints
// "ok <pkg> [no tests to run]", -list prints an empty list — so a criterion whose
// pass condition is the exit status cannot tell "verified" from "verified
// nothing".
//
// The rule, and its ORDER IS LOAD-BEARING:
//
//	(0) no `go test` in the command            → nil
//	(1) a -list flag  → require a NUMERIC COMPARISON, else reject
//	(2) else a -run flag → require a NAMED RUNNER LINE (--- PASS / --- FAIL /
//	    === RUN), else reject
//	(3) else                                   → nil
//
// -list takes precedence because when BOTH flags appear `go test` lists and never
// runs, so applying the -run arm to a both-flags command would demand a PASS line
// the runner can never emit. And -list emits no marker of its own at all, which
// is why its arm asserts a COUNT: a bare `grep -c` prints its count and exits 0
// regardless, so the count must feed a comparison to be falsifiable.
//
// The -run arm asserts POSITIVELY rather than rejecting "[no tests to run]": that
// marker legitimately appears on a multi-package run for the packages the
// selector does not match, so its absence proves nothing.
//
// WHAT THIS DOES NOT CLAIM. It asserts command SHAPE, never semantics, so three
// vacuity shapes survive it: a swallowed exit status (a trailing `|| true`, or a
// final pipeline stage that always succeeds); a multi-package -run whose
// unanchored assertion is satisfied by one package while the rest match nothing;
// and an always-true comparison such as `-ge 0`. Those remain the author's
// responsibility.
func RunSelectorGuard(toolName, fieldPath, command string) error {
	if !strings.Contains(command, "go test") {
		return nil
	}
	quoted := runePrefix(strings.TrimSpace(command), commandQuoteLen)
	switch {
	case goTestListFlag.MatchString(command):
		if numericCompare.MatchString(command) {
			return nil
		}
		return fmt.Errorf("%s: %s lists matching test names but asserts nothing about HOW MANY matched. "+
			"`go test -list <selector>` exits 0 whether or not the selector matched, and prints no marker, "+
			"so only the count can tell a real match from none — and a bare `grep -c` prints its count and "+
			"exits 0 too. Compare the count numerically: "+
			"N=$(go test ... -list '^TestName$' | grep -c '^Test'); test -n \"$N\" && test \"$N\" -eq 1. Command: %q",
			toolName, fieldPath, quoted)
	case goTestRunFlag.MatchString(command):
		for _, marker := range []string{"--- PASS", "--- FAIL", "=== RUN"} {
			if strings.Contains(command, marker) {
				return nil
			}
		}
		return fmt.Errorf("%s: %s selects tests by name but asserts nothing about WHETHER THE SELECTOR MATCHED. "+
			"`go test -run <selector>` exits 0 printing \"ok <pkg> [no tests to run]\" when the selector matches "+
			"nothing, so this command cannot tell a passing test from an absent one. Assert a named runner line: "+
			"... -v > /tmp/x.log 2>&1; grep -q '^--- PASS: TestName ' /tmp/x.log. Command: %q",
			toolName, fieldPath, quoted)
	}
	return nil
}

// StepDescription enforces non-empty, non-trivial descriptions on
// NodeStep creations. Single-character and all-whitespace descriptions
// are the symptom of placeholder steps escaping into the graph.
// Apply at every
// step-creation entry point: handleMutateCreate when type=="step", and
// the create_plan per-step nested-validation loop.
func StepDescription(toolName, fieldPath, description string) error {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return fmt.Errorf("%s: %s is required and must be non-empty", toolName, fieldPath)
	}
	if len(trimmed) < MinStepDescriptionLen {
		return fmt.Errorf("%s: %s must be at least %d characters (got %d) — placeholder descriptions like \"x\" are rejected", toolName, fieldPath, MinStepDescriptionLen, len(trimmed))
	}
	return nil
}

// Name enforces a non-empty, non-whitespace, single-line Name on
// creation paths for human-meaningful node types (decision, finding,
// project, ticket, plan, phase, step, document, rule, pattern, etc.).
// Empty-name nodes pollute search results and the graph examine view
// (rendered as "[type]" with no identity). Names with embedded newlines
// break markdown table renders, search snippets, and at least one
// backend (Linear rejects newlines in project name with a GraphQL
// validation error after a network round trip — better to fail fast).
// Apply at the dispatch site that owns the node-type semantics.
//
// Field name is hardcoded to "name" — every call site lives at the
// mutate(create) boundary where the offending field is always the top-
// level Name. Wider field-path threading (à la Summary) can be added
// if a nested-validation path shows up later.
func Name(toolName, name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%s: name is required and must be non-empty (a human-readable label for the node)", toolName)
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return fmt.Errorf("%s: name must not contain newline characters (got %q) — names render in markdown tables, search snippets, and external backends (e.g., Linear) reject embedded newlines", toolName, name)
	}
	return nil
}
