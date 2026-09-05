// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// spawnRecord is one observed daemonExecCommand invocation.
type spawnRecord struct {
	name string
	args []string
	cmd  *exec.Cmd
}

// TestUpdateCheck_RestartHandsOffToNewBinary asserts the PARENT half of the
// handoff: what it spawns, how, and — just as load-bearing — what it does NOT
// do in-process.
func TestUpdateCheck_RestartHandsOffToNewBinary(t *testing.T) {
	s := &updateSeams{}
	installUpdateSeams(t, s, "v2.0.0", nil, false)
	withVersionAndConfig(t, "v1.0.0", nil)

	installedDir := t.TempDir()
	installedExe := filepath.Join(installedDir, "knowledge")
	if err := os.WriteFile(installedExe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("seed installed binary: %v", err)
	}

	var mu sync.Mutex
	var spawns []spawnRecord
	var stopSignals, ownerLookups int

	prevExec, prevPath := daemonExecCommand, installedClientPath
	prevSignal, prevOwner := signalDaemonStop, daemonOwner
	daemonExecCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command("true") //nolint:gosec // fixed argv; the real argv is recorded, not run
		mu.Lock()
		spawns = append(spawns, spawnRecord{name: name, args: args, cmd: c})
		mu.Unlock()
		return c
	}
	installedClientPath = func() (string, error) { return installedExe, nil }
	signalDaemonStop = func(int) error {
		mu.Lock()
		stopSignals++
		mu.Unlock()
		return nil
	}
	daemonOwner = func() (daemonOwnerKind, int) {
		mu.Lock()
		ownerLookups++
		mu.Unlock()
		return daemonOwnerNone, 0
	}
	t.Cleanup(func() {
		daemonExecCommand, installedClientPath = prevExec, prevPath
		signalDaemonStop, daemonOwner = prevSignal, prevOwner
	})

	out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
	if out.Err != nil {
		t.Fatalf("the update tick failed: %v", out.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(spawns) != 1 {
		t.Fatalf("expected exactly one spawned child, got %d", len(spawns))
	}
	got := spawns[0]

	// The full argv, whatever launcher wraps it.
	full := append([]string{got.name}, got.args...)
	joined := strings.Join(full, " ")

	// argv[0] of the CHILD PROGRAM is the path the install just wrote — not
	// this process's own executable. On linux a transient-scope launcher may
	// sit in front of it, so the assertion is on presence in the argv rather
	// than on position zero.
	if !strings.Contains(joined, installedExe) {
		t.Errorf("the child does not run the newly-installed binary %q; argv was %q — the point of the handoff is that the NEW binary performs the restart", installedExe, joined)
	}
	if strings.Contains(joined, os.Args[0]) && os.Args[0] != installedExe {
		t.Errorf("the child runs this process's own executable rather than the installed one; argv was %q", joined)
	}
	if !strings.Contains(joined, restartDaemonVerb) {
		t.Errorf("the child argv omits the %q verb: %q", restartDaemonVerb, joined)
	}
	if !strings.Contains(joined, "--"+restartTargetVersionFlag) || !strings.Contains(joined, "v2.0.0") {
		t.Errorf("the child argv omits --%s with the resolved tag: %q", restartTargetVersionFlag, joined)
	}

	// HALF ONE of the survival fix. On unix a child forked WITHOUT this shares
	// the parent's process group and is killed by the very stop it is about to
	// issue, leaving new binaries on disk, no daemon running, and the identity
	// assertion never reached.
	//
	// THE PREDICATE IS PLATFORM-SPLIT (handoff_procattr_{unix,windows}_test.go)
	// because the FIELD that carries the property is: Setpgid on unix,
	// CreationFlags on windows, and syscall.SysProcAttr has neither field on the
	// other platform's build. The assertion is unchanged on unix — the same
	// nil-or-not-set condition, read through a function instead of inline.
	if !handoffAttrIsolatesChild(got.cmd.SysProcAttr) {
		t.Errorf("the handoff child was not given its own process group; it would be killed by the unit stop it performs")
	}

	// NO IN-PROCESS STOP. This is what rejects the plausible-but-wrong
	// implementation that simply calls the restart sequence: such an
	// implementation spawns nothing and signals its OWN pid, and the argv
	// assertions above could not tell it from a missing spawn.
	if stopSignals != 0 {
		t.Errorf("the update path signaled a daemon in-process %d time(s); it would be signaling ITSELF and would exit before the restart completed", stopSignals)
	}
	if ownerLookups != 0 {
		t.Errorf("the update path classified the daemon owner in-process %d time(s); that is the first step of the self-restart it must not perform", ownerLookups)
	}
}

// TestUpdateCheck_HandoffUsesTheCgroupEscapingLauncherOnLinux pins HALF TWO,
// in every arm, on whichever platform runs it.
//
// Setpgid creates a process group and does NOT escape a cgroup, so on
// linux/systemd the child would still be killed by the unit stop without a
// transient scope. MEASURED under a real user systemd manager: with the unit's
// own main process spawning the child, a plain child was KILLED by the unit stop
// while a scope-launched child SURVIVED it.
//
// USABLE, NOT MERELY PRESENT, and that correction came from CI. The rule used to
// key on the launcher BINARY EXISTING, and this test skipped itself wherever it
// did not. A Linux box can have systemd-run installed with no user session
// behind it — a container, an ssh login without lingering — and `systemd-run
// --user` then exits without ever exec'ing the child, so the upgrade's child
// never starts and nothing observes it because the parent releases rather than
// waits. Reproduced in a golang:1.26 container with systemd installed: three
// handoff tests failed with "the child never started", and passed in the same
// container without the package.
//
// THE ARMS ARE DRIVEN, NOT OBSERVED. Both the platform and the probe are
// parameters, so all four combinations assert here rather than three of them
// being whatever the host happens to be.
func TestUpdateCheck_HandoffUsesTheCgroupEscapingLauncherOnLinux(t *testing.T) {
	const exe = "/opt/knowledge/knowledge"
	always := func() bool { return true }
	never := func() bool { return false }

	t.Run("linux with a usable launcher goes through it", func(t *testing.T) {
		name, argv := handoffArgvFor("linux", exe, "v2.0.0", always)
		joined := strings.Join(append([]string{name}, argv...), " ")
		if name != scopeLauncher[0] {
			t.Errorf("argv[0] = %q, want the launcher %q", name, scopeLauncher[0])
		}
		for _, want := range scopeLauncher[1:] {
			if !strings.Contains(joined, want) {
				t.Errorf("the launcher argv omits %q: %q", want, joined)
			}
		}
		if !strings.Contains(joined, exe) {
			t.Errorf("the launcher argv lost the binary it is supposed to place: %q", joined)
		}
	})

	t.Run("linux with an UNUSABLE launcher spawns the binary directly", func(t *testing.T) {
		name, argv := handoffArgvFor("linux", exe, "v2.0.0", never)
		if name != exe {
			t.Errorf("argv[0] = %q, want the binary itself: a launcher that cannot place the child must not be prefixed, or the upgrade never starts", name)
		}
		if strings.Contains(strings.Join(argv, " "), scopeLauncher[0]) {
			t.Errorf("the launcher leaked into the argv: %q", argv)
		}
	})

	t.Run("a non-linux host never uses the launcher", func(t *testing.T) {
		// Even with a probe that says yes: there is no cgroup to escape, and this
		// is the arm a darwin-only run would otherwise be the only one to see.
		if name, _ := handoffArgvFor("darwin", exe, "v2.0.0", always); name != exe {
			t.Errorf("on darwin the child must be the binary itself, got %q", name)
		}
	})

	t.Run("every arm carries the verb and the target", func(t *testing.T) {
		for _, tc := range []struct {
			goos   string
			usable func() bool
		}{{"linux", always}, {"linux", never}, {"darwin", always}} {
			name, argv := handoffArgvFor(tc.goos, exe, "v2.0.0", tc.usable)
			joined := strings.Join(append([]string{name}, argv...), " ")
			if !strings.Contains(joined, restartDaemonVerb) || !strings.Contains(joined, "v2.0.0") {
				t.Errorf("%s/usable=%v lost the verb or the target: %q", tc.goos, tc.usable(), joined)
			}
		}
	})
}

// TestScopeLauncherUsableAnswersFromTheLauncherItself pins the probe's own
// contract: it reports false when the launcher is not installed, without
// consulting anything else.
func TestScopeLauncherUsableAnswersFromTheLauncherItself(t *testing.T) {
	prev := scopeLauncher
	scopeLauncher = []string{"knowledge-no-such-launcher-binary", "--user"}
	t.Cleanup(func() { scopeLauncher = prev })

	if scopeLauncherUsable() {
		t.Error("a launcher that is not installed cannot be usable")
	}
}

// TestRestartDaemonVerb_ReachesInstallServiceUnitsAndRestart drives the CHILD
// half through the real subcommand dispatcher.
//
// It exists because every assertion in the parent test is a property of the
// PARENT. Three implementations satisfy that test completely while the mandated
// post-restart identity assertion never happens: the verb dispatching straight
// to the restart sequence and skipping the unit install; the verb dropping the
// target and passing an empty string, which inverts the identity check into
// something no live daemon can satisfy; and the verb never being registered at
// all, which nothing observes because the parent Releases rather than Waits.
func TestRestartDaemonVerb_ReachesInstallServiceUnitsAndRestart(t *testing.T) {
	// THIS ONE DRIVES THE DISPATCH, which resolves the operator's graph storage
	// from $HOME — that is its job in production. So the isolation has to be
	// here: without it this test writes the real ~/.knowledge/knowledge-daemon.log
	// and, through slog.SetDefault, sends the rest of the suite there too.
	knowledgeDir(t)
	withRestoredDefaultLogger(t)

	var mu sync.Mutex
	var reached []string
	prev := installServiceUnitsAndRestartFn
	installServiceUnitsAndRestartFn = func(target string) error {
		mu.Lock()
		reached = append(reached, target)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { installServiceUnitsAndRestartFn = prev })

	// Dispatched through the REAL RunSubcommand with the argv the parent
	// constructs, so an unregistered verb fails here rather than silently.
	prevArgs := os.Args
	os.Args = []string{"knowledge", restartDaemonVerb, "--" + restartTargetVersionFlag, "v2.0.0"}
	handled, code := RunSubcommand()
	os.Args = prevArgs

	if !handled {
		t.Fatalf("RunSubcommand did not handle %q; the verb is not registered, and a child running an unregistered verb exits non-zero with nothing observing it", restartDaemonVerb)
	}
	if code != 0 {
		t.Errorf("the verb exited %d, want 0", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reached) != 1 || reached[0] != "v2.0.0" {
		t.Errorf("installServiceUnitsAndRestart reached with %v, want exactly [v2.0.0] — the restart must install the units and assert the identity, not bypass them", reached)
	}
}

// TestRestartDaemonVerb_RefusesAnEmptyTarget is the row that rejects the
// drops-the-flag implementation: an empty target makes the identity assertion a
// comparison against "" that no live daemon can satisfy, so it must be refused
// up front rather than passed through.
func TestRestartDaemonVerb_RefusesAnEmptyTarget(t *testing.T) {
	// THE CONTROL BELOW REACHES THE VERB'S SUCCESS PATH, which installs the
	// process's logging. It is given a directory of its own, and HOME is isolated
	// besides, so neither this test nor the rest of the suite it redirects can
	// touch the operator's store.
	knowledgeDir(t)
	withRestoredDefaultLogger(t)
	storage := t.TempDir()

	var mu sync.Mutex
	reached := 0
	prev := installServiceUnitsAndRestartFn
	installServiceUnitsAndRestartFn = func(string) error {
		mu.Lock()
		reached++
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { installServiceUnitsAndRestartFn = prev })

	if err := runRestartDaemon(nil, storage); err == nil {
		t.Errorf("an absent --%s must be refused", restartTargetVersionFlag)
	}
	if err := runRestartDaemon([]string{"--" + restartTargetVersionFlag, ""}, storage); err == nil {
		t.Errorf("an EMPTY --%s must be refused", restartTargetVersionFlag)
	}
	// Read the counter WITHOUT holding the lock across the control call below:
	// the stub takes the same mutex, so holding it here would deadlock.
	mu.Lock()
	afterRefusals := reached
	mu.Unlock()
	if afterRefusals != 0 {
		t.Errorf("a refused target still reached the restart %d time(s)", afterRefusals)
	}

	// KNOWN-POSITIVE, same run: a non-empty target IS accepted, so the refusals
	// above are a property of the empty value rather than of a verb that always
	// errors.
	if err := runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v3.0.0"}, storage); err != nil {
		t.Fatalf("the control failed: a valid target must be accepted: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reached != 1 {
		t.Errorf("the control failed: a valid target reached the restart %d time(s), want 1", reached)
	}
}

// handOffRestart must refuse an empty target for the same reason the verb does,
// so the parent never spawns a child that cannot succeed.
func TestHandOffRestart_RefusesAnEmptyTarget(t *testing.T) {
	spawned := 0
	prev := daemonExecCommand
	daemonExecCommand = func(string, ...string) *exec.Cmd {
		spawned++
		return exec.Command("true")
	}
	t.Cleanup(func() { daemonExecCommand = prev })

	if err := (&client{}).handOffRestart(""); err == nil {
		t.Errorf("handing off with no target version must be refused")
	}
	if spawned != 0 {
		t.Errorf("a refused handoff still spawned %d child(ren)", spawned)
	}
	_ = context.Background
}
