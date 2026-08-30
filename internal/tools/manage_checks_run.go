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
		TopK:       a.TopK,
		Language:   a.Language,
	}
	// An ABSENT key means "every check" and is legitimate; a present-but-empty
	// one is refused by the analyzer. So the key is set only when the caller
	// actually named ids.
	if len(a.IDs) > 0 {
		req.Extra = map[string]string{corpusscan.ExtraKeyChecks: strings.Join(a.IDs, ",")}
	}

	findings, err := analyzer.Run(ctx, req)
	if err != nil {
		// Relayed verbatim. The analyzer's refusals name the offending value and
		// the accepted vocabulary; re-wording them would drop what makes them
		// actionable.
		return errorResult("manage_checks run: " + err.Error())
	}

	body, rerr := foundation.RenderFindings(findings)
	if rerr != nil {
		return errorResult("manage_checks run: render findings: " + rerr.Error())
	}
	// THE VERDICT LEADS. The analyzer emits its refusals, disclosures and
	// truncation notices AHEAD of the match findings so a small top_k cannot clip
	// them; the verdict line follows the same rule for the same reason.
	return textResult(renderRunVerdict(corpusscan.ClassifyRun(findings)) + "\n" + body)
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

// renderRunVerdict renders the single machine-readable line.
//
// A REFUSED OR TRUNCATED RUN NEVER READS CLEAN. The classification comes from the
// verdict's own methods rather than being re-derived here, so this line and the
// CLI's exit status cannot disagree.
func renderRunVerdict(v corpusscan.RunVerdict) string {
	return fmt.Sprintf(
		"%s: %s  checks_flagged=%d sites_flagged=%d checks_refused=%d llm_only_not_executed=%d truncated=%t",
		corpusscan.AnalyzerName, RunVerdictToken(v),
		v.ChecksExecuted, v.SitesFlagged, v.ChecksRefused, v.LLMOnlyNotExecuted, v.Truncated)
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
