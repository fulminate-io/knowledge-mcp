// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// NOTE: no t.Parallel() anywhere in this file, by design — every seam these
// tests stub (daemonOwner, signalDaemonStop, daemonExecCommand, runLaunchctl,
// serverOwnerFn, serviceGOOS, the readiness knobs) is a PACKAGE-LEVEL var
// shared with setup_restart_test.go, so parallel tests would race each other's
// stubs. These tests never touch a real daemon or a real service manager.

// selectAccount writes id into the temp-HOME config.
// knowledgeDir(t) must have run first.
func selectAccount(t *testing.T, id string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if err := config.WriteSelectedAccountID(filepath.Join(home, ".knowledge", "config"), id); err != nil {
		t.Fatalf("write selection: %v", err)
	}
}

// stubSignalDaemonStop records every SIGTERM'd pid.
func stubSignalDaemonStop(t *testing.T) *[]int {
	t.Helper()
	prev := signalDaemonStop
	var pids []int
	signalDaemonStop = func(pid int) error {
		pids = append(pids, pid)
		return nil
	}
	t.Cleanup(func() { signalDaemonStop = prev })
	return &pids
}

// noDaemonOnPort makes the port-release wait resolve immediately, and keeps
// every test in this file off lsof and the machine's real 15023 listener.
func noDaemonOnPort(t *testing.T) {
	t.Helper()
	prev := daemonPIDOnPort
	daemonPIDOnPort = func(int) int { return 0 }
	t.Cleanup(func() { daemonPIDOnPort = prev })
}

// TestRestartDaemonIfSelectionChanged_OnlyOnRealChange proves the trigger is
// change-driven and never starts a daemon that was not already running.
func TestRestartDaemonIfSelectionChanged_OnlyOnRealChange(t *testing.T) {
	knowledgeDir(t)
	shortReadiness(t)
	stubProbeDaemon(t, "v-test")
	stubServerOwner(t, "bare")
	noDaemonOnPort(t)
	calls := spyDaemonExec(t)
	pids := stubSignalDaemonStop(t)

	selectAccount(t, "acct_01BEFORE")

	// Unchanged value: no stop, no spawn.
	stubDaemonOwner(t, daemonOwnerBare, 4242)
	restartDaemonIfSelectionChanged("acct_01BEFORE")
	if len(*pids) != 0 || len(*calls) != 0 {
		t.Errorf("an unchanged selection cycled the daemon: %d stops, %d spawns", len(*pids), len(*calls))
	}

	// No daemon running: a config write must never START one.
	stubDaemonOwner(t, daemonOwnerNone, 0)
	restartDaemonIfSelectionChanged("acct_01SOMETHINGELSE")
	if len(*calls) != 0 {
		t.Errorf("the helper spawned a daemon where none was running: %v", *calls)
	}

	// Changed value with a running bare daemon: stop then spawn.
	stubDaemonOwner(t, daemonOwnerBare, 4242)
	out := captureStdout(t, func() { restartDaemonIfSelectionChanged("acct_01SOMETHINGELSE") })
	if len(*pids) != 1 || (*pids)[0] != 4242 {
		t.Errorf("stopped pids = %v, want exactly [4242]", *pids)
	}
	if len(*calls) != 1 {
		t.Fatalf("spawned %d daemons, want 1: %v", len(*calls), *calls)
	}
	if argv := (*calls)[0]; len(argv) < 2 || argv[1] != "serve" {
		t.Errorf("spawn argv = %v, want the daemon `serve` argv", argv)
	}
	if !strings.Contains(out, "restarted") {
		t.Errorf("output %q does not report the restart", out)
	}
}

// TestRestartDaemonIfSelectionChanged_DaemonUnitOnly proves that on a
// unit-managed daemon ONLY the daemon unit is cycled — the 15022 server unit
// is never kickstarted, so unrelated work is not interrupted.
func TestRestartDaemonIfSelectionChanged_DaemonUnitOnly(t *testing.T) {
	knowledgeDir(t)
	shortReadiness(t)
	withServiceGOOS(t, "darwin")
	stubProbeDaemon(t, "v-test")
	stubServerOwner(t, "launchd")
	stubDaemonOwner(t, daemonOwnerUnit, 777)
	noDaemonOnPort(t)
	spawns := spyDaemonExec(t)
	lc := spyLaunchctl(t, "")

	selectAccount(t, "acct_01UNIT")
	restartDaemonIfSelectionChanged("")

	if len(*lc) == 0 {
		t.Fatal("no launchctl calls at all — the assertions below would be vacuous")
	}
	var sawDaemonKickstart bool
	for _, call := range *lc {
		joined := strings.Join(call, " ")
		if strings.HasSuffix(joined, launchdServerLabel) {
			t.Errorf("the 15022 server unit was cycled: %v", call)
		}
		if call[0] == "kickstart" && strings.HasSuffix(joined, launchdDaemonLabel) {
			sawDaemonKickstart = true
		}
	}
	if !sawDaemonKickstart {
		t.Errorf("the daemon unit was never kickstarted: %v", *lc)
	}
	if len(*spawns) != 0 {
		t.Errorf("a unit-managed daemon was fork+exec'd: %v", *spawns)
	}
}

// TestRestartDaemonIfSelectionChanged_BrewDefers proves a brew-managed daemon
// is never stopped or spawned — the helper prints the instruction instead.
func TestRestartDaemonIfSelectionChanged_BrewDefers(t *testing.T) {
	knowledgeDir(t)
	shortReadiness(t)
	withServiceGOOS(t, "darwin")
	stubProbeDaemon(t, "v-test")
	stubServerOwner(t, "brew services")
	stubDaemonOwner(t, daemonOwnerBare, 9001)
	noDaemonOnPort(t)
	spawns := spyDaemonExec(t)
	lc := spyLaunchctl(t, "")
	pids := stubSignalDaemonStop(t)

	selectAccount(t, "acct_01BREW")
	out := captureStdout(t, func() { restartDaemonIfSelectionChanged("") })

	if !strings.Contains(out, "brew services restart knowledge") {
		t.Errorf("output %q does not carry the brew restart instruction", out)
	}
	if len(*pids) != 0 {
		t.Errorf("a brew-managed daemon was signaled: %v", *pids)
	}
	if len(*spawns) != 0 {
		t.Errorf("a brew-managed daemon was spawned: %v", *spawns)
	}
	if len(*lc) != 0 {
		t.Errorf("a brew-managed daemon was cycled via launchctl: %v", *lc)
	}
}

// TestRestartDaemonIfSelectionChanged_FailureIsNonFatal proves a restart that
// never comes back prints an actionable line and returns normally — the
// selection is already persisted and must never be reported as a failure.
func TestRestartDaemonIfSelectionChanged_FailureIsNonFatal(t *testing.T) {
	knowledgeDir(t)
	shortReadiness(t)
	stubServerOwner(t, "bare")
	stubDaemonOwner(t, daemonOwnerBare, 555)
	noDaemonOnPort(t)
	stubSignalDaemonStop(t)
	spyDaemonExec(t)

	// The daemon never answers the readiness probe.
	prev := probeDaemon15023
	probeDaemon15023 = func(int) (string, bool) { return "", false }
	t.Cleanup(func() { probeDaemon15023 = prev })

	selectAccount(t, "acct_01NEVERBACK")
	out := captureStdout(t, func() { restartDaemonIfSelectionChanged("") })

	if !strings.Contains(out, "the account is saved") {
		t.Errorf("output %q does not state the selection was persisted", out)
	}
	if !strings.Contains(out, "restart it yourself") {
		t.Errorf("output %q is not actionable", out)
	}
	// The selection survived the failed restart.
	home, _ := os.UserHomeDir()
	got, err := config.ReadSelectedAccountID(filepath.Join(home, ".knowledge", "config"))
	if err != nil {
		t.Fatalf("ReadSelectedAccountID: %v", err)
	}
	if got != "acct_01NEVERBACK" {
		t.Errorf("selection after a failed restart = %q, want it untouched", got)
	}
}

// TestRunSubcommand_AccountVerbs proves the dispatcher recognizes both nouns
// and errors actionably on an unknown account verb.
func TestRunSubcommand_AccountVerbs(t *testing.T) {
	// Verb dispatch, asserted directly: an unknown or missing verb names the
	// supported one.
	for _, args := range [][]string{nil, {}, {"lsit"}, {"select", "acme"}} {
		err := runAccountVerb(args)
		if err == nil {
			t.Fatalf("runAccountVerb(%q): want an error, got nil", args)
		}
		if !strings.Contains(err.Error(), "knowledge account use <id|slug>") {
			t.Errorf("runAccountVerb(%q) error %q does not name the supported verb", args, err)
		}
	}
	// Known-positive control: `use` IS dispatched — it reaches AccountUseCmd,
	// whose --help returns nil without any network call.
	if err := runAccountVerb([]string{"use", "--help"}); err != nil {
		t.Errorf("runAccountVerb(use --help) = %v, want nil", err)
	}

	// Dispatcher recognition, through the real RunSubcommand. --help keeps
	// both commands off the network.
	knowledgeDir(t)
	stubDaemonOwner(t, daemonOwnerNone, 0)
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })

	cases := []struct {
		argv     []string
		wantCode int
	}{
		{[]string{"knowledge", "accounts", "--help"}, 0},
		{[]string{"knowledge", "account", "use", "--help"}, 0},
		{[]string{"knowledge", "account"}, 1},         // missing verb: recognized, exits 1
		{[]string{"knowledge", "account", "lsit"}, 1}, // unknown verb: recognized, exits 1
	}
	for _, tc := range cases {
		os.Args = tc.argv
		var handled bool
		var code int
		captureStdout(t, func() { handled, code = RunSubcommand() })
		if !handled {
			t.Errorf("%v: not recognized by RunSubcommand", tc.argv)
		}
		if code != tc.wantCode {
			t.Errorf("%v: exit code %d, want %d", tc.argv, code, tc.wantCode)
		}
	}

	// Negative control: an unrelated argv is still NOT recognized, so the
	// handled=true assertions above mean something.
	os.Args = []string{"knowledge", "not-a-subcommand"}
	if handled, _ := RunSubcommand(); handled {
		t.Error("an unrecognized subcommand was claimed as handled")
	}
}
