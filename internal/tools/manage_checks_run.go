// SPDX-License-Identifier: Apache-2.0

// manage_checks_run.go — executing checks against a repo's working tree.
//
// THE RUNNER IS NOT REBUILT HERE. corpus_scan is a registered foundation
// analyzer and it already implements every selector this operation exposes: the
// id subset through its one Extra key, the corpus language filter through
// Request.Language, and the subtree narrowing through Request.PathPrefix. This
// file shapes the request, relays the findings through the same renderer the
// topology dispatcher uses, and prepends the verdict line.
//
// IT STORES NOTHING. No result nodes are written and no history is kept; the
// shape of a stored result is an open question this surface does not answer.

package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// manageChecksRun executes the selected checks and renders the verdict plus the
// findings.
func manageChecksRun(ctx context.Context, deps ClientDeps, gc GraphCaller, a manageChecksArgs) kgtools.ToolResult {
	if strings.TrimSpace(a.Repo) == "" {
		return errorResult("manage_checks run: repo is required — it names both the code graph and the tree the checks walk")
	}
	// A NEGATIVE CAP IS REFUSED BEFORE ANYTHING IS SCANNED, and the comparison is
	// strictly less than zero on purpose: ZERO IS THE DOCUMENTED NO-CAP VALUE, so
	// refusing it would break every uncapped call. A negative is a different
	// answer entirely — foundation.TruncateTopK returns its input untouched at or
	// below zero, so accepting -1 would make it a second, undocumented spelling of
	// "no cap" rather than the value the caller actually wrote.
	if a.TopK < 0 {
		return errorResult(fmt.Sprintf(
			"manage_checks run: top_k=%d is not admitted — it caps how many findings are rendered, so the admitted range is 0 (no cap) or a positive count",
			a.TopK))
	}
	// The registry lookup is resolved through the exported constant, never a
	// hand-typed name: the registered identifier carries an underscore while the
	// Go package does not, and a literal one character off is refused by the
	// registry outright.
	analyzer, ok := foundation.Get(corpusscan.AnalyzerName)
	if !ok {
		return errorResult(fmt.Sprintf(
			"manage_checks run: analyzer %q is not registered, so no scan was performed — this is a build defect, not a clean corpus",
			corpusscan.AnalyzerName))
	}
	// The SAME resolution the topology dispatcher installs, under this tool's own
	// name. Not reimplemented: a second resolver is a second answer to where the
	// walk runs.
	repoRoot, err := resolveRepoDir(ctx, deps, "manage_checks run", a.Repo)
	if err != nil {
		return errorResult(err.Error())
	}

	req := foundation.Request{
		Caller:     gc,
		Graph:      kgtypes.GraphCode,
		Name:       codeGraphInstanceName(a.Repo),
		RepoRoot:   repoRoot,
		PathPrefix: a.PathPrefix,
		Language:   a.Language,
	}
	// An ABSENT key means "every check" and is legitimate; a present-but-empty
	// one is refused by the analyzer. So the key is set only when the caller
	// actually named ids.
	if len(a.IDs) > 0 {
		req.Extra = map[string]string{corpusscan.ExtraKeyChecks: strings.Join(a.IDs, ",")}
	}
	// THE TRI-STATE SURVIVES TO THE ANALYZER. An omitted flag leaves the key
	// absent, which is what makes "the caller never asked" distinguishable from
	// "the caller asked for false" — and the analyzer refuses only the explicit
	// forms for a language with no test-file convention. Collapsing this to a
	// plain bool here would hand every caller of an unsupported language a
	// silent false they never wrote.
	if a.IncludeTests != nil {
		if req.Extra == nil {
			req.Extra = map[string]string{}
		}
		req.Extra[corpusscan.ExtraKeyIncludeTests] = strconv.FormatBool(*a.IncludeTests)
	}

	findings, err := analyzer.Run(ctx, req)
	if err != nil {
		// Relayed verbatim. The analyzer's refusals name the offending value and
		// the accepted vocabulary; re-wording them would drop what makes them
		// actionable.
		return errorResult("manage_checks run: " + err.Error())
	}

	// CLASSIFY BEFORE CLIPPING, and the order of these three statements is the
	// whole fix. The fold runs over the COMPLETE set of findings the analyzer
	// produced — which is why top_k is deliberately absent from the Request
	// above, leaving the analyzer to apply only its own ceilings — and the
	// caller's cap is applied afterwards, to the render alone. Folding a
	// render-side slice was the defect: a cap of one over a dirty corpus reported
	// CLEAN with zero counts, because the findings that would have flagged it had
	// already been clipped away before the classifier ever saw them.
	v := corpusscan.ClassifyRun(findings)
	rendered := foundation.TruncateTopK(findings, a.TopK)
	body, rerr := foundation.RenderFindings(rendered)
	if rerr != nil {
		return errorResult("manage_checks run: render findings: " + rerr.Error())
	}
	return textResult(renderRunVerdict(v, len(rendered), len(findings)) + body)
}

// The verdict tokens. They are the machine-readable half of the line, so they are
// constants a consumer can cite rather than words a reader has to parse.
const (
	// VerdictClean means every selected check executed, nothing was withheld,
	// and no site was flagged.
	VerdictClean = "CLEAN"
	// VerdictFlagged means the run completed and flagged at least one site.
	VerdictFlagged = "FLAGGED"
	// VerdictInconclusive means the run did NOT deliver a complete answer: a
	// check was refused, or output was withheld by a render ceiling. It is
	// deliberately distinct from FLAGGED — a flagged run answered the question
	// and the answer was bad, an inconclusive one did not answer it.
	VerdictInconclusive = "INCONCLUSIVE"
)

// renderRunVerdict renders the single machine-readable line, plus the count line
// when the caller's cap withheld findings from the body. rendered is how many
// findings this render carries and total is how many the run produced.
//
// THE TOKEN AND THE truncated FIELD ANSWER DIFFERENT QUESTIONS, and keeping them
// apart is the point of taking two counts here. The TOKEN answers whether the
// VERDICT is complete: it is folded over every finding the run produced, so a
// render cap can move neither it nor the counts beside it. The FIELD answers
// whether the BODY is complete, and it is COMPUTED from a rendered-versus-total
// comparison rather than inferred from the presence of a truncation notice — the
// inference was the original defect, since a notice the analyzer never emitted
// cannot set a flag. The comparison stays OUT of RunVerdict deliberately:
// RunVerdict.Truncated feeds Inconclusive(), which outranks Flagged, so folding
// a render clip into the struct would report a flagged corpus as INCONCLUSIVE.
//
// A REFUSED OR TRUNCATED RUN NEVER READS CLEAN. The classification comes from the
// verdict's own methods rather than being re-derived here, so this line and the
// CLI's exit status cannot disagree.
//
// test_files_scanned IS A SCOPE FACT, NOT A COMPLETENESS ONE, which is why it is
// rendered beside the counters and reaches neither the token nor truncated. A
// run that deliberately excluded test files answered the question it was asked;
// it just answered a narrower one, and the number is how a reader can tell.
func renderRunVerdict(v corpusscan.RunVerdict, rendered, total int) string {
	clipped := rendered < total
	line := fmt.Sprintf(
		"%s: %s  checks_flagged=%d sites_flagged=%d checks_refused=%d llm_only_not_executed=%d test_files_scanned=%d truncated=%t\n",
		corpusscan.AnalyzerName, RunVerdictToken(v),
		v.ChecksExecuted, v.SitesFlagged, v.ChecksRefused, v.LLMOnlyNotExecuted, v.TestFilesScanned, v.Truncated || clipped)
	if clipped {
		line += fmt.Sprintf("returning %d of %d findings\n", rendered, total)
	}
	return line
}

// RunVerdictToken maps a verdict onto its token. INCONCLUSIVE outranks FLAGGED: a run that
// could not execute part of its corpus has not established that the sites it did
// flag are all of them.
//
// EXPORTED FOR ONE CALLER: the CLI's cross-face test drives the SAME shared
// table of findings through this token and through the subcommand's exit
// status, which is how "one classification, two faces" is asserted
// behaviorally rather than grepped for.
func RunVerdictToken(v corpusscan.RunVerdict) string {
	switch {
	case v.Inconclusive():
		return VerdictInconclusive
	case v.Clean():
		return VerdictClean
	default:
		return VerdictFlagged
	}
}
