// SPDX-License-Identifier: Apache-2.0

// run.go orders one scan: probe once, then per check validate-then-execute,
// applying the two render ceilings and the ordering rule that keeps every
// disclosure ahead of the match findings it describes.

package corpusscan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
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
func runCorpus(ctx context.Context, req foundation.Request, set corpusSet, opts scanOptions) ([]foundation.Finding, error) {
	if len(set.Checks) == 0 {
		return nil, emptyCorpusError(req.Language, set)
	}
	probeErr := probeTempDir()

	var lead, matched []foundation.Finding
	executed := 0
	// scanZero keeps the FIRST check whose walk opened no file, with that
	// check's own stats and scope, so the refusal below can explain that walk.
	// testFilesScanned is the run's test-file reach.
	var scanZero *zeroScanWalk
	testFilesScanned := 0
	for _, entry := range set.Checks {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/%s: %w", AnalyzerName, err)
		}
		refusal, ok := admitCheck(ctx, set, entry.Check, probeErr)
		if !ok {
			lead = append(lead, refusal)
			continue
		}
		sites, walk, err := executeCheck(ctx, req, entry, opts)
		if err != nil {
			return nil, err
		}
		executed++
		if walk != nil {
			if walk.TestFilesScanned > testFilesScanned {
				testFilesScanned = walk.TestFilesScanned
			}
			if walk.FilesScanned == 0 && scanZero == nil {
				scanZero = &zeroScanWalk{check: entry.Check, stats: *walk, scope: checkScope(req, entry.Check, opts)}
			}
		}
		kept, notice := applyCheckCeiling(entry.Check, sites)
		if notice != nil {
			lead = append(lead, *notice)
		}
		matched = append(matched, kept...)
	}
	if testFilesScanned > 0 {
		lead = append(lead, testFilesDisclosure(req.Language, testFilesScanned))
	}
	if len(set.LLMOnly) > 0 {
		lead = append(lead, llmOnlyDisclosure(set.LLMOnly))
	}
	if executed == 0 {
		return nil, everyCheckRefusedError(req.Language, set, len(set.Checks))
	}
	// THE ZERO IS A PER-CHECK FACT, and it was not always. A caller-supplied
	// prefix is still the first conjunct, because an empty one means the whole
	// repo and a repo that legitimately holds no file of the language is a clean
	// answer rather than a mistyped scope. What changed is the second: checks no
	// longer share one scope, since a check may declare that its class lives in
	// test files and walk wider than its neighbors. So a run is refused when ANY
	// executed check opened no file, not only when every one of them did —
	// under the old run-level rule ONE widened check reaching a test file cleared
	// the guard for every narrow check that opened nothing, and those were then
	// folded into a CLEAN verdict, which is the vacuous green this refusal exists
	// to prevent. A graph-only corpus records no walk at all, so it is unaffected
	// and is never refused for scanning nothing.
	if strings.TrimSpace(req.PathPrefix) != "" && scanZero != nil {
		return nil, pathPrefixScannedNothingError(req, *scanZero)
	}
	return assembleFindings(req, lead, matched), nil
}

// zeroScanWalk is one check that opened no file: which check, the stats its own
// walk produced, and the scope that walk actually ran under.
//
// ALL THREE TRAVEL TOGETHER because the refusal has to explain THAT walk. The
// retired code kept only the most recent walk's stats, so with per-check scopes
// the numbers handed to the message could belong to a different check than the
// one that scanned nothing.
type zeroScanWalk struct {
	check corpus.Check
	stats ast.WalkStats
	scope ast.Scope
}

// pathPrefixScannedNothingError is the scope-side vacuous-pass closer: a
// path_prefix that reached no file of the corpus language is refused, because a
// mistyped scope would otherwise render as a clean corpus.
//
// THE MESSAGE IS DELEGATED to ast.ZeroScanHint rather than written a fourth
// time. That function distinguishes the causes of a zero scan in precedence
// order. Three of them are reachable here — a discovery rule declined the
// files, the walk's own test-file filter took them, or package_prefixes matched
// none. Its fourth, the wrong root, is not: this refusal fires only for a
// non-empty path_prefix, so the scope it hands the hint always carries a
// package_prefixes entry and the prefix branch always wins. It already names
// path-SEGMENT matching, which is the mistake this refusal exists to catch.
//
// IT REASONS ABOUT THE WALK THAT RAN. Both the stats and the SCOPE come from the
// check that scanned nothing rather than being rebuilt here from the request.
// The stats are what changes the message today: the test-file cause is read off
// TestFilesExcluded, which is why a prefix naming only test files is no longer
// refused with "no go files under package_prefixes" — a false cause for files
// that exist, are tracked, and are of the right language. The SCOPE matters for
// a different reason: the hint reads package_prefixes off it, and a second
// hand-built literal here was free to drift from the one the walk used. Under
// per-check scope those two literals no longer even describe the same walk, so
// the one the walk ran under is the only honest thing to hand it.
func pathPrefixScannedNothingError(req foundation.Request, zero zeroScanWalk) error {
	hint := ast.ZeroScanHint(req.RepoRoot, req.Language, zero.scope, zero.stats)
	return fmt.Errorf("topology/%s: check %q scanned no file — %s — a scan that reached no file is not a clean scan, so this run is refused rather than reported as clean",
		AnalyzerName, zero.check.ID, hint)
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

// executeCheck dispatches one admitted check to the executor that owns its type,
// relaying the walk stats when the executor walked the tree.
//
// THE GRAPH ARM RETURNS A NIL WalkStats, and that nil is the signal rather than
// an omission: graph_assertion and topology_threshold read the graph, not the
// tree, so they open no file BY DESIGN and a run made of them must never be
// refused for having scanned nothing.
//
// flow_model has no executor in this tree: its facts are flow-fact edges over
// CALLS filtered through source/sink model nodes, and neither the edges nor the
// model vocabulary are landed. It cannot reach here — the gate refuses it first
// with the contract's ErrNoExecutor — so this arm exists to keep the seam NAMED
// rather than to run. If a future dispatch ever lets one through, it errors
// naming the check instead of returning a silent clean zero.
func executeCheck(ctx context.Context, req foundation.Request, entry corpusEntry, opts scanOptions) ([]foundation.Finding, *ast.WalkStats, error) {
	switch entry.Check.Type {
	case corpus.CheckAstPattern:
		return executeAstCheck(ctx, req, entry, opts)
	case corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold:
		sites, err := executeGraphCheck(ctx, req, entry)
		return sites, nil, err
	default:
		return nil, nil, fmt.Errorf("topology/%s: check %q declares %s=%s, which has no executor in this tree — refusing rather than reporting a clean scan",
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
//
// A POSITIVE TopK HERE BOUNDS A RENDER FOR CALLERS THAT DERIVE NO VERDICT, and
// that restriction is what keeps the two sentences above safe to read. This
// analyzer's verdict-bearing caller — manage_checks run — does NOT set
// Request.TopK: it folds its classification over the complete finding set and
// applies the caller's cap to its own render afterwards. The only caller that
// still passes a TopK is the topology dispatcher (tools/intercept_topology.go
// runLocalTopology), which renders findings and classifies nothing. A cap must
// never be reintroduced on a path whose output is CLASSIFIED: clipping before
// the fold is precisely how a dirty corpus came to report CLEAN once a verdict
// was layered on top of a silent cap.
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
