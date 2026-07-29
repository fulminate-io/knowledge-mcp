// SPDX-License-Identifier: Apache-2.0

// subcommand_exit_test.go — unit coverage for subcommandExit, the error->exit-code
// mapping RunSubcommand uses so a shelled-out child's non-zero status (ssh
// forwarding a remote command's exit code in `knowledge tunnel <env> --command …`)
// surfaces as that EXACT process exit code, SSM-style.

package bootstrap

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubcommandExit_PropagatesChildStatus asserts an *exec.ExitError's remote code
// is surfaced verbatim with NO annotation, while a plain error is exit 1 annotated,
// and nil is a clean 0.
func TestSubcommandExit_PropagatesChildStatus(t *testing.T) {
	// A real *exec.ExitError carrying a known non-zero status, exactly as ssh returns
	// when it propagates a remote command's exit code.
	exitErr := exec.Command("sh", "-c", "exit 7").Run()
	var ee *exec.ExitError
	require.ErrorAs(t, exitErr, &ee, "setup: expected an *exec.ExitError")

	t.Run("child exit code propagates, no message", func(t *testing.T) {
		code, printMessage := subcommandExit(exitErr)
		assert.Equal(t, 7, code, "the remote exit code must surface verbatim")
		assert.False(t, printMessage, "a propagated child status must not print a redundant annotation")
	})

	t.Run("wrapped exit error still propagates", func(t *testing.T) {
		code, printMessage := subcommandExit(fmt.Errorf("tunnel: %w", exitErr))
		assert.Equal(t, 7, code, "errors.As must unwrap the exit code through a wrap")
		assert.False(t, printMessage)
	})

	t.Run("generic error is annotated exit 1", func(t *testing.T) {
		code, printMessage := subcommandExit(errors.New("boom"))
		assert.Equal(t, 1, code)
		assert.True(t, printMessage, "a generic failure must print the `<sub>: <err>` annotation")
	})

	t.Run("nil is a clean 0", func(t *testing.T) {
		code, printMessage := subcommandExit(nil)
		assert.Equal(t, 0, code)
		assert.False(t, printMessage)
	})
}
