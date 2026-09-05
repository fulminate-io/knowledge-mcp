// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// EVERY LOG RECORD MUST NAME THE PROCESS THAT WROTE IT.
//
// Several processes legitimately append to one knowledge-daemon.log: the daemon
// and the handoff child that restarts it write to the same path by design, and
// containers sharing a bind-mounted store share it too. Measured: one such file
// carried 21 daemon-start lines from interleaved processes with no container,
// pid or instance field, and no line in it could be attributed to a process —
// the investigation that needed it had to go to /proc instead.

var instanceField = regexp.MustCompile(`instance=([A-Z2-7]{8})\b`)

// withRestoredDefaultLogger keeps setupLogging's slog.SetDefault from leaking
// into the rest of the test binary.
func withRestoredDefaultLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// TestSetupLoggingStampsEveryRecordWithPIDAndInstance is the guard.
func TestSetupLoggingStampsEveryRecordWithPIDAndInstance(t *testing.T) {
	withRestoredDefaultLogger(t)

	logPath := filepath.Join(t.TempDir(), daemonLogFileName)
	lvl := new(slog.LevelVar)
	setupLogging(&Config{LogFile: logPath}, lvl)

	slog.Info("knowledge serve: HTTP MCP daemon listening", "addr", "127.0.0.1:15023")
	slog.Warn("second line from the same process")

	b, err := os.ReadFile(logPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	body := string(b)

	// PID AGAINST AN EXTERNAL EXPECTATION: the value is compared with the
	// runtime's own answer, not with whatever the handler happened to print.
	require.Contains(t, body, fmt.Sprintf("pid=%d", os.Getpid()),
		"every record must name the pid that wrote it; got %q", body)

	found := instanceField.FindAllStringSubmatch(body, -1)
	require.Len(t, found, 2, "both records must carry an instance field; got %q", body)
	require.Equal(t, found[0][1], found[1][1],
		"the instance id must be STABLE within a process — a per-record id would make one process look like many")

	// CONTROL: the handler still emits the caller's own attributes, so the two
	// assertions above are reading a live record rather than a fixed prefix.
	require.Contains(t, body, "addr=127.0.0.1:15023")
	require.Contains(t, body, "knowledge serve: HTTP MCP daemon listening")
}

// TestProcessInstanceIDIsStableWithinAProcess pins the other half of the
// attribution contract. Distinctness ACROSS processes rests on crypto/rand.Text
// rather than on a clock or a pid, because two daemons started in the same
// second from the same image in different pid namespaces are exactly the pair
// that has to be told apart, and both of those seeds collide for them.
func TestProcessInstanceIDIsStableWithinAProcess(t *testing.T) {
	first := processInstanceID()
	require.Len(t, first, 8)
	require.Equal(t, first, processInstanceID(),
		"repeated calls in one process must return the same id")
	require.Regexp(t, `^[A-Z2-7]{8}$`, first)
}
