// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- restart-layer test seams ---------------------------------------------

// spyDaemonExec records each daemonExecCommand invocation (name + args)
// and returns a harmless real command so Start/Release succeed without
// forking the actual daemon.
func spyDaemonExec(t *testing.T) *[][]string {
	t.Helper()
	prev := daemonExecCommand
	var calls [][]string
	daemonExecCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("/bin/echo")
	}
	t.Cleanup(func() { daemonExecCommand = prev })
	return &calls
}

func stubStartServerBare(t *testing.T) *int {
	t.Helper()
	prev := startServerBare
	n := 0
	startServerBare = func() error { n++; return nil }
	t.Cleanup(func() { startServerBare = prev })
	return &n
}

func stubHealth15022(t *testing.T, ok bool) {
	t.Helper()
	prev := health15022
	health15022 = func() bool { return ok }
	t.Cleanup(func() { health15022 = prev })
}

func stubProbeDaemon(t *testing.T, version string) {
	t.Helper()
	prev := probeDaemon15023
	probeDaemon15023 = func(int) (string, bool) { return version, true }
	t.Cleanup(func() { probeDaemon15023 = prev })
}

func stubDaemonOwner(t *testing.T, kind daemonOwnerKind, pid int) {
	t.Helper()
	prev := daemonOwner
	daemonOwner = func() (daemonOwnerKind, int) { return kind, pid }
	t.Cleanup(func() { daemonOwner = prev })
}

// shortReadiness collapses the restart readiness poll to near-instant so
// never-ready failure paths don't block on the production timeout.
func shortReadiness(t *testing.T) {
	t.Helper()
	pt, pi := restartReadinessTimeout, restartReadinessInterval
	restartReadinessTimeout = 150 * time.Millisecond
	restartReadinessInterval = 10 * time.Millisecond
	t.Cleanup(func() { restartReadinessTimeout, restartReadinessInterval = pt, pi })
}

// knowledgeDir points HOME at a temp dir and creates ~/.knowledge so
// spawnDaemonProcess's log open succeeds.
func knowledgeDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".knowledge"), 0o750); err != nil {
		t.Fatalf("mkdir ~/.knowledge: %v", err)
	}
}

// --- tests ----------------------------------------------------------------

// TestStopPreExistingListeners locks the migration-safe pre-stop: a live
// 15022 server is gracefully stopped and a bare daemon holding 15023 is
// SIGTERMed, BEFORE units load — the fix a real Mac migration surfaced
// (a bare dev daemon otherwise survives and false-fails the identity
// check). Nothing running → no stop fires.
func TestStopPreExistingListeners(t *testing.T) {
	t.Run("server + bare daemon are stopped", func(t *testing.T) {
		shortReadiness(t)
		prevH, prevS, prevPID, prevSig := health15022, stopServerListener, daemonPIDOnPort, signalDaemonStop
		var serverStopped, daemonSigned bool
		pidCalls := 0
		health15022 = func() bool { return true }
		stopServerListener = func() error { serverStopped = true; return nil }
		daemonPIDOnPort = func(int) int {
			pidCalls++
			if pidCalls == 1 {
				return 4321
			}
			return 0
		}
		signalDaemonStop = func(int) error { daemonSigned = true; return nil }
		t.Cleanup(func() {
			health15022, stopServerListener, daemonPIDOnPort, signalDaemonStop = prevH, prevS, prevPID, prevSig
		})
		stopPreExistingListeners()
		if !serverStopped {
			t.Error("live 15022 server must be stopped")
		}
		if !daemonSigned {
			t.Error("bare daemon PID must be SIGTERMed")
		}
	})

	t.Run("nothing running → no stop", func(t *testing.T) {
		prevH, prevS, prevPID, prevSig := health15022, stopServerListener, daemonPIDOnPort, signalDaemonStop
		var serverStopped, daemonSigned bool
		health15022 = func() bool { return false }
		stopServerListener = func() error { serverStopped = true; return nil }
		daemonPIDOnPort = func(int) int { return 0 }
		signalDaemonStop = func(int) error { daemonSigned = true; return nil }
		t.Cleanup(func() {
			health15022, stopServerListener, daemonPIDOnPort, signalDaemonStop = prevH, prevS, prevPID, prevSig
		})
		stopPreExistingListeners()
		if serverStopped || daemonSigned {
			t.Errorf("nothing should be stopped when idle; server=%v daemon=%v", serverStopped, daemonSigned)
		}
	})
}

// TestRestart_BareSpawnsDaemon: the bare-process
// branch forks the daemon via spawnDaemonProcess — the daemonExecCommand
// spy sees os.Executable() + `serve --http-port 15023 …`.
func TestRestart_BareSpawnsDaemon(t *testing.T) {
	knowledgeDir(t)
	stubDaemonOwner(t, daemonOwnerNone, 0)
	_ = stubStartServerBare(t)
	stubHealth15022(t, true)
	stubProbeDaemon(t, "v1.2.3")
	calls := spyDaemonExec(t)
	wantExe, _ := getExecutable()

	out := captureStdout(t, func() {
		if err := restartSequence("v1.2.3", outcomeBare); err != nil {
			t.Fatalf("restartSequence: %v", err)
		}
	})
	if len(*calls) != 1 {
		t.Fatalf("spawnDaemonProcess must fork exactly once; got %d", len(*calls))
	}
	got := (*calls)[0]
	if got[0] != wantExe {
		t.Fatalf("daemon exe = %q; want os.Executable() %q", got[0], wantExe)
	}
	joined := strings.Join(got[1:], " ")
	if !strings.HasPrefix(joined, "serve --http-port 15023") {
		t.Fatalf("daemon argv = %q; want to start with `serve --http-port 15023`", joined)
	}
	if !strings.Contains(out, "reconnect to the daemon's endpoint on their next session") {
		t.Fatalf("expected editor-reconnect note; got %q", out)
	}
}

// TestRestart_StopBeforeSpawn: the owner-routed stop
// fires BEFORE spawnDaemonProcess; a first install with no owner skips
// the stop.
func TestRestart_StopBeforeSpawn(t *testing.T) {
	run := func(t *testing.T, kind daemonOwnerKind, pid int) []string {
		knowledgeDir(t)
		withServiceGOOS(t, "darwin")
		stubDaemonOwner(t, kind, pid)
		_ = stubStartServerBare(t)
		stubHealth15022(t, true)
		stubProbeDaemon(t, "v1.2.3")

		var seq []string
		prevLC, prevSig, prevExec := runLaunchctl, signalDaemonStop, daemonExecCommand
		runLaunchctl = func(args ...string) error {
			if len(args) > 0 && args[0] == "bootout" {
				seq = append(seq, "stop-unit")
			}
			return nil
		}
		signalDaemonStop = func(int) error { seq = append(seq, "stop-bare"); return nil }
		daemonExecCommand = func(name string, args ...string) *exec.Cmd {
			seq = append(seq, "spawn")
			return exec.Command("/bin/echo")
		}
		t.Cleanup(func() { runLaunchctl, signalDaemonStop, daemonExecCommand = prevLC, prevSig, prevExec })

		_ = captureStdout(t, func() {
			if err := restartSequence("v1.2.3", outcomeBare); err != nil {
				t.Fatalf("restartSequence: %v", err)
			}
		})
		return seq
	}

	t.Run("unit-managed old daemon: bootout before spawn", func(t *testing.T) {
		seq := run(t, daemonOwnerUnit, 4321)
		assertOrdered(t, seq, "stop-unit", "spawn")
	})
	t.Run("bare old daemon: SIGTERM before spawn", func(t *testing.T) {
		seq := run(t, daemonOwnerBare, 4321)
		assertOrdered(t, seq, "stop-bare", "spawn")
	})
	t.Run("first install: no stop, just spawn", func(t *testing.T) {
		seq := run(t, daemonOwnerNone, 0)
		for _, s := range seq {
			if s == "stop-unit" || s == "stop-bare" {
				t.Fatalf("no stop should fire on first install; seq=%v", seq)
			}
		}
		assertContains(t, seq, "spawn")
	})
}

// TestRestart_BrewDefers: a brew outcome defers
// entirely — no stop, no spawn, brew-defer note, nil.
func TestRestart_BrewDefers(t *testing.T) {
	_ = stubStartServerBare(t)
	spawn := spyDaemonExec(t)
	var stopped bool
	prevSig := signalDaemonStop
	signalDaemonStop = func(int) error { stopped = true; return nil }
	t.Cleanup(func() { signalDaemonStop = prevSig })

	out := captureStdout(t, func() {
		if err := restartSequence("v1.2.3", outcomeBrew); err != nil {
			t.Fatalf("restartSequence(brew): %v", err)
		}
	})
	if len(*spawn) != 0 || stopped {
		t.Fatalf("brew defer must not stop or spawn; spawn=%v stopped=%v", *spawn, stopped)
	}
	if !strings.Contains(out, "brew services owns the daemon; manage via `brew services restart knowledge`") {
		t.Fatalf("expected brew-defer note; got %q", out)
	}
}

// TestRestart_VersionIdentity: the post-restart
// check compares probeDaemonVersion(15023) against the threaded
// targetVersion — NOT the process's stale compiled-in bootstrap.Version.
func TestRestart_VersionIdentity(t *testing.T) {
	// (a) self-update path: targetVersion (installed tag) DIFFERS from the
	// process's compiled-in Version; the daemon reports the installed tag
	// → nil. A naive probe==bootstrap.Version compare would false-alarm.
	t.Run("installed tag distinct from bootstrap.Version passes", func(t *testing.T) {
		knowledgeDir(t)
		installedTag := "v9.9.9-installed"
		if installedTag == Version {
			t.Fatalf("test precondition: installedTag must differ from bootstrap.Version %q", Version)
		}
		stubDaemonOwner(t, daemonOwnerNone, 0)
		_ = stubStartServerBare(t)
		stubHealth15022(t, true)
		stubProbeDaemon(t, installedTag)
		_ = spyDaemonExec(t)
		_ = captureStdout(t, func() {
			if err := restartSequence(installedTag, outcomeBare); err != nil {
				t.Fatalf("matching installed tag must pass: %v", err)
			}
		})
	})

	// (b) stale daemon: probe version ≠ targetVersion → loud error.
	t.Run("stale daemon version errors", func(t *testing.T) {
		knowledgeDir(t)
		stubDaemonOwner(t, daemonOwnerNone, 0)
		_ = stubStartServerBare(t)
		stubHealth15022(t, true)
		stubProbeDaemon(t, "v0.0.1-stale")
		_ = spyDaemonExec(t)
		_ = captureStdout(t, func() {
			err := restartSequence("v9.9.9-target", outcomeBare)
			if err == nil || !strings.Contains(err.Error(), "stale daemon may have survived") {
				t.Fatalf("stale daemon must surface a loud error; got %v", err)
			}
		})
	})
}

// TestRestart_HealthCheck: a failed 15022 health
// check surfaces a loud error; success prints the editor-reconnect note.
func TestRestart_HealthCheck(t *testing.T) {
	t.Run("unhealthy server errors", func(t *testing.T) {
		knowledgeDir(t)
		shortReadiness(t) // never-ready → fail fast, don't poll the full timeout
		stubDaemonOwner(t, daemonOwnerNone, 0)
		_ = stubStartServerBare(t)
		stubHealth15022(t, false) // unhealthy
		stubProbeDaemon(t, "v1.2.3")
		_ = spyDaemonExec(t)
		_ = captureStdout(t, func() {
			err := restartSequence("v1.2.3", outcomeBare)
			if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
				t.Fatalf("unhealthy 15022 must error; got %v", err)
			}
		})
	})
	t.Run("healthy server prints reconnect note", func(t *testing.T) {
		knowledgeDir(t)
		stubDaemonOwner(t, daemonOwnerNone, 0)
		_ = stubStartServerBare(t)
		stubHealth15022(t, true)
		stubProbeDaemon(t, "v1.2.3")
		_ = spyDaemonExec(t)
		out := captureStdout(t, func() {
			if err := restartSequence("v1.2.3", outcomeBare); err != nil {
				t.Fatalf("restartSequence: %v", err)
			}
		})
		if !strings.Contains(out, "reconnect to the daemon's endpoint on their next session") {
			t.Fatalf("expected editor-reconnect note; got %q", out)
		}
	})
}

// TestRunSetup_NoServiceShortCircuits: --no-service
// short-circuits before any unit write or restart.
func TestRunSetup_NoServiceShortCircuits(t *testing.T) {
	setupHome(t)
	clearCredEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "ant")
	emptyPATH(t)
	_ = spySelfUpdate(t, "")
	lc := spyLaunchctl(t, "")
	spawn := spyDaemonExec(t)
	server := stubStartServerBare(t)
	_ = captureStdout(t, func() {
		if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
			t.Fatalf("runSetup: %v", err)
		}
	})
	if len(*lc) != 0 || len(*spawn) != 0 || *server != 0 {
		t.Fatalf("--no-service must skip unit write + restart; launchctl=%v spawn=%v serverStarts=%d", *lc, *spawn, *server)
	}
}

// --- helpers --------------------------------------------------------------

func assertOrdered(t *testing.T, seq []string, first, second string) {
	t.Helper()
	fi, si := -1, -1
	for i, s := range seq {
		if s == first && fi == -1 {
			fi = i
		}
		if s == second {
			si = i
		}
	}
	if fi == -1 || si == -1 || fi >= si {
		t.Fatalf("expected %q before %q in seq=%v", first, second, seq)
	}
}

func assertContains(t *testing.T, seq []string, want string) {
	t.Helper()
	if !slices.Contains(seq, want) {
		t.Fatalf("seq %v missing %q", seq, want)
	}
}
