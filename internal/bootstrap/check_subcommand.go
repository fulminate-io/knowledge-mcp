// SPDX-License-Identifier: Apache-2.0

// check_subcommand.go — `knowledge check run`, the SHELL FACE of the corpus-check
// classification.
//
// WHY IT EXISTS SEPARATELY FROM THE MCP TOOL. A plan criterion is a shell command
// and reads an EXIT STATUS; an MCP tool result is text and has none. So the tool's
// verdict line and this subcommand are two faces of ONE classification, never two
// classifications — the exit status is computed by corpusscan.ClassifyRun, the
// same fold the verdict line uses, and this file performs no classification of
// its own. It does not re-read the title constants, it does not count severities,
// and above all it does not parse the rendered verdict text: that would look like
// reuse while coupling the exit code to a display format.

package bootstrap

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The exit codes.
//
// 3 AND 4, NOT 1 AND 2, and the reason is a collision rather than taste:
// subcommandExit already maps a generic error to 1 and a missing session to
// cli.ExitNoValidSession = 2 for EVERY subcommand. Reusing either would make
// "checks flagged" indistinguishable from "the command failed" or "you are
// logged out", which is exactly the confusion a gate must not create.
//
// THEY ARE TWO CODES RATHER THAN ONE NON-ZERO because a gate that cannot tell a
// real finding from a probe that could not run is the defect class this repo's
// criteria discipline exists to prevent: an author would read a refused corpus as
// a caught defect.
const (
	// ExitCheckFlagged means the run completed and flagged at least one site.
	ExitCheckFlagged = 3
	// ExitCheckInconclusive means the run could NOT answer: a check was refused,
	// or a render ceiling truncated the output. It is not a clean corpus.
	ExitCheckInconclusive = 4
)

// The sentinels subcommandExit maps with errors.Is — the same mechanism the
// no-valid-session code already uses, in the same switch.
var (
	errCheckFlagged      = errors.New("corpus checks flagged at least one site")
	errCheckInconclusive = errors.New("the corpus scan could not answer: a check was refused or the output was truncated")
)

// checkVerbs is the admitted verb set, sorted, and sized by construction.
//
// IT STAYS AT ONE DELIBERATELY. create and list have no shell consumer, and a CLI
// mirroring the whole tool would double the surface for no gate.
var checkVerbs = []string{"run"}

// runCheckVerb dispatches the check subcommand's verb.
func runCheckVerb(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("check: expected a verb — admitted: %s", strings.Join(sortedCheckVerbs(), ", "))
	}
	switch args[0] {
	case "run":
		return runCheckRun(args[1:])
	default:
		return fmt.Errorf("check: unknown verb %q — admitted: %s", args[0], strings.Join(sortedCheckVerbs(), ", "))
	}
}

// sortedCheckVerbs renders the admitted set at call time rather than as a second
// hand-written list, so the refusal message cannot enumerate a vocabulary the
// dispatch does not actually have.
func sortedCheckVerbs() []string {
	out := append([]string(nil), checkVerbs...)
	sort.Strings(out)
	return out
}

// checkRunFlags are the flags `knowledge check run` accepts.
type checkRunFlags struct {
	repo       string
	language   string
	pathPrefix string
	port       int
	ids        []string
}

// parseCheckRunFlags parses the flag set, taking any positional arguments as
// check ids.
func parseCheckRunFlags(args []string) (checkRunFlags, error) {
	var f checkRunFlags
	fs := flag.NewFlagSet("check run", flag.ContinueOnError)
	fs.StringVar(&f.repo, "repo", "", "code-graph name or absolute checkout path (required)")
	fs.StringVar(&f.language, "language", "", "tree-sitter language slug selecting the checks corpus (required)")
	fs.StringVar(&f.pathPrefix, "path-prefix", "", "repo-relative subtree to narrow the walk to")
	fs.IntVar(&f.port, "port", graphclient.DefaultPort, "port the knowledge-server is listening on")
	if err := fs.Parse(args); err != nil {
		return checkRunFlags{}, err
	}
	f.ids = fs.Args()
	if strings.TrimSpace(f.repo) == "" {
		return checkRunFlags{}, errors.New("check run: --repo is required — it names both the code graph and the tree the checks walk")
	}
	if strings.TrimSpace(f.language) == "" {
		return checkRunFlags{}, errors.New("check run: --language is required — it selects the checks corpus and there is no default")
	}
	return f, nil
}

// runCheckRun executes the selected checks and returns the sentinel matching the
// verdict.
func runCheckRun(args []string) error {
	f, err := parseCheckRunFlags(args)
	if err != nil {
		return err
	}
	// The registered identifier carries an UNDERSCORE while the Go package does
	// not, so it is resolved through the exported constant everywhere — a literal
	// one character off is refused by the registry outright.
	analyzer, ok := foundation.Get(corpusscan.AnalyzerName)
	if !ok {
		return fmt.Errorf("check run: analyzer %q is not registered, so no scan was performed — this is a build defect, not a clean corpus",
			corpusscan.AnalyzerName)
	}
	repoRoot, err := resolveCheckRepoRoot(f.repo)
	if err != nil {
		return err
	}

	gc := graphclient.NewGraphClient(f.port)
	// HEALTH-GATE FIRST. A daemon that is not up must say so; reporting a clean
	// scan when the corpus could not even be read is the vacuous green this whole
	// surface exists to prevent.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	healthy := gc.HealthyCtx(ctx)
	cancel()
	if !healthy {
		return fmt.Errorf("check run: knowledge-server is not running on port %d, so the checks corpus could not be read", f.port)
	}

	req := foundation.Request{
		Caller:     gc,
		Graph:      kgtypes.GraphCode,
		Name:       checkGraphInstanceName(f.repo),
		RepoRoot:   repoRoot,
		PathPrefix: f.pathPrefix,
		Language:   f.language,
	}
	// An ABSENT key means "every check"; a present-but-empty one is refused by
	// the analyzer, so the key is set only when ids were actually named.
	if len(f.ids) > 0 {
		req.Extra = map[string]string{corpusscan.ExtraKeyChecks: strings.Join(f.ids, ",")}
	}

	findings, err := analyzer.Run(context.Background(), req)
	if err != nil {
		return fmt.Errorf("check run: %w", err)
	}
	return reportCheckRun(findings)
}

// reportCheckRun prints the findings and returns the sentinel for the verdict.
//
// THE CLASSIFICATION IS NOT MADE HERE. corpusscan.ClassifyRun and the verdict's
// own methods decide; this maps their answer onto an exit status.
func reportCheckRun(findings []foundation.Finding) error {
	body, rerr := foundation.RenderFindings(findings)
	if rerr != nil {
		return fmt.Errorf("check run: render findings: %w", rerr)
	}
	v := corpusscan.ClassifyRun(findings)
	fmt.Fprintf(os.Stdout,
		"%s: checks_flagged=%d sites_flagged=%d checks_refused=%d llm_only_not_executed=%d truncated=%t\n%s\n",
		corpusscan.AnalyzerName, v.ChecksExecuted, v.SitesFlagged, v.ChecksRefused, v.LLMOnlyNotExecuted, v.Truncated, body)
	switch {
	case v.Inconclusive():
		return errCheckInconclusive
	case v.Clean():
		return nil
	default:
		return errCheckFlagged
	}
}

// resolveCheckRepoRoot resolves the repo argument to the tree the checks walk,
// through the SAME core the MCP path uses.
//
// The CLI's own working directory is the base and it is always explicit — a
// person typing this command is standing somewhere on purpose — and there is no
// session cwd on this path.
func resolveCheckRepoRoot(repo string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("check run: resolve the working directory: %w", err)
	}
	return tools.ResolveRepoDirCore(cwd, true, "", "check run", repo)
}

// checkGraphInstanceName derives the code-GRAPH instance name from the repo
// argument, matching what the MCP path does: the argument names the graph AND is
// the source of the walk root, and those diverge for an absolute path, where the
// basename is the name collect recorded for that directory.
func checkGraphInstanceName(repo string) string {
	if filepath.IsAbs(repo) {
		return filepath.Base(repo)
	}
	return repo
}
