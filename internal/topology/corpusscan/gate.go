// SPDX-License-Identifier: Apache-2.0

// gate.go is the fixture gate: the analyzer refuses, loudly and by name, to
// execute a check that has no passing fixture validation.
//
// THERE IS NO MARKER TO READ. The contract persists nothing about a passed
// validation, deliberately — a stamp would create a drift class where editing a
// fixture's Content leaves the stamp over-claiming. The rule is satisfied by
// CALLING the validator on every run.

package corpusscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The refusal classifications. FOUR STATES, and they are told apart by
// errors.Is against the contract's exported sentinels — never by sniffing the
// message text, which is payload this package relays and never asserts on.
//
//	(a) CONTRACT MALFORMED — corpus.ParseCheck refused the node. This one is
//	    NOT a finding: it is raised as a hard error at decode time in checks.go,
//	    because a corpus carrying a node the contract rejects is a corpus an
//	    operator must fix before any result from it means anything.
//	(b) FIXTURE VALIDATION FAILED — errors.Is(err, corpus.ErrFixtureValidation).
//	    About the CHECK or the FIXTURE CONTENT.
//	(c) UNEXECUTABLE CHECK TYPE — errors.Is(err, corpus.ErrNoExecutor).
//	(d) ENVIRONMENT — the host could not PLACE the fixture on disk. Two sources:
//	    the whole-run precondition probe below, and
//	    errors.Is(err, corpus.ErrFixtureMaterialization) mid-run.
const (
	classifyValidation  = "fixture validation failed"
	classifyNoExecutor  = "no executor for this check type"
	classifyEnvironment = "the fixture could not be placed on disk"
	classifyFixtureBind = "a fixture binding does not resolve in this corpus"
)

// probeTempDir asks ONCE per run whether this host can place a fixture on disk.
//
// It is no longer load-bearing for classification — errors.Is answers that — but
// it is still the right shape: it fails the whole run once, early, with an
// operator-actionable message, instead of letting N checks fail one at a time
// with N identical relayed errors. os.TempDir re-reads TMPDIR on every call on
// unix, so the environment variable is a live seam and this needs no injection
// point in production code.
func probeTempDir() error {
	d, err := os.MkdirTemp("", "corpusscan-envprobe-")
	if err != nil {
		return err
	}
	_ = os.RemoveAll(d)
	return nil
}

// resolveFixtures binds a check's two fixture ids to the example nodes read from
// the same checks graph. The binding is METADATA AND ONLY METADATA — never an
// edge.
func resolveFixtures(set corpusSet, c corpus.Check) (bad, good corpus.Fixture, err error) {
	bad, ok := set.Fixtures[c.FixtureBad]
	if !ok {
		return corpus.Fixture{}, corpus.Fixture{}, fmt.Errorf("%s=%q resolves to no example node in this corpus", corpus.MetaFixtureBad, c.FixtureBad)
	}
	good, ok = set.Fixtures[c.FixtureGood]
	if !ok {
		return corpus.Fixture{}, corpus.Fixture{}, fmt.Errorf("%s=%q resolves to no example node in this corpus", corpus.MetaFixtureGood, c.FixtureGood)
	}
	return bad, good, nil
}

// validateEntryFixtures dispatches to the validator that owns the check type's
// fixture semantics.
//
// Routing a graph-shaped check through corpus.ValidateFixtures would produce a
// PERMANENT state-(c) refusal even though this package holds its executor,
// silently disabling it — so the dispatch is explicit rather than a default.
// flow_model deliberately falls to the contract's validator, which refuses it
// with ErrNoExecutor: that is the honest answer while the flow-fact collector is
// unlanded, and it is a named seam rather than a silent skip.
func validateEntryFixtures(ctx context.Context, c corpus.Check, bad, good corpus.Fixture) error {
	switch c.Type {
	case corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold:
		return ValidateGraphFixtures(ctx, c, bad, good)
	default:
		return corpus.ValidateFixtures(ctx, c, bad, good)
	}
}

// classifyRefusal turns a validator error into the per-check refusal finding,
// classified by SENTINEL. The upstream message survives verbatim inside the
// classification this package decides.
func classifyRefusal(c corpus.Check, err error) foundation.Finding {
	switch {
	case errors.Is(err, corpus.ErrFixtureMaterialization):
		return refusalFinding(c, RefusalPrefixEnvironment+c.ID, classifyEnvironment, err)
	case errors.Is(err, corpus.ErrNoExecutor):
		return refusalFinding(c, RefusalPrefixUnvalidated+c.ID, classifyNoExecutor, err)
	default:
		return refusalFinding(c, RefusalPrefixUnvalidated+c.ID, classifyValidation, err)
	}
}

// refusalFinding builds one refusal. It carries NO file or line key: there is no
// flagged site, and a zero line would be a false row in the calibration join.
func refusalFinding(c corpus.Check, title, classification string, err error) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityCritical,
		Title:     title,
		Summary:   fmt.Sprintf("%s: %s: %v", c.ID, classification, err),
		Evidence:  []string{c.ID},
		Metadata:  map[string]string{MetaKeyCheckID: c.ID},
	}
}

// environmentRefusal is state (d)'s whole-run form: the precondition probe
// failed, so corpus.ValidateFixtures is never called for any check.
func environmentRefusal(c corpus.Check, probeErr error) foundation.Finding {
	return refusalFinding(c, RefusalPrefixEnvironment+c.ID, classifyEnvironment, probeErr)
}

// llmOnlyDisclosure reports the accepted-llm_only lane: nodes the contract
// validated and deliberately marked as needing LLM judgment rather than machine
// execution. They are not prose and must not be reported as nothing.
//
// ONE finding, not N: the lane is a property of the corpus, and N findings would
// drown the scan it rides on. It carries no check_id key because it describes a
// set rather than one check.
func llmOnlyDisclosure(llmOnly []corpus.Check) foundation.Finding {
	ids := make([]string, 0, len(llmOnly))
	for i, c := range llmOnly {
		if i >= MaxFindingsPerCheck {
			break
		}
		ids = append(ids, c.ID)
	}
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosureTitleLLMOnly,
		Summary: fmt.Sprintf("%d check node(s) in this corpus are accepted %s entries: prose with no deterministic expression, "+
			"which this scan did not execute and which need LLM judgment. They are reported here so a machine-verified "+
			"result is never mistaken for a complete one.", len(llmOnly), corpus.MetaLLMOnly),
		Evidence: ids,
		Metrics:  map[string]float64{"llm_only_total": float64(len(llmOnly))},
	}
}

// testFilesDisclosure reports that this run's walk reached test files, and how
// many, so a reader can tell a run that reached them from one that did not.
//
// IT IS A LEAD FINDING, emitted with the refusals and the llm_only disclosure
// ahead of every match finding, because the render ceilings keep the FIRST k
// findings: appended last, a bounded result would clip away the disclosure that
// tells the reader what the run's scope was.
//
// EMITTED ONLY WHEN THE COUNT IS NON-ZERO, exactly as the llm_only lane is. A
// run that reached no test file has nothing to disclose, and the verdict line
// renders the zero from the fold either way — so a default run's finding set is
// byte-for-byte what it was before this counter existed.
//
// THE LANGUAGE IS A PARAMETER because the sentence names it: "this language's
// test files" is what the verdict field and the generated help both promise, and
// what a test-file count means at all depends on whose convention decided which
// files are tests. It is the run's language rather than anything read off a
// check, since every check in a corpus shares it.
//
// THE COUNT IS THE LARGEST ANY ONE CHECK WALKED, not a sum across checks. Under
// per-check scope two checks may walk different file sets, and summing would
// report the same file several times and turn "five test files in this corpus"
// into fifteen. It is a reach, not a corpus census.
func testFilesDisclosure(language string, n int) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosureTitleTestFiles,
		Summary: fmt.Sprintf("this run's walk reached %d %s test file(s) — the most any single check scanned, since a check may declare that its "+
			"defect class lives in tests and walk wider than its neighbors. A run reporting zero here did not read your test code at all, "+
			"which is a different answer from finding nothing in it.", n, language),
		Metrics: map[string]float64{MetricTestFilesScanned: float64(n)},
	}
}

// emptyCorpusError names the language rather than returning an empty findings
// slice: a run that had nothing to execute must not read as a run that found
// nothing.
func emptyCorpusError(language string, set corpusSet) error {
	return fmt.Errorf("topology/%s: the %s graph holds no executable check for %s=%s%s — a scan that executed nothing is not a clean scan",
		AnalyzerName, kgtypes.GraphChecks, corpus.MetaLanguage, language, llmOnlySuffix(set))
}

// everyCheckRefusedError is the vacuous-pass closer: the admitted set was
// non-empty and every member was refused, so Run returns an error rather than a
// findings slice.
func everyCheckRefusedError(language string, set corpusSet, refused int) error {
	return fmt.Errorf("topology/%s: all %d check(s) in the %s graph for %s=%s were refused and none executed%s — see the refusal findings for each",
		AnalyzerName, refused, kgtypes.GraphChecks, corpus.MetaLanguage, language, llmOnlySuffix(set))
}

// llmOnlySuffix names the accepted-llm_only count on both whole-run errors.
// Telling an operator "no checks found for go" while the corpus held eleven
// accepted llm_only entries is a true sentence that produces a false belief.
func llmOnlySuffix(set corpusSet) string {
	if len(set.LLMOnly) == 0 {
		return ""
	}
	return fmt.Sprintf(" (the corpus does hold %d accepted %s entr%s, which are reported but not executed)",
		len(set.LLMOnly), corpus.MetaLLMOnly, plural(len(set.LLMOnly), "y", "ies"))
}

// plural picks a suffix without pulling in a dependency for one word.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// checkPathPrefixes renders req.PathPrefix as the one-element slice ast.Scope
// takes, or nil when no narrowing was asked for.
func checkPathPrefixes(prefix string) []string {
	if strings.TrimSpace(prefix) == "" {
		return nil
	}
	return []string{prefix}
}
