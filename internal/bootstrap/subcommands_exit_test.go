// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// TestSubcommandExit_NoValidSessionHasItsOwnCode pins a cross-module contract:
// the bench harness's preflight branches on `knowledge auth-status`'s exit
// code, and it can only tell "no session" from "this binary predates the
// subcommand" if the two differ. Every unrecognized argv exits 1, so the
// no-session answer must not.
//
// The literal 2 is pinned here deliberately. A refactor of the exit-code
// plumbing that silently renumbered it would otherwise turn every logged-out
// machine into an indeterminate result, or worse, every old binary into a
// confident refusal.
func TestSubcommandExit_NoValidSessionHasItsOwnCode(t *testing.T) {
	err := fmt.Errorf("%w: not logged in", cli.ErrNoValidSession)

	code, printMessage := subcommandExit(err)
	if code != 2 {
		t.Errorf("no-valid-session exit code = %d, want 2 (the literal the bench preflight pins)", code)
	}
	if code != cli.ExitNoValidSession {
		t.Errorf("exit code %d disagrees with cli.ExitNoValidSession (%d)", code, cli.ExitNoValidSession)
	}
	if !printMessage {
		t.Error("the one-line reason must still print — it is what tells a human " +
			"whether they are logged out or merely expired")
	}
}

// TestSubcommandExit_OtherOutcomesUnchanged is the control: without it, a
// subcommandExit that returned 2 for everything would satisfy the test above
// while destroying every other caller's exit contract.
func TestSubcommandExit_OtherOutcomesUnchanged(t *testing.T) {
	if code, printMessage := subcommandExit(nil); code != 0 || printMessage {
		t.Errorf("nil error = (%d, %v), want (0, false)", code, printMessage)
	}

	// A generic failure — including the unrecognized-argv case a probing
	// caller must read as indeterminate — stays 1.
	if code, printMessage := subcommandExit(errors.New("something broke")); code != 1 || !printMessage {
		t.Errorf("generic error = (%d, %v), want (1, true)", code, printMessage)
	}

	// A shelled-out child's status still propagates verbatim with no
	// annotation.
	childErr := exec.Command("sh", "-c", "exit 7").Run()
	var exitErr *exec.ExitError
	if !errors.As(childErr, &exitErr) {
		t.Fatalf("expected an *exec.ExitError, got %T", childErr)
	}
	if code, printMessage := subcommandExit(childErr); code != 7 || printMessage {
		t.Errorf("child exit error = (%d, %v), want (7, false)", code, printMessage)
	}
}
