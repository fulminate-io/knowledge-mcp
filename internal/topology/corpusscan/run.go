// SPDX-License-Identifier: Apache-2.0

// run.go orders one scan: probe once, then per check validate-then-execute,
// applying the two render ceilings and the ordering rule that keeps every
// disclosure ahead of the match findings it describes.

package corpusscan

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// runCorpus executes an already-read corpus.
//
// ORDERING IS A CORRECTNESS REQUIREMENT, NOT PRESENTATION. foundation.TruncateTopK
// keeps the FIRST k findings, so refusals, the llm_only disclosure and the
// truncation notices are emitted AHEAD of every match finding — otherwise a
// caller passing a small top_k would clip away the very disclosures that make a
// bounded result honest, which is the silent cap this analyzer's self-bounding
// rule forbids. The natural instinct is to append them last; do not.
//
// The environment is probed ONCE up front, then validation and execution
// interleave per check. Interleaving buys no caller-visible progress — Run
// returns one slice and does not stream — but it does mean a corpus whose LAST
// check fails validation has already scanned the ones before it.
func runCorpus(ctx context.Context, req foundation.Request, set corpusSet) ([]foundation.Finding, error) {
	if len(set.Checks) == 0 {
		return nil, emptyCorpusError(req.Language, set)
	}
	probeErr := probeTempDir()

	var lead, matched []foundation.Finding
	executed := 0
	for _, entry := range set.Checks {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/%s: %w", AnalyzerName, err)
		}
		refusal, ok := admitCheck(ctx, set, entry.Check, probeErr)
		if !ok {
			lead = append(lead, refusal)
			continue
		}
		sites, err := executeCheck(ctx, req, entry)
		if err != nil {
			return nil, err
		}
		executed++
		kept, notice := applyCheckCeiling(entry.Check, sites)
		if notice != nil {
			lead = append(lead, *notice)
		}
		matched = append(matched, kept...)
	}
	if len(set.LLMOnly) > 0 {
		lead = append(lead, llmOnlyDisclosure(set.LLMOnly))
	}
	if executed == 0 {
		return nil, everyCheckRefusedError(req.Language, set, len(set.Checks))
	}
	return assembleFindings(req, lead, matched), nil
}

// admitCheck runs the gate for one check, returning the refusal finding when the
// check may not execute. The bool is the admission answer; a false always comes
// with a finding naming the check.
func admitCheck(ctx context.Context, set corpusSet, c corpus.Check, probeErr error) (foundation.Finding, bool) {
	if probeErr != nil {
		return environmentRefusal(c, probeErr), false
	}
	bad, good, err := resolveFixtures(set, c)
	if err != nil {
		return refusalFinding(c, RefusalPrefixUnvalidated+c.ID, classifyFixtureBind, err), false
	}
	if err := validateEntryFixtures(ctx, c, bad, good); err != nil {
		return classifyRefusal(c, err), false
	}
	return foundation.Finding{}, true
}

// executeCheck dispatches one admitted check to the executor that owns its type.
//
// flow_model has no executor in this tree: its facts are flow-fact edges over
// CALLS filtered through source/sink model nodes, and neither the edges nor the
// model vocabulary are landed. It cannot reach here — the gate refuses it first
// with the contract's ErrNoExecutor — so this arm exists to keep the seam NAMED
// rather than to run. If a future dispatch ever lets one through, it errors
// naming the check instead of returning a silent clean zero.
func executeCheck(ctx context.Context, req foundation.Request, entry corpusEntry) ([]foundation.Finding, error) {
	switch entry.Check.Type {
	case corpus.CheckAstPattern:
		return executeAstCheck(ctx, req, entry)
	case corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold:
		return executeGraphCheck(ctx, req, entry)
	default:
		return nil, fmt.Errorf("topology/%s: check %q declares %s=%s, which has no executor in this tree — refusing rather than reporting a clean scan",
			AnalyzerName, entry.Check.ID, corpus.MetaCheckType, entry.Check.Type)
	}
}

// applyCheckCeiling clips one check's match findings to MaxFindingsPerCheck and
// returns the truncation notice when it fired. The notice carries the TRUE total
// so a reader never has to infer how much was withheld.
func applyCheckCeiling(c corpus.Check, sites []foundation.Finding) ([]foundation.Finding, *foundation.Finding) {
	if len(sites) <= MaxFindingsPerCheck {
		return sites, nil
	}
	notice := foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityNotice,
		Title:     TruncationPrefixCheck + c.ID,
		Summary: fmt.Sprintf("check %q matched %d site(s); the first %d are rendered and the rest are withheld by this analyzer's per-check render ceiling",
			c.ID, len(sites), MaxFindingsPerCheck),
		Evidence: []string{c.ID},
		Metrics: map[string]float64{
			"matches_total":    float64(len(sites)),
			"matches_rendered": float64(MaxFindingsPerCheck),
		},
		Metadata: map[string]string{MetaKeyCheckID: c.ID},
	}
	return sites[:MaxFindingsPerCheck], &notice
}

// assembleFindings applies the run ceiling and the caller's TopK, preserving the
// ordering rule in both directions.
//
// When TopK is positive the caller has asked for a bound and gets it; the run
// ceiling is skipped so two caps never compound into a number neither of them
// named. Because the lead findings come first, a TopK handoff cannot clip a
// disclosure.
func assembleFindings(req foundation.Request, lead, matched []foundation.Finding) []foundation.Finding {
	if req.TopK <= 0 && len(matched) > MaxFindingsTotal {
		total := len(matched)
		matched = matched[:MaxFindingsTotal]
		lead = append(lead, foundation.Finding{
			Algorithm: AnalyzerName,
			Severity:  foundation.SeverityNotice,
			Title:     TruncationTitleRun,
			Summary: fmt.Sprintf("this run matched %d site(s) across the corpus; the first %d are rendered and the rest are withheld by the run-level render ceiling",
				total, MaxFindingsTotal),
			Metrics: map[string]float64{
				"findings_total":    float64(total),
				"findings_rendered": float64(MaxFindingsTotal),
			},
		})
	}
	out := make([]foundation.Finding, 0, len(lead)+len(matched))
	out = append(out, lead...)
	out = append(out, matched...)
	return foundation.TruncateTopK(out, req.TopK)
}

// sortSites orders one check's findings by (file, line) so the render is
// diffable run to run and the per-check ceiling always keeps the same prefix.
func sortSites(sites []foundation.Finding) {
	sort.SliceStable(sites, func(i, j int) bool {
		fi, fj := sites[i].Metadata[MetaKeyFile], sites[j].Metadata[MetaKeyFile]
		if fi != fj {
			return fi < fj
		}
		return sites[i].Metrics["line"] < sites[j].Metrics["line"]
	})
}
