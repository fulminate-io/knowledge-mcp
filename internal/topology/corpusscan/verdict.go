// SPDX-License-Identifier: Apache-2.0

// verdict.go folds a run's findings into the ONE classification every consumer
// acts on.
//
// IT LIVES HERE, IN THE PACKAGE THAT OWNS THE TITLE CONSTANTS, and that is not a
// filing preference. vocabulary.go calls itself the single authoritative
// declaration for every token more than one part of this family consumes;
// classifying elsewhere would put a consumer's copy of that vocabulary one import
// away from the declaration. There are two consumers — the MCP tool's verdict
// line and the CLI's exit status — and two hand-kept classifiers would be two
// answers to the same question.

package corpusscan

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// RunVerdict is what a run amounted to, counted rather than summarized.
type RunVerdict struct {
	// ChecksExecuted is how many DISTINCT checks produced at least one match
	// finding.
	//
	// IT IS A FLOOR, NOT THE NUMBER THAT RAN, and the gap is structural rather
	// than an omission: a check that executed and matched nothing leaves no
	// finding behind, so a fold over findings alone cannot see it. Nothing
	// downstream can recover the true figure either — Run returns findings and
	// no corpus size. Read this as "checks that flagged something", which is how
	// every consumer renders it, and never as a completeness measure.
	ChecksExecuted int
	// SitesFlagged is how many findings flag a real site — every finding that is
	// not one of this analyzer's own disclosures.
	SitesFlagged int
	// ChecksRefused is how many checks the admission gate or the environment
	// stopped from executing.
	ChecksRefused int
	// LLMOnlyNotExecuted is how many accepted llm_only entries the corpus held
	// and the scan did not execute.
	LLMOnlyNotExecuted int
	// Truncated reports whether either render ceiling fired, so a bounded result
	// is never mistaken for a complete one.
	Truncated bool
	// TestFilesScanned is how many of this language's test files the run's walk
	// reached — the most any single check scanned, since the test-file scope is
	// per check. Zero means the run did not read test code at all, which is a
	// different answer from having found nothing in it.
	//
	// IT DOES NOT PARTICIPATE IN Clean() OR Inconclusive(). Reaching test files
	// is a scope fact, not a completeness one: a run that deliberately excluded
	// them answered the question it was asked.
	TestFilesScanned int
}

// Clean reports whether the run both completed and found nothing.
//
// IT IS A METHOD RATHER THAN A RULE EACH CONSUMER RE-DERIVES, because the MCP
// verdict line and the CLI exit status must not be able to disagree. A run that
// could not execute part of its corpus, or whose output was clipped, is NOT
// clean: reporting it as clean is exactly the vacuous green the fixture gate and
// the render-ceiling disclosures exist to prevent.
func (v RunVerdict) Clean() bool {
	return v.SitesFlagged == 0 && v.ChecksRefused == 0 && !v.Truncated
}

// Inconclusive reports whether the run failed to deliver a complete answer —
// some check could not execute, or some output was withheld. It is distinct from
// FLAGGED: a flagged run answered the question and the answer was bad news, while
// an inconclusive one did not answer it.
func (v RunVerdict) Inconclusive() bool {
	return v.ChecksRefused > 0 || v.Truncated
}

// ClassifyRun folds a run's findings into a verdict.
//
// IT KEYS ON THE LOCKED TITLE CONSTANTS, never on message wording and never on
// severity. Severity cannot tell a refusal from a real flag — a refusal is
// emitted at critical, and so is a critical check's genuine site — so a verdict
// derived from severity counts alone is wrong in exactly the case that matters.
//
// THE TRAILING SPACE ON THE TWO REFUSAL PREFIXES IS LOAD-BEARING: an id is
// concatenated directly after it, which is why these are HasPrefix tests against
// the declared constants rather than equality or a hand-typed copy.
//
// THE DEFAULT ARM IS A TRAP FOR NEW DISCLOSURES. Anything this switch does not
// recognize counts as a flagged site, so a disclosure added to the analyzer
// without an arm here adds one to sites_flagged on every run, makes Clean()
// false, and renders a clean corpus as FLAGGED with a non-zero exit status.
func ClassifyRun(findings []foundation.Finding) RunVerdict {
	var v RunVerdict
	flagged := map[string]bool{}
	for _, f := range findings {
		switch {
		case strings.HasPrefix(f.Title, RefusalPrefixUnvalidated),
			strings.HasPrefix(f.Title, RefusalPrefixEnvironment):
			v.ChecksRefused++
		case strings.HasPrefix(f.Title, TruncationPrefixCheck), f.Title == TruncationTitleRun:
			v.Truncated = true
		case f.Title == DisclosureTitleLLMOnly:
			v.LLMOnlyNotExecuted = int(f.Metrics["llm_only_total"])
		case f.Title == DisclosureTitleTestFiles:
			v.TestFilesScanned = int(f.Metrics[MetricTestFilesScanned])
		default:
			v.SitesFlagged++
			flagged[f.Metadata[MetaKeyCheckID]] = true
		}
	}
	v.ChecksExecuted = len(flagged)
	return v
}
