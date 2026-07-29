// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// RunSubcommand inspects os.Args[1] and, when it matches one of the
// recognized CLI subcommands (login/logout/start/stop/status/serve/
// install/setup/install-claude-assets/install-codex-assets/transcript-upload/tunnel/doctor/version),
// dispatches to the appropriate handler.
// Returns (handled=true, exitCode) when it handled the invocation so
// the caller exits immediately. Returns (false, 0) when the first arg
// is not a recognized subcommand so the no-subcommand fall-through
// (bootstrap.Run, which directs the user to `knowledge serve`) runs.
//
// Errors from subcommands are printed to stderr and return exit 1, EXCEPT a
// propagated child status (an *exec.ExitError from a subcommand that shelled out,
// e.g. `knowledge tunnel <env> --command …` forwarding ssh's remote exit code),
// which surfaces as that exact code with no extra annotation. A clean completion
// returns 0. (See subcommandExit.)
//
// (Despite the name overlap with cmd/knowledge-server/bootstrap's
// RunAuthSubcommand, this handles more than auth — start/stop/status
// and the install-claude-assets/doctor diagnostics also live here.)
func RunSubcommand() (handled bool, exitCode int) {
	if len(os.Args) < 2 {
		return false, 0
	}
	sub := os.Args[1]
	rest := os.Args[2:]
	var err error
	switch sub {
	case "login":
		err = cli.LoginCmd(rest)
	case "logout":
		err = cli.LogoutCmd(rest)
	case "start":
		err = runStart(rest)
	case "stop":
		err = runStop(rest)
	case "status":
		err = runStatus(rest)
	case "serve":
		err = runServe(rest)
	case "install":
		_, err = runInstall(rest)
	case "setup":
		err = runSetup(rest)
	case "install-claude-assets":
		err = runInstallClaudeAssets(rest)
	case "install-codex-assets":
		err = runInstallCodexAssets(rest)
	case "transcript-upload":
		err = cli.TranscriptUploadCmd(rest)
	case "tunnel":
		err = cli.TunnelCmd(rest)
	case "doctor":
		err = runDoctor(rest)
	case "version":
		err = runVersion(rest)
	default:
		return false, 0
	}
	if err != nil {
		code, printMessage := subcommandExit(err)
		if printMessage {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sub, err)
		}
		return true, code
	}
	return true, 0
}

// subcommandExit maps a subcommand's returned error to the process exit code and
// whether to print a "<sub>: <err>" annotation. A subcommand that shells out — e.g.
// `knowledge tunnel <env> --command …` runs ssh, which propagates the REMOTE
// command's exit status as an *exec.ExitError — surfaces that EXACT code with NO
// extra line (the child already wrote its own stdout/stderr, SSM-style). Every other
// error is a generic failure: exit 1 with the annotation.
func subcommandExit(err error) (code int, printMessage bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode(), false
	}
	return 1, true
}

// hasNoWorkerRuntimeFlag reports whether --no-worker-runtime appears
// anywhere in args. Used by RunWorkerSubcommand for an early-bail path:
// when the flag is present anywhere in os.Args, the subcommand returns
// (false, 0) so main() falls through to Run, where ParseFlags picks the
// flag up and skips wireWorkerRuntime. This lets a process whose argv
// happens to carry the worker subcommand keywords still be run purely to
// serve the graph (e.g. the bench harness) without starting a Runner.
func hasNoWorkerRuntimeFlag(args []string) bool {
	for _, a := range args {
		if a == "--no-worker-runtime" || a == "-no-worker-runtime" {
			return true
		}
	}
	return false
}

// expandTilde expands a leading '~/' to the user's home directory.
func expandTilde(path string) string {
	if len(path) > 1 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("failed to resolve home directory", "error", err)
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
