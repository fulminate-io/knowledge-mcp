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
	executed, refused, outOfScope := 0, 0, 0
	var walks walkAccounting
	for _, entry := range set.Checks {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/%s: %w", AnalyzerName, err)
		}
		adm := admitAndScope(ctx, req, set, opts, entry, probeErr)
		if adm.lead != nil {
			lead = append(lead, *adm.lead)
			if adm.refused {
				refused++
			} else {
				outOfScope++
			}
			continue
		}
		dec := adm.dec
		sites, disclosures, walk, err := executeCheck(ctx, req, entry, opts, dec)
		if err != nil {
			return nil, err
		}
		// A SCOPED WALK THAT OPENED NOTHING IS THE CHECK'S SCOPE, NOT THE
		// CALLER'S, and the two need different answers. The caller's mistyped
		// prefix is refused below, because a scan that read nothing is not a clean
		// scan; a check scoped to a subtree this repository does not have is
		// simply not applicable here, and refusing the whole run for it would make
		// one scoped corpus unusable across repositories — which is exactly what
		// the scope keys exist to support. It is disclosed by name either way.
		if dec.narrowed && walk != nil && walk.FilesScanned == 0 {
			lead = append(lead, outOfScopeDisclosure(entry.Check.ID, dec, reasonScopeReachedNoFile(req.Language)))
			outOfScope++
			continue
		}
		executed++
		if dec.narrowed {
			lead = append(lead, scopeAppliedDisclosure(entry.Check.ID, dec))
		}
		lead = append(lead, disclosures...)
		walks.observe(req, entry, opts, dec, walk)
		kept, notice := applyCheckCeiling(entry.Check, sites)
		if notice != nil {
			lead = append(lead, *notice)
		}
		matched = append(matched, kept...)
	}
	if walks.testFilesScanned > 0 {
		lead = append(lead, testFilesDisclosure(req.Language, walks.testFilesScanned))
	}
	// A NAMED PATH OF ANOTHER LANGUAGE IS DROPPED AND SAID SO. It is the one arm
	// of the file-list scope that neither scans nor refuses, so the disclosure is
	// the whole of its accounting: without it the path would be absent from the
	// walk and from the report both, which is the silent drop this scope closes.
	if opts.files != nil && len(opts.files.other) > 0 {
		lead = append(lead, otherLanguageDisclosure(req.Language, opts.files.other))
	}
	if len(set.LLMOnly) > 0 {
		lead = append(lead, llmOnlyDisclosure(set.LLMOnly))
	}
	if executed == 0 {
		// THE SCOPE ARM IS A SEPARATE SENTENCE BECAUSE IT IS A DIFFERENT FACT. A
		// corpus that could not execute is a corpus to fix; a corpus that was
		// entirely out of scope is a run to re-aim. Both are refusals — neither is
		// a clean scan — and the scope arm is what stops a mistyped caller scope
		// from hiding behind an all-scoped corpus, where every check would be
		// skipped without a walk and so would never reach the zero-scan guard.
		if outOfScope > 0 {
			return nil, everyCheckOutOfScopeError(req, set, opts, refused, outOfScope)
		}
		return nil, everyCheckRefusedError(req.Language, set, refused)
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
	//
	// THE FIRST CONJUNCT ADMITS BOTH SCOPE CHANNELS, and keying it on path_prefix
	// alone was a hole rather than a subtlety: a file-list run leaves PathPrefix
	// empty BY CONSTRUCTION, so a file-list scan whose checks opened nothing
	// rendered a verdict over files nobody read — the exact vacuous green this
	// refusal exists to prevent, reached through the newer of the two channels.
	//
	// A CHECK NARROWED BY ITS OWN DECLARED SCOPE NEVER REACHES HERE, and that is
	// the one exception, taken in the loop above rather than by a condition here.
	// The two zeros have different causes and different remedies: a caller's
	// scope that reached nothing is a mistyped call, while a check scoped to a
	// subtree this repository does not have is simply not applicable to it, and
	// refusing the whole run for the second would make one scoped corpus unusable
	// across repositories. It is disclosed by name either way, so neither is ever
	// folded into a clean verdict.
	if (strings.TrimSpace(req.PathPrefix) != "" || opts.files != nil) && walks.zero != nil {
		return nil, scopeScannedNothingError(req, *walks.zero)
	}
	// THE SHORTFALL IS THE ZERO'S LESS SEVERE SIBLING and it is checked second
	// because the zero can explain its own cause and this can only count. A walk
	// that opened three of four named files has silently dropped one; reporting
	// the three as clean is the same defect as reporting a zero as clean.
	if walks.shortfall != nil {
		return nil, walks.shortfall
	}
	return assembleFindings(req, lead, matched), nil
}

// checkAdmission is the answer to the two questions asked of every check before
// it walks anything: may it run, and under what scope.
//
// IT IS ONE VALUE RATHER THAN TWO CALLS IN THE LOOP because the two answers have
// the same shape — a lead finding and a skip — and differ only in which counter
// they move. Keeping them together is also what keeps the caller's own arm to
// one branch, so a later reader can see the whole per-check decision in one
// place rather than reconstructing it from three consecutive continues.
type checkAdmission struct {
	// dec is the scope the check runs under. It is meaningful only when lead is
	// nil; on a skip it carries whatever was resolved before the skip.
	dec checkScopeDecision
	// lead is the refusal or the disclosure to emit, and its presence IS the
	// skip. Nil means the check runs.
	lead *foundation.Finding
	// refused tells the two skips apart for the counters: a refusal means the
	// scan could not do what it was asked, while an out-of-scope check is doing
	// exactly what its author declared. They are counted separately because only
	// the first makes a run INCONCLUSIVE.
	refused bool
}

// admitAndScope runs the fixture gate, then resolves the check's declared scope.
//
// THE ORDER IS LOAD-BEARING. The gate first, because it is the first authority
// on whether a check is executable at all and a check with broken fixtures
// should report that rather than its scope. The scope BEFORE the walk, because
// the whole requirement is that a scoped check never opens a file outside its
// scope, and a narrowing applied to the findings afterwards would have read
// them.
func admitAndScope(ctx context.Context, req foundation.Request, set corpusSet, opts scanOptions, entry corpusEntry, probeErr error) checkAdmission {
	if refusal, ok := admitCheck(ctx, set, entry.Check, probeErr); !ok {
		return checkAdmission{lead: &refusal, refused: true}
	}
	dec, err := resolveCheckScope(req, opts, entry)
	if err != nil {
		refusal := refusalFinding(entry.Check, RefusalPrefixUnvalidated+entry.Check.ID, classifyScope, err)
		return checkAdmission{dec: dec, lead: &refusal, refused: true}
	}
	if !dec.applies {
		disclosure := outOfScopeDisclosure(entry.Check.ID, dec, scopeExclusionReason(req, dec))
		return checkAdmission{dec: dec, lead: &disclosure}
	}
	return checkAdmission{dec: dec}
}

// walkAccounting is what the run's two scope guards and its test-file
// disclosure read off the per-check walks, accumulated as they happen.
//
// IT IS A VALUE RATHER THAN THREE LOCALS because all three are FIRST-SEEN or
// HIGH-WATER facts about a loop, and three independent "have I already recorded
// one" branches inside that loop is where a later reader stops being able to
// tell which of them a given walk updated.
type walkAccounting struct {
	// testFilesScanned is the HIGH-WATER mark across checks, never a sum: under
	// per-check scope two checks may walk different file sets, and summing would
	// report the same file several times.
	testFilesScanned int
	// zero is the FIRST check whose walk opened no file, carrying that check's
	// own stats and scope so the refusal can explain THAT walk rather than one
	// rebuilt from the request.
	zero *zeroScanWalk
	// shortfall is the first walk that opened some but not all of the named
	// files.
	shortfall error
}

// observe folds one check's walk into the accounting. A nil walk is the graph
// arm's signal that it read the graph rather than the tree, and it must leave
// every field untouched: a run made of graph checks opens no file BY DESIGN and
// is never refused for it.
func (a *walkAccounting) observe(req foundation.Request, entry corpusEntry, opts scanOptions, dec checkScopeDecision, walk *ast.WalkStats) {
	if walk == nil {
		return
	}
	if walk.TestFilesScanned > a.testFilesScanned {
		a.testFilesScanned = walk.TestFilesScanned
	}
	if walk.FilesScanned == 0 && a.zero == nil {
		a.zero = &zeroScanWalk{check: entry.Check, stats: *walk, scope: checkScope(req, entry.Check, opts, dec)}
	}
	if a.shortfall == nil {
		// THE EXPECTATION IS PER CHECK, not per run. A check's own path scope
		// legitimately excludes named files, so comparing every check against the
		// whole named list would refuse a correctly narrowed run as a silent drop.
		a.shortfall = fileScopeShortfall(opts.files, opts.files.expectedUnder(dec.prefixes), entry.Check.ID, *walk)
	}
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

// scopeScannedNothingError is the scope-side vacuous-pass closer: a scope that
// reached no file of the corpus language is refused, because a mistyped one
// would otherwise render as a clean corpus. It covers BOTH scope channels — a
// path_prefix and a file list — since both narrow the same walk and both can
// narrow it to nothing.
//
// THE MESSAGE IS DELEGATED to ast.ZeroScanHint rather than written a fourth
// time. That function distinguishes the causes of a zero scan in precedence
// order. Which of them are reachable now depends on the channel. Under a
// path_prefix, three are: a discovery rule declined the files, the walk's own
// test-file filter took them, or package_prefixes matched none. UNDER A FILE
// LIST ONLY THE LAST IS, because that scope lifts discovery's rules and the
// test-file filter for the paths it names — so the remaining way to reach zero
// is a named set holding no file of the corpus language, which is exactly what
// the prefix branch says. The fourth cause, a wrong root, stays unreachable
// through either channel: both always hand the hint a non-empty
// package_prefixes, so the prefix branch always wins over it.
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
func scopeScannedNothingError(req foundation.Request, zero zeroScanWalk) error {
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
// relaying the walk stats when the executor walked the tree, plus any LEAD
// findings the execution produced.
//
// THE SECOND RETURN IS LEAD, NOT SITES, and keeping them apart is the ordering
// rule this file opens with: a disclosure appended to the match findings would
// be clipped by a small top_k, which is precisely the silent cap the analyzer's
// self-bounding rule forbids. The graph arm produces one — a check whose
// candidates all fall outside a file-list scope did NOT run, and saying so is
// not a flagged site.
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
func executeCheck(ctx context.Context, req foundation.Request, entry corpusEntry, opts scanOptions, dec checkScopeDecision) ([]foundation.Finding, []foundation.Finding, *ast.WalkStats, error) {
	switch entry.Check.Type {
	case corpus.CheckAstPattern:
		sites, walk, err := executeAstCheck(ctx, req, entry, opts, dec)
		return sites, nil, walk, err
	case corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold:
		sites, lead, err := executeGraphCheck(ctx, req, entry, opts, dec)
		return sites, lead, nil, err
	default:
		return nil, nil, nil, fmt.Errorf("topology/%s: check %q declares %s=%s, which has no executor in this tree — refusing rather than reporting a clean scan",
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
