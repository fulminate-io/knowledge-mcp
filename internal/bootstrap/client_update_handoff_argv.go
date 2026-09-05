// SPDX-License-Identifier: Apache-2.0

// client_update_handoff_argv.go — the argv the restart handoff spawns, and the
// transient-scope launcher it is wrapped in on linux. Split out of
// client_update_check.go, which is at the file-length bound.

package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// scopeLauncher is the transient-scope launcher used on linux, per HALF TWO.
//
// --quiet IS LOAD-BEARING AND IT IS NOT COSMETIC. Without it systemd-run writes
// "Running as unit: ..." to its own STDERR before it execs the wrapped child —
// and the stderr it is given here is the spawning daemon's, which in the field
// is a pipe held by a container runtime or a supervisor whose reader can already
// be gone. Nothing in systemd-run alters SIGPIPE's default disposition, so that
// status line kills the LAUNCHER on a dead pipe and the child it was about to
// exec never runs at all. The upgrade then leaves no daemon, and because the
// hand-off Releases rather than Waits, nothing observes it.
//
// The flag is the one systemd-run provides for exactly this: it suppresses the
// information message and nothing else, so a launcher that genuinely cannot
// place the child still says why. Suppressing the message is preferred to
// redirecting the launcher's stderr elsewhere, which would take the child's own
// stream with it — the child MUST keep the inherited stderr, which is the
// invariant spawn_detached_stdio.go documents and three tests guard.
var scopeLauncher = []string{"systemd-run", "--user", "--scope", "--collect", "--quiet"}

// restartHandoffArgv builds the child's command line: the installed binary
// running the restart verb, wrapped in the transient-scope launcher on linux.
//
// HALF TWO applies on linux only, and only when the launcher is actually
// USABLE: a bare-process or non-systemd linux install has no cgroup to escape,
// and prefixing a launcher that cannot place the child turns a working handoff
// into a spawn failure.
//
// PRESENT WAS NOT ENOUGH, and the gap was measured rather than reasoned. A
// Linux box can have systemd-run installed with no user session behind it — a
// container, an ssh login without lingering, any daemon outside a user manager
// — and `systemd-run --user` then exits without ever exec'ing the child:
//
//	Failed to connect to user scope bus via local transport:
//	$DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined
//
// The upgrade's child never starts, and because the parent RELEASES rather than
// waits, nothing observes it. Reproduced in a golang:1.26 container with systemd
// installed: three handoff tests failed with "the spawned child never started",
// and the same tests pass in the same container without the package.
//
// So the question asked is whether the launcher WORKS here, by running it on a
// trivial command once, at handoff time. A launcher that cannot place /bin/true
// cannot place the upgrade either.
func restartHandoffArgv(exe, targetVersion string) (name string, argv []string) {
	return handoffArgvFor(runtime.GOOS, exe, targetVersion, scopeLauncherUsable)
}

// handoffArgvFor is that decision with both of its inputs passed in.
//
// THE PLATFORM AND THE PROBE ARE PARAMETERS so both arms are testable on either
// platform. The rule is conditional on the operating system, CI runs two of
// them, and a test that can assert only the arm it happens to be running on
// leaves the other to be discovered in CI — which is how the wrong rule reached
// CI in the first place.
func handoffArgvFor(goos, exe, targetVersion string, usable func() bool) (name string, argv []string) {
	child := []string{exe, restartDaemonVerb, "--" + restartTargetVersionFlag, targetVersion}
	if goos == "linux" && usable() {
		return scopeLauncher[0], append(append([]string{}, scopeLauncher[1:]...), child...)
	}
	return child[0], child[1:]
}

// scopeLauncherUsable reports whether the transient-scope launcher can actually
// place a child here.
//
// THE PROBE RUNS THE LAUNCHER, rather than sniffing the environment variables
// its error message happens to name: the variables are a proxy for a bus
// connection, and the thing that has to be true is the connection. It costs one
// short-lived subprocess on the upgrade path, which is not a hot path, and is
// bounded so a wedged service manager cannot stall the handoff.
//
// A FAILING PROBE IS NOT A DEGRADE. It selects the same argv a machine without
// systemd already gets, which is the shape HALF TWO was written to preserve.
//
//nolint:gochecknoglobals // overridable seam so a test can drive both arms.
var scopeLauncherUsable = func() bool {
	if _, err := exec.LookPath(scopeLauncher[0]); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), scopeLauncherProbeTimeout)
	defer cancel()
	probe := launcherProbeCommand(ctx, scopeLauncher[0], append(append([]string{}, scopeLauncher[1:]...), "/bin/true"))
	return probe.Run() == nil
}

// launcherProbeCommand builds the probe with THE SAME STDIO THE REAL HAND-OFF
// GIVES THE LAUNCHER, and that is the whole reason it exists as a named function
// rather than three lines inside the probe.
//
// THE PROBE USED TO ANSWER ABOUT A DIFFERENT INVOCATION THAN THE ONE THAT RUNS.
// It left both streams nil, which exec connects to the null device, so it could
// only ever report whether the launcher can reach the service manager. The
// launcher's other failure mode — dying on a WRITE to an inherited stream whose
// reader is gone — was invisible to it, and the probe therefore returned usable
// for an invocation that could not survive. Handing it this process's own stderr
// makes the probe's subject the invocation the hand-off actually performs: if
// the launcher cannot survive this stream, the probe now fails and the hand-off
// falls back to the plain argv, which is the shape a machine without systemd
// already gets and which the survival tests prove the child lives through.
//
// ONE CONSEQUENCE, STATED RATHER THAN HIDDEN: a launcher that fails for the
// ORIGINAL reason now writes its complaint to the operator's stream instead of
// the null device. That is one line on the upgrade path, on the failure arm
// only, and it is the sentence a reader of that stream needs.
func launcherProbeCommand(ctx context.Context, name string, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, argv...) //nolint:gosec // a fixed argv on the production path; the test seam passes its own binary
	// *os.File on both, for the reason the hand-off's own spawn documents:
	// exec.Cmd passes a raw fd only for an *os.File, and any other writer
	// becomes a pipe drained by a goroutine of this process — which would make
	// the probe survive a stream the real launcher would die on.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd
}

// scopeLauncherProbeTimeout bounds the probe. Short: the answer is a bus
// connection that either exists or does not, and a handoff waiting on a wedged
// service manager is the failure this whole seam exists to avoid.
const scopeLauncherProbeTimeout = 5 * time.Second
