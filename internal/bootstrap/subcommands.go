// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// RunSubcommand inspects os.Args[1] and, when it matches one of the
// recognized CLI subcommands (login/logout/start/stop/status/
// install-claude-assets/doctor), dispatches to the appropriate handler.
// Returns (handled=true, exitCode) when it handled the invocation so
// the caller exits immediately. Returns (false, 0) when the first arg
// is not a recognized subcommand so the default MCP stdio path runs.
//
// Errors from subcommands are printed to stderr; the returned exitCode
// is non-zero on failure and 0 on clean completion.
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
	case "install":
		err = runInstall(rest)
	case "install-claude-assets":
		err = runInstallClaudeAssets(rest)
	case "doctor":
		err = runDoctor(rest)
	default:
		return false, 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", sub, err)
		return true, 1
	}
	return true, 0
}

// hasNoWorkerRuntimeFlag reports whether --no-worker-runtime appears
// anywhere in args. Used by RunWorkerSubcommand for an early-bail path:
// when the flag is present anywhere in os.Args, the subcommand returns
// (false, 0) so main() falls through to Run, where ParseFlags picks the
// flag up and skips wireWorkerRuntime. This is the recursion guard for
// claudecli child processes that inherit the parent's argv (which may
// include the worker subcommand keywords).
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
