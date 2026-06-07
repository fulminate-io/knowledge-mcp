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
