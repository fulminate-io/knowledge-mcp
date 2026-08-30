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
// recognized CLI subcommands (login/logout/auth-status/accounts/account/
// start/stop/status/serve/
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
		err = withSelectionRestart(func() error { return cli.LoginCmd(rest) })
	case "logout":
		err = cli.LogoutCmd(rest)
	case "auth-status":
		err = cli.AuthStatusCmd(rest)
	case "accounts":
		err = cli.AccountsCmd(rest)
	case "account":
		err = withSelectionRestart(func() error { return runAccountVerb(rest) })
	case "check":
		err = runCheckVerb(rest)
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

// withSelectionRestart runs a command that can MOVE the stored account
// selection, and restarts a running daemon when it did, so the user performs
// no manual step.
//
// It wraps both `account use` and `login`: login ESTABLISHES the selection
// where there was none, which moves the daemon's account identity off "" — the
// segment manager built under the unpartitioned cache root would otherwise
// refuse to serve until a restart, recreating exactly the hoop this removes.
//
// A failed command never triggers a restart.
func withSelectionRestart(run func() error) error {
	before := storedSelection()
	if err := run(); err != nil {
		return err
	}
	restartDaemonIfSelectionChanged(before)
	return nil
}

// runAccountVerb dispatches the `account` noun's verbs. Two commands, one
// noun: `accounts` (plural, read) lists, `account use` (singular, write)
// selects.
func runAccountVerb(args []string) error {
	if len(args) == 0 {
		return errors.New("account: expected a verb — the supported one is `knowledge account use <id|slug>`")
	}
	switch args[0] {
	case "use":
		return cli.AccountUseCmd(args[1:])
	default:
		return fmt.Errorf("account: unknown verb %q — the supported one is `knowledge account use <id|slug>`", args[0])
	}
}

// subcommandExit maps a subcommand's returned error to the process exit code and
// whether to print a "<sub>: <err>" annotation. A subcommand that shells out — e.g.
// `knowledge tunnel <env> --command …` runs ssh, which propagates the REMOTE
// command's exit status as an *exec.ExitError — surfaces that EXACT code with NO
// extra line (the child already wrote its own stdout/stderr, SSM-style).
//
// `auth-status` reporting no usable session maps to cli.ExitNoValidSession so a
// caller can tell that answer apart from exit 1, which every unrecognized argv
// also returns — without a distinct code, a script probing an older binary
// would read "this subcommand does not exist" as "you are not logged in". The
// annotation still prints, because the one-line reason is what tells a human
// which of not-logged-in or expired they hit.
//
// `check run` maps its two verdict sentinels to their own codes for the same
// reason and one more: 3 means the corpus checks FLAGGED something and 4 means
// the run could not answer at all. Folding either into 1 would make a caught
// defect indistinguishable from a command that failed to execute, which is
// precisely the confusion a criterion gate must not create.
//
// Every other error is a generic failure: exit 1 with the annotation.
func subcommandExit(err error) (code int, printMessage bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode(), false
	}
	if errors.Is(err, cli.ErrNoValidSession) {
		return cli.ExitNoValidSession, true
	}
	// The two corpus-check verdicts. They are DISTINCT codes rather than one
	// non-zero because a gate that cannot tell a real finding from a probe that
	// could not run would let an author read a refused corpus as a caught defect.
	if errors.Is(err, errCheckFlagged) {
		return ExitCheckFlagged, true
	}
	if errors.Is(err, errCheckInconclusive) {
		return ExitCheckInconclusive, true
	}
	return 1, true
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
