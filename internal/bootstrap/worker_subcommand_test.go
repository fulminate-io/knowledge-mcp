// SPDX-License-Identifier: Apache-2.0

// worker_subcommand_test.go — unit tests for the runWorkerSubcommand
// dispatch shape and the pure flag-parsing helpers it sits on top of.
//
// The runtime side (OnManualTrigger, Status, event subscription) is
// covered by Phase H tests on InterceptWorker and the Phase J smoke
// test — exercising it here would require a real GraphClient + server
// process, which the plan rules out. These tests pin the surface that
// does NOT need a runtime: which subcommand gets dispatched, error
// messages on bad args, and the pure-helper option-parsing edges.

package bootstrap

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// withArgs swaps os.Args for the test body and restores on cleanup.
// RunWorkerSubcommand reads os.Args directly (mirroring RunSubcommand)
// so we set the slice and assert the boolean return without spawning
// a subprocess.
func withArgs(t *testing.T, args []string) {
	t.Helper()
	prev := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = prev })
}

// TestRunWorkerSubcommand_NotWorkerFallsThrough pins that
// RunWorkerSubcommand returns (false, 0) when os.Args[1] is not
// "worker" so main() falls through to the remaining dispatch (version
// flags, ParseFlags, bootstrap.Run). Mirrors RunSubcommand's
// "default: return false, 0" semantics.
func TestRunWorkerSubcommand_NotWorkerFallsThrough(t *testing.T) {
	cases := [][]string{
		{"knowledge"},                // no subcommand → fall through
		{"knowledge", "login"},       // auth subcommand → fall through
		{"knowledge", "logout"},      // auth subcommand → fall through
		{"knowledge", "--root", "."}, // flags only → fall through
		{"knowledge", "collect"},     // unrelated subcommand → fall through
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			withArgs(t, args)
			handled, _ := RunWorkerSubcommand()
			assert.False(t, handled, "args %v must not be handled by RunWorkerSubcommand", args)
		})
	}
}

// TestParseTriggerArgs_HappyPath pins that a fully-specified flag set
// round-trips into triggerOpts with the values the operator typed.
func TestParseTriggerArgs_HappyPath(t *testing.T) {
	args := []string{
		"--payload", `{"q":"hi"}`,
		"--no-wait",
		"--timeout", "5s",
		"--port", "20000",
		"--graph-storage", "/tmp/kg",
		"smoke",
	}
	opts, err := parseTriggerArgs(args)
	require.NoError(t, err)
	assert.Equal(t, "smoke", opts.name)
	assert.JSONEq(t, `{"q":"hi"}`, opts.payload)
	assert.True(t, opts.noWait)
	assert.Equal(t, 5*time.Second, opts.timeout)
	assert.Equal(t, 20000, opts.port)
	assert.Equal(t, "/tmp/kg", opts.graphStorage)
}

// TestParseTriggerArgs_Defaults pins that the default flag values
// match the contract documented in the file-level docstring: payload
// empty, no-wait false, 30s timeout, default port + ~/.knowledge/.
func TestParseTriggerArgs_Defaults(t *testing.T) {
	opts, err := parseTriggerArgs([]string{"smoke"})
	require.NoError(t, err)
	assert.Equal(t, "smoke", opts.name)
	assert.Empty(t, opts.payload)
	assert.False(t, opts.noWait)
	assert.Equal(t, 30*time.Second, opts.timeout)
	assert.Equal(t, graphclient.DefaultPort, opts.port)
	assert.Equal(t, "~/.knowledge/", opts.graphStorage)
}

// TestParseTriggerArgs_MissingName pins that a positional name is
// required — the subcommand is meaningless without one.
func TestParseTriggerArgs_MissingName(t *testing.T) {
	_, err := parseTriggerArgs([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	_, err = parseTriggerArgs([]string{"--no-wait"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// TestParseTriggerArgs_EmptyName pins that whitespace-only positional
// arg is rejected with a clear error rather than silently accepted.
func TestParseTriggerArgs_EmptyName(t *testing.T) {
	_, err := parseTriggerArgs([]string{"   "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name must not be empty")
}

// TestParseTriggerArgs_ExtraPositional pins that we reject extra
// positional args rather than silently dropping them — protects the
// operator from typos like `worker trigger smoke extra-arg`.
func TestParseTriggerArgs_ExtraPositional(t *testing.T) {
	_, err := parseTriggerArgs([]string{"smoke", "extra"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected positional args")
}

// TestParseTriggerArgs_InvalidJSONPayload pins that --payload is
// validated as JSON early. Firing a worker with malformed payload
// would only surface the error inside the runtime; rejecting at the
// CLI boundary saves a round-trip.
func TestParseTriggerArgs_InvalidJSONPayload(t *testing.T) {
	_, err := parseTriggerArgs([]string{"--payload", "{nope", "smoke"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

// TestParseTriggerArgs_HelpFlag pins that --help returns flag.ErrHelp
// verbatim so the caller can short-circuit to a clean exit instead of
// printing "error: flag: help requested".
func TestParseTriggerArgs_HelpFlag(t *testing.T) {
	_, err := parseTriggerArgs([]string{"--help"})
	require.Error(t, err)
	assert.ErrorIs(t, err, flag.ErrHelp, "expected flag.ErrHelp, got %v", err)
}

// TestParseStatusArgs_HappyPath pins the round-trip on the status
// subcommand's flag set.
func TestParseStatusArgs_HappyPath(t *testing.T) {
	args := []string{
		"--limit", "50",
		"--port", "20001",
		"--graph-storage", "/tmp/kg",
		"smoke",
	}
	opts, err := parseStatusArgs(args)
	require.NoError(t, err)
	assert.Equal(t, "smoke", opts.name)
	assert.Equal(t, 50, opts.limit)
	assert.Equal(t, 20001, opts.port)
	assert.Equal(t, "/tmp/kg", opts.graphStorage)
}

// TestParseStatusArgs_Defaults pins default flag values: limit=20,
// default port, ~/.knowledge/ for graph storage.
func TestParseStatusArgs_Defaults(t *testing.T) {
	opts, err := parseStatusArgs([]string{"smoke"})
	require.NoError(t, err)
	assert.Equal(t, "smoke", opts.name)
	assert.Equal(t, 20, opts.limit)
	assert.Equal(t, graphclient.DefaultPort, opts.port)
	assert.Equal(t, "~/.knowledge/", opts.graphStorage)
}

// TestParseStatusArgs_MissingName mirrors the trigger version — a
// positional name is mandatory.
func TestParseStatusArgs_MissingName(t *testing.T) {
	_, err := parseStatusArgs([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// TestParseStatusArgs_NonPositiveLimit pins that --limit 0 / -5 is
// rejected. ReadRecent treats limit<=0 as "return nil" silently;
// surfacing the error at the CLI boundary is more operator-friendly.
func TestParseStatusArgs_NonPositiveLimit(t *testing.T) {
	for _, v := range []string{"0", "-5"} {
		_, err := parseStatusArgs([]string{"--limit", v, "smoke"})
		require.Error(t, err, "limit %s must be rejected", v)
		assert.Contains(t, err.Error(), "--limit must be > 0")
	}
}

// TestParseStatusArgs_ExtraPositional mirrors the trigger version.
func TestParseStatusArgs_ExtraPositional(t *testing.T) {
	_, err := parseStatusArgs([]string{"smoke", "extra"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected positional args")
}

// TestParseStatusArgs_HelpFlag mirrors the trigger version.
func TestParseStatusArgs_HelpFlag(t *testing.T) {
	_, err := parseStatusArgs([]string{"--help"})
	require.Error(t, err)
	assert.ErrorIs(t, err, flag.ErrHelp, "expected flag.ErrHelp, got %v", err)
}
