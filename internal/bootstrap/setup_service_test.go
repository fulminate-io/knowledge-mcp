// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- service-layer test seams ---------------------------------------------

func withServiceGOOS(t *testing.T, goos string) {
	t.Helper()
	prev := serviceGOOS
	serviceGOOS = goos
	t.Cleanup(func() { serviceGOOS = prev })
}

func stubServerOwner(t *testing.T, owner string) {
	t.Helper()
	prev := serverOwnerFn
	serverOwnerFn = func() string { return owner }
	t.Cleanup(func() { serverOwnerFn = prev })
}

// spyLaunchctl records every launchctl invocation and optionally fails
// the call whose first arg == failOn (e.g. "bootstrap").
func spyLaunchctl(t *testing.T, failOn string) *[][]string {
	t.Helper()
	prev := runLaunchctl
	var calls [][]string
	runLaunchctl = func(args ...string) error {
		calls = append(calls, args)
		if failOn != "" && len(args) > 0 && args[0] == failOn {
			return fmt.Errorf("stub launchctl %s failed", failOn)
		}
		return nil
	}
	t.Cleanup(func() { runLaunchctl = prev })
	return &calls
}

func spySystemctl(t *testing.T) *[][]string {
	t.Helper()
	prev := runSystemctlUser
	var calls [][]string
	runSystemctlUser = func(args ...string) error {
		calls = append(calls, args)
		return nil
	}
	t.Cleanup(func() { runSystemctlUser = prev })
	return &calls
}

func stubSystemdAvailable(t *testing.T, avail bool) {
	t.Helper()
	prevA := userSystemdAvailable
	prevL := enableLinger
	userSystemdAvailable = func() bool { return avail }
	enableLinger = func() error { return nil }
	t.Cleanup(func() { userSystemdAvailable = prevA; enableLinger = prevL })
}

// stubServiceBins stubs getExecutable at a temp binary with a sibling
// knowledge-server so resolveServiceBins resolves both paths, and points
// HOME at a temp dir for unit writes. Returns HOME.
func stubServiceBins(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	exe := filepath.Join(bin, "knowledge")
	server := filepath.Join(bin, "knowledge-server")
	for _, p := range []string{exe, server} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // stub
			t.Fatalf("write %s: %v", p, err)
		}
	}
	withStubExecutable(t, exe)
	withPATH(t, "")
	return home
}

// TestSystemdManagerLive locks the `systemctl --user is-system-running`
// classification (a real Docker-container smoke caught "offline" being
// mis-read as available, which hard-failed daemon-reload instead of
// degrading to bare-process).
func TestSystemdManagerLive(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"running\n", true},
		{"degraded\n", true},
		{"starting", true},
		{"offline\n", false},                  // headless box, no user manager
		{"Failed to connect to bus\n", false}, // no session bus
		{"unknown\n", false},
		{"", false}, // systemctl absent
	} {
		if got := systemdManagerLive(tc.out); got != tc.want {
			t.Errorf("systemdManagerLive(%q) = %v; want %v", tc.out, got, tc.want)
		}
	}
}

// --- rendered-string tests ------------------------------------------------

// TestRenderLaunchdPlist: the plist bodies carry
// the labels, RunAtLoad+KeepAlive, and the ProgramArguments (serve
// --http-port 15023 / --port 15022) with absolute binary paths.
func TestRenderLaunchdPlist(t *testing.T) {
	daemon := renderLaunchdPlist(launchdDaemonLabel, "/opt/knowledge/bin/knowledge", []string{"serve", "--http-port", "15023"}, "/home/u/.knowledge")
	server := renderLaunchdPlist(launchdServerLabel, "/opt/knowledge/bin/knowledge-server", []string{"--port", "15022"}, "/home/u/.knowledge")

	for _, want := range []string{"<string>io.fulminate.knowledge</string>", "<key>RunAtLoad</key>", "<key>KeepAlive</key>", "<string>serve</string>", "<string>--http-port</string>", "<string>15023</string>", "<string>/opt/knowledge/bin/knowledge</string>"} {
		if !strings.Contains(daemon, want) {
			t.Errorf("daemon plist missing %q:\n%s", want, daemon)
		}
	}
	for _, want := range []string{"<string>io.fulminate.knowledge-server</string>", "<string>--port</string>", "<string>15022</string>", "<string>/opt/knowledge/bin/knowledge-server</string>"} {
		if !strings.Contains(server, want) {
			t.Errorf("server plist missing %q:\n%s", want, server)
		}
	}
}

// TestRenderSystemdUnit: the unit bodies carry the
// ExecStart (serve --http-port 15023 / --port 15022), Restart=always,
// WantedBy=default.target.
func TestRenderSystemdUnit(t *testing.T) {
	daemon := renderSystemdUnit("knowledge daemon", "/opt/knowledge/bin/knowledge", []string{"serve", "--http-port", "15023"})
	server := renderSystemdUnit("knowledge server", "/opt/knowledge/bin/knowledge-server", []string{"--port", "15022"})

	if !strings.Contains(daemon, "ExecStart=/opt/knowledge/bin/knowledge serve --http-port 15023") {
		t.Errorf("daemon unit ExecStart wrong:\n%s", daemon)
	}
	if !strings.Contains(server, "ExecStart=/opt/knowledge/bin/knowledge-server --port 15022") {
		t.Errorf("server unit ExecStart wrong:\n%s", server)
	}
	for _, body := range []string{daemon, server} {
		if !strings.Contains(body, "Restart=always") || !strings.Contains(body, "WantedBy=default.target") {
			t.Errorf("unit missing Restart=always / WantedBy=default.target:\n%s", body)
		}
	}
}

// --- degrade + windows + brew ---------------------------------------------

// TestInstallServiceUnits_DarwinLoadFailure: a
// launchctl bootstrap failure degrades-with-note and returns outcomeBare
// (no hard fail).
func TestInstallServiceUnits_DarwinLoadFailure(t *testing.T) {
	stubServiceBins(t)
	stubServerOwner(t, "") // not brew
	withServiceGOOS(t, "darwin")
	calls := spyLaunchctl(t, "bootstrap") // bootstrap fails
	var outcome serviceOutcome
	out := captureStdout(t, func() {
		var err error
		outcome, err = installServiceUnits()
		if err != nil {
			t.Fatalf("installServiceUnits must degrade, not error: %v", err)
		}
	})
	if outcome != outcomeBare {
		t.Fatalf("outcome = %d; want outcomeBare on load failure", outcome)
	}
	if !strings.Contains(out, "launchd LaunchAgent load failed") {
		t.Fatalf("expected load-failure remediation note; got %q", out)
	}
	sawBootstrap := false
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "bootstrap" {
			sawBootstrap = true
		}
	}
	if !sawBootstrap {
		t.Fatalf("bootstrap should have been attempted; calls=%v", *calls)
	}
}

// TestInstallServiceUnits_LinuxBusUnavailable: a
// bus-unavailable manager (even after enable-linger retry) skips
// daemon-reload/enable, prints the note, returns outcomeBare.
func TestInstallServiceUnits_LinuxBusUnavailable(t *testing.T) {
	stubServerOwner(t, "")
	withServiceGOOS(t, "linux")
	stubSystemdAvailable(t, false) // never available, even after linger
	sysctl := spySystemctl(t)
	var outcome serviceOutcome
	out := captureStdout(t, func() {
		var err error
		outcome, err = installServiceUnits()
		if err != nil {
			t.Fatalf("installServiceUnits must degrade, not error: %v", err)
		}
	})
	if outcome != outcomeBare {
		t.Fatalf("outcome = %d; want outcomeBare on bus-unavailable", outcome)
	}
	if !strings.Contains(out, "systemd user manager unavailable") {
		t.Fatalf("expected enable-linger remediation note; got %q", out)
	}
	if len(*sysctl) != 0 {
		t.Fatalf("systemctl must NOT be invoked when the bus is unavailable; calls=%v", *sysctl)
	}
}

// TestInstallServiceUnits_Windows: the windows arm
// writes no unit, invokes neither systemctl nor launchctl, prints the
// session-only note, and returns outcomeBare.
func TestInstallServiceUnits_Windows(t *testing.T) {
	stubServerOwner(t, "")
	withServiceGOOS(t, "windows")
	lc := spyLaunchctl(t, "")
	sc := spySystemctl(t)
	var outcome serviceOutcome
	out := captureStdout(t, func() {
		var err error
		outcome, err = installServiceUnits()
		if err != nil {
			t.Fatalf("installServiceUnits(windows): %v", err)
		}
	})
	if outcome != outcomeBare {
		t.Fatalf("windows outcome = %d; want outcomeBare", outcome)
	}
	if len(*lc) != 0 || len(*sc) != 0 {
		t.Fatalf("neither launchctl nor systemctl may run on windows; lc=%v sc=%v", *lc, *sc)
	}
	if !strings.Contains(out, "no boot-persistence service on Windows") {
		t.Fatalf("expected the session-only windows note; got %q", out)
	}
}

// TestInstallServiceUnits_BrewSkip: a brew-owned
// server skips unit writes and prints the brew-skip note.
func TestInstallServiceUnits_BrewSkip(t *testing.T) {
	stubServerOwner(t, "brew services (launchd: homebrew.mxcl.knowledge)")
	withServiceGOOS(t, "darwin")
	lc := spyLaunchctl(t, "")
	var outcome serviceOutcome
	out := captureStdout(t, func() {
		var err error
		outcome, err = installServiceUnits()
		if err != nil {
			t.Fatalf("installServiceUnits(brew): %v", err)
		}
	})
	if outcome != outcomeBrew {
		t.Fatalf("brew outcome = %d; want outcomeBrew", outcome)
	}
	if len(*lc) != 0 {
		t.Fatalf("no launchctl calls under brew ownership; got %v", *lc)
	}
	if !strings.Contains(out, "brew services manages the daemon; skipping unit install") {
		t.Fatalf("expected brew-skip note; got %q", out)
	}
}
