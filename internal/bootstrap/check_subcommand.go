// SPDX-License-Identifier: Apache-2.0

// check_subcommand.go — `knowledge check run`, the SHELL FACE of the corpus-check
// classification.
//
// WHY IT EXISTS SEPARATELY FROM THE MCP TOOL. A plan criterion is a shell command
// and reads an EXIT STATUS; an MCP tool result is text and has none. So the tool's
// verdict line and this subcommand are two faces of ONE classification, never two
// classifications.
//
// HOW THE ONE CLASSIFICATION IS KEPT ONE. This face does not run the scan and
// does not classify: it asks the DAEMON to run manage_checks — the same tool an
// MCP caller invokes, over the daemon's own login-aware graph caller — and maps
// the machine-readable verdict TOKEN of that run onto an exit status. The token
// is read through tools.ParseRunVerdict, which lives beside the renderer that
// writes it, so the line's shape has one owner and this file re-derives nothing:
// it does not read the title constants, it does not count severities, and it
// does not interpret the counters or the body, which are display.
//
// WHY IT ROUTES RATHER THAN CONSTRUCTING A GRAPH CLIENT ITSELF. A client built
// here would be aimed at the local file-backed knowledge-server with no Router,
// so it could never read a cloud-backed checks corpus — and checks authored
// through any MCP tool call live in exactly that plane. The daemon holds the
// router, so asking it is what makes this face's answer the same corpus the
// author wrote to. The cost is that the verb now NEEDS the daemon: an
// unreachable one is refused by name, never answered from a local store.

package bootstrap

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
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
	// httpPort is the loopback port the `knowledge serve` daemon binds its MCP
	// endpoint on — NOT the graph server's port. It is spelled the way
	// `knowledge version` spells the same thing, and it is deliberately a
	// DIFFERENT flag name from the graph-server --port this verb used to take:
	// an invocation still naming the old flag is refused at parse time rather
	// than speaking MCP at a Connect server and failing obscurely.
	httpPort int
	ids      []string
	// includeTests is nil when the flag was not supplied. THE THREE STATES ARE
	// NOT TWO: an omitted flag is legal for every language, while an explicit
	// true OR false is refused for a language ast carries no test-file
	// convention for — there the control would decide nothing, and a caller who
	// wrote it would believe otherwise. A plain bool cannot tell "not supplied"
	// from "supplied as false", which is why this is a pointer and why it is set
	// from fs.Visit rather than from comparing the value against its default.
	includeTests *bool
	// files is the FILE-LIST scope, one path per --files occurrence, nil when
	// the caller named none. A file-list scope reachable from the MCP tool and
	// not from this face would be a silent drop between two faces of one
	// analyzer, which is the defect the parameter-accounting test exists to
	// catch.
	files []string
	// compact selects the one-line-per-hit render. Nil when the flag was not
	// supplied, which means "the default for this scope" — compact for a file
	// list, full otherwise — and a plain bool could not tell that from an
	// explicit false.
	compact *bool
}

// repeatedPathFlag collects one repo-relative path per occurrence.
//
// A flag.Value RATHER THAN A SEPARATED LIST, for the same reason the arguments
// travel as JSON: every separator a list flag could use is legal inside a path,
// so splitting would silently turn one path the caller wrote into two that do
// not exist. Repetition costs the caller a few characters and cannot lose a
// path.
type repeatedPathFlag struct{ values *[]string }

// String renders the collected paths for flag's own usage output.
func (r repeatedPathFlag) String() string {
	if r.values == nil {
		return ""
	}
	return strings.Join(*r.values, " ")
}

// Set appends one occurrence's value verbatim — never trimmed and never split.
func (r repeatedPathFlag) Set(v string) error {
	*r.values = append(*r.values, v)
	return nil
}

// newCheckRunFlagSet registers every flag `knowledge check run` accepts against
// f, returning the set and the pointers that record the explicitly-supplied
// tri-state flags.
//
// IT IS SEPARATE FROM THE PARSE so the registered set is READABLE without
// running a command. The parameter-accounting test walks it to classify every
// CLI input against the tool schema; a flag added here with no classification
// there fails that test, which is the half a schema-only table cannot see.
func newCheckRunFlagSet(f *checkRunFlags) (*flag.FlagSet, *bool, *bool) {
	fs := flag.NewFlagSet("check run", flag.ContinueOnError)
	fs.StringVar(&f.repo, "repo", "", "code-graph name or absolute checkout path (required)")
	fs.StringVar(&f.language, "language", "", "tree-sitter language slug selecting the checks corpus (required)")
	fs.StringVar(&f.pathPrefix, "path-prefix", "", "repo-relative subtree to narrow the walk to")
	fs.Var(repeatedPathFlag{values: &f.files}, "files",
		"a repo-relative path to scan, repeatable — the file list of a diff; mutually exclusive with --path-prefix, and every named path is scanned, disclosed by name, or refused by name")
	fs.IntVar(&f.httpPort, "http-port", graphclient.DefaultMCPHTTPPort,
		"loopback port the `knowledge serve` daemon binds its MCP endpoint (/mcp) on; the run is performed there, over the daemon's routed view of the checks corpus")
	includeTests := fs.Bool("include-tests", false,
		"walk this language's TEST files too (omitted walks non-test files only; an explicit value is refused for a language with no test-file convention)")
	compact := fs.Bool("compact", false,
		"render one line per flagged site instead of the full finding body (defaults to on for --files and off otherwise)")
	return fs, includeTests, compact
}

// parseCheckRunFlags parses the flag set, taking any positional arguments as
// check ids.
func parseCheckRunFlags(args []string) (checkRunFlags, error) {
	var f checkRunFlags
	fs, includeTests, compact := newCheckRunFlagSet(&f)
	if err := fs.Parse(args); err != nil {
		return checkRunFlags{}, err
	}
	// fs.Visit reports only the flags the caller ACTUALLY SUPPLIED, which is the
	// one way to recover the omitted state: a value-compare against false would
	// read an explicit --include-tests=false as an omission.
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "include-tests":
			f.includeTests = includeTests
		case "compact":
			f.compact = compact
		}
	})
	f.ids = fs.Args()
	if strings.TrimSpace(f.repo) == "" {
		return checkRunFlags{}, errors.New("check run: --repo is required — it names both the code graph and the tree the checks walk")
	}
	if strings.TrimSpace(f.language) == "" {
		return checkRunFlags{}, errors.New("check run: --language is required — it selects the checks corpus and there is no default")
	}
	return f, nil
}

// runCheckRun asks the daemon to execute the selected checks and returns the
// sentinel matching the verdict it answered with.
func runCheckRun(args []string) error {
	f, err := parseCheckRunFlags(args)
	if err != nil {
		return err
	}
	// THE REPO IS RESOLVED ON THE CALLER'S SIDE and travels as an absolute path.
	// The daemon resolves a BARE name against its own root or the machine-local
	// manifest, never the shell's working directory, so a bare name sent onward
	// would scan whichever checkout the daemon knows by that name rather than
	// the tree the operator is standing in. Resolving here keeps this verb's
	// answer about the caller's tree, and the daemon returns an absolute path
	// unchanged.
	repoRoot, err := resolveCheckRepoRoot(f.repo)
	if err != nil {
		return err
	}
	rendered, err := runChecksOnDaemon(f.httpPort, checkRunToolArgs(f, repoRoot))
	if err != nil {
		return err
	}
	return reportCheckRun(rendered)
}

// checkRunToolArgs renders the parsed flags as the manage_checks arguments, with
// repoRoot as the already-resolved tree.
//
// AN ABSENT KEY IS THE POINT in every optional case. An absent check subset
// means "every check"; an absent test-file knob means the caller never asked,
// which the analyzer treats differently from an explicit false; an absent
// compact knob means "the default for this scope", which the analyzer's own
// resolver decides. Setting any of them to a value the caller did not write
// would hand them a control they never chose, and spelling the compact default
// here would be a second answer to a question the tool already answers.
func checkRunToolArgs(f checkRunFlags, repoRoot string) map[string]any {
	args := map[string]any{
		"operation": "run",
		"repo":      repoRoot,
		"language":  f.language,
	}
	if len(f.ids) > 0 {
		args["ids"] = f.ids
	}
	if f.pathPrefix != "" {
		args["path_prefix"] = f.pathPrefix
	}
	// A nil test rather than a length test: a present-but-empty list is a
	// mistake the analyzer refuses by name, and dropping the key here instead
	// would silently widen the caller's scan from the files they meant to name
	// to the whole repository — the largest possible reading of a value they got
	// wrong.
	if f.files != nil {
		args["files"] = f.files
	}
	if f.includeTests != nil {
		args["include_tests"] = *f.includeTests
	}
	if f.compact != nil {
		args["compact"] = *f.compact
	}
	return args
}

// reportCheckRun prints the daemon's rendered run to stdout and returns the
// sentinel for its verdict.
func reportCheckRun(rendered string) error {
	return reportCheckRunTo(os.Stdout, rendered)
}

// reportCheckRunTo writes the daemon's rendered run to w and returns the
// sentinel for its verdict.
//
// THE WRITER IS A PARAMETER SO THE OUTPUT IS TESTABLE. A face that prints
// straight to a package-level stdout can be asserted on only through its exit
// code, which is exactly the half that was already covered.
//
// AN UNREADABLE VERDICT IS AN ERROR, NEVER A CLEAN CORPUS. If the answer carries
// no token this client recognizes, the run's outcome is unknown, and the one
// thing it must not do is exit 0 — a gate reporting success on an answer it
// could not read is the vacuous green this whole surface exists to prevent.
func reportCheckRunTo(w io.Writer, rendered string) error {
	body := rendered
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if _, err := io.WriteString(w, body); err != nil {
		return fmt.Errorf("check run: write the verdict: %w", err)
	}
	token, ok := tools.ParseRunVerdict(rendered)
	if !ok {
		return fmt.Errorf(
			"check run: the daemon's answer carried no %s verdict token (expected %s, %s or %s on its first line), so the corpus state is unknown",
			corpusscan.AnalyzerName, tools.VerdictClean, tools.VerdictFlagged, tools.VerdictInconclusive)
	}
	switch token {
	case tools.VerdictClean:
		return nil
	case tools.VerdictFlagged:
		return errCheckFlagged
	case tools.VerdictInconclusive:
		return errCheckInconclusive
	default:
		// UNREACHABLE BY CONSTRUCTION, AND OBSERVED BY NO TEST FOR THAT REASON.
		// ParseRunVerdict returns ok only for the three constants above, so no
		// input to this function can select this arm and no test can drive it
		// without replacing the parser. It is kept rather than deleted because
		// the day a fourth token is minted, a face that fell through to nil would
		// report a pass for a verdict it has no status for — this arm makes that
		// drift loud instead. Deleting it would not fail any test here; that is
		// the honest state of it, not an omission.
		return fmt.Errorf("check run: %q is a verdict token this face has no exit status for", token)
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
