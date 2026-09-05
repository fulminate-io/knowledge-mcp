// SPDX-License-Identifier: Apache-2.0

// client_update_check.go — the daemon's background self-update loop.
//
// A direct analog of client_transcript_upload.go: a config-gated starter, a
// boot-delay one-shot, a ticker loop, and one outcome helper both goroutines
// call. The differences from that template are all consequences of what this
// loop DOES — it can replace the binaries under the running process — so every
// one of them is a guard, a cadence choice, or a refusal.

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// updateCheckInterval is the cadence of the background update check.
//
// DAILY rather than the transcript loop's hourly, and the reasoning is a
// property of the SUBJECT: releases happen at release cadence, not at working
// cadence, so an hourly check re-asks a question whose answer changes a few
// times a month. Each check is also a request to a public release API shared by
// every installed daemon in the world, so twenty-four times fewer requests
// costs at most a day of upgrade latency for something already unattended.
const updateCheckInterval = 24 * time.Hour

// updateCheckBootDelay is the wait before the FIRST check after daemon start.
//
// Five minutes rather than the transcript loop's one, because this check must
// clear more than the listener bind: it should not compete with the daemon's own
// start-up burst — graph load, LLM precheck, pipeline registration — for a
// question whose answer will be equally true five minutes later. Like the
// transcript loop's boot delay it is awaited INSIDE the spawned goroutine, never
// on the wiring path.
const updateCheckBootDelay = 5 * time.Minute

// updateCheckJitter bounds a random wait applied before each check.
//
// The failure mode of a fleet is CORRELATION: every daemon on a machine that
// rebooted after an outage, and every daemon installed from the same release,
// would otherwise reach the endpoint in lockstep. Spreading each check over an
// hour turns a spike into a trickle, and costs nothing — the check has no
// deadline.
const updateCheckJitter = 1 * time.Hour

// updateCheckFailureEscalation is the consecutive-failure count at which the
// loop escalates its log from Warn to Error, mirroring the transcript loop's
// own escalation. A failure that repeats is a real persistent condition and
// must get loud rather than accumulating quietly.
const updateCheckFailureEscalation = 3

// updateCheckWaitFn computes an actual wait: the given base cadence plus a
// random draw inside updateCheckJitter.
//
// BOTH waits — the boot delay and the interval — read through this ONE seam. It
// is a package var, mirroring the transcript loop's own overridable seam, so a
// test can collapse both delays to something it can drive without sleeping out
// a real five minutes, and so the jitter is deterministic under test.
//
//nolint:gochecknoglobals // overridable wait/jitter seam for testability.
var updateCheckWaitFn = func(base time.Duration) time.Duration {
	return base + time.Duration(rand.Int64N(int64(updateCheckJitter)))
}

// updateCheckResolveFn resolves the latest published release. A package var so a
// test substitutes a stub for the real network call.
//
// IT DELIBERATELY DOES NOT USE resolveReleaseTag. That function PINS a
// version-stamped client to its own exact tag and routes only dev stamps to the
// latest endpoint — which is correct for `knowledge install` (a versioned client
// pulling a MATCHING server) and fatal here: a loop built on it would resolve a
// v0.8.2 client to tag v0.8.2 and never discover anything newer. This asks for
// latest unconditionally and compares what comes back.
//
//nolint:gochecknoglobals // overridable resolve seam for testability.
var updateCheckResolveFn = func(ctx context.Context) (*releaseResponse, error) {
	return fetchRelease(ctx, githubAPIBaseURL, "", true)
}

// updateCheckInstallFn performs the install of an already-resolved release. A
// package var so a test can observe the decision without touching disk.
//
//nolint:gochecknoglobals // overridable install seam for testability.
var updateCheckInstallFn = func(ctx context.Context, rel *releaseResponse) error {
	goos, goarch, err := detectPlatform()
	if err != nil {
		return err
	}
	// An EMPTY dest so resolveInstallDest derives the directory from the
	// running binary — which, in the daemon, IS the installed client. This is
	// the Phase 1 hardened stage-both-then-swap path, and it is the whole
	// reason that hardening had to land before this loop existed.
	return installBothBinaries(ctx, rel, goos, goarch, "")
}

// updateSkipReason names why a tick took no action. Every refusal records one,
// so an operator reading the status surface sees a DELIBERATELY IDLE updater
// rather than a silent one.
type updateSkipReason string

const (
	skipDevStamped     updateSkipReason = "dev-stamped build"
	skipBrewManaged    updateSkipReason = "brew-managed install"
	skipContainer      updateSkipReason = "externally-managed install (KNOWLEDGE_MANAGED_INSTALL set)"
	skipNotNewer       updateSkipReason = "already at or ahead of the latest release"
	skipUncomparable   updateSkipReason = "release tag is not comparable to this build's version"
	skipDisabled       updateSkipReason = "automatic updates disabled"
	skipCheckPerformed updateSkipReason = ""
)

// envManagedInstall marks an install whose lifecycle something else owns.
//
// Our own container image sets it in its runtime stage. The client-side read is
// deliberately "any non-empty value" rather than the literal marker, so a distro
// package or an internal deployment can set it and opt out too.
//
// AN ENVIRONMENT VARIABLE IS ACCEPTABLE FOR THIS DECISION BECAUSE IT IS
// FAIL-SAFE IN ONE DIRECTION ONLY: a spoofed marker can only make the updater
// SKIP, never make it update something it otherwise would not. Skipping is the
// conservative outcome and is already reachable through the disable flag.
const envManagedInstall = "KNOWLEDGE_MANAGED_INSTALL"

// isExternallyManaged reports whether something else owns this install's
// lifecycle. See envManagedInstall.
func isExternallyManaged() bool { return os.Getenv(envManagedInstall) != "" }

// brewListFn probes for a brew-managed install cross-platform. A package var so
// tests need no brew.
//
//nolint:gochecknoglobals // overridable probe seam for testability.
var brewListFn = func() bool {
	if _, err := exec.LookPath("brew"); err != nil {
		return false
	}
	return exec.Command("brew", "list", "knowledge").Run() == nil //nolint:gosec // fixed argv, no interpolation
}

// isBrewManagedInstall answers the brew guard on BOTH platforms.
//
// TWO LEGS, because the in-process detector is macOS-only: isBrewOwned resolves
// the running server's owner through launchctl, which answers nothing on Linux.
// The second leg mirrors the shell installer's own cross-platform detection,
// `brew list knowledge` behind a `command -v brew`. Either leg true means brew
// owns this install and the updater never touches it. The exec probe runs once
// per tick, which at a daily cadence is free.
func isBrewManagedInstall() bool {
	if runtime.GOOS == "darwin" && isBrewOwned() {
		return true
	}
	return brewListFn()
}

// updateCheckOutcome is what one tick decided.
type updateCheckOutcome struct {
	// Skipped names the guard that refused, empty when a check ran to a
	// decision.
	Skipped updateSkipReason
	// Installed reports that a newer release was fetched and committed.
	Installed bool
	// Tag is the release the tick resolved, when it got that far.
	Tag string
	// Err is the failure, when the tick failed rather than refused.
	Err error
}

// runUpdateCheckOnce evaluates the guards and, when all of them permit and a
// strictly-newer release exists, installs it and hands off the restart.
//
// GUARD ORDER MATTERS: every guard is evaluated BEFORE any download, so a
// refused install costs no bandwidth and touches no disk.
func (c *client) runUpdateCheckOnce(ctx context.Context, f Config) updateCheckOutcome {
	// GUARD 1 — DISABLED. Also checked in the starter so a disabled daemon
	// spawns no goroutines at all; re-checked here because the config can be
	// reloaded and because a guard that only exists at start-up is one an
	// operator cannot reason about.
	if !autoUpdateEnabled(f) {
		return updateCheckOutcome{Skipped: skipDisabled}
	}
	// GUARD 2 — DEV-STAMPED. A locally-built binary must never be clobbered by
	// a release. This cannot be delegated to the version comparator: that
	// reports uncomparable for a dev stamp, so the guard's intent would ride on
	// a failure for the wrong reason.
	if isDevVersion(Version) {
		return updateCheckOutcome{Skipped: skipDevStamped}
	}
	// GUARD 3 — BREW-MANAGED. Brew owns the lifecycle; never fight it.
	if isBrewManagedInstall() {
		return updateCheckOutcome{Skipped: skipBrewManaged}
	}
	// GUARD 4 — CONTAINER / EXTERNALLY MANAGED. A tag-built image carries a
	// RELEASE stamp so it sails past guard 2, and it has neither launchd nor
	// brew so it sails past guard 3 — while its read-only runtime filesystem
	// makes every write fail, daily, forever. The marker is what stops that.
	if isExternallyManaged() {
		return updateCheckOutcome{Skipped: skipContainer}
	}

	rel, err := updateCheckResolveFn(ctx)
	if err != nil {
		return updateCheckOutcome{Err: fmt.Errorf("resolve latest release: %w", err)}
	}
	cmp, ok := compareReleaseVersions(rel.TagName, Version)
	if !ok {
		return updateCheckOutcome{Skipped: skipUncomparable, Tag: rel.TagName}
	}
	// GUARD 5 — NOT A DOWNGRADE. Strictly newer only, and the install is driven
	// with the already-resolved release rather than through any path that could
	// set a downgrade override.
	if cmp <= 0 {
		return updateCheckOutcome{Skipped: skipNotNewer, Tag: rel.TagName}
	}

	slog.Info("update check: newer release found; installing", "current", Version, "target", rel.TagName)
	if err := updateCheckInstallFn(ctx, rel); err != nil {
		return updateCheckOutcome{Tag: rel.TagName, Err: fmt.Errorf("install %s: %w", rel.TagName, err)}
	}
	// Retire any latched cloud refusal BEFORE the handoff, not after. Between a
	// successful swap and the restart actually taking effect the OLD process
	// keeps running, and a still-latched refusal would have the watcher
	// re-trigger on it in that window. Clearing here closes it.
	//
	// Clearing is not an assertion that this client is now acceptable — it only
	// retires a stale verdict. If it is still refused, the very next cloud
	// request re-latches.
	clientver.ClearRefusal()
	// The binaries on disk are now new while THIS process is still old. The
	// restart is a handoff to the newly-installed binary; this process does not
	// signal itself.
	if err := c.handOffRestart(rel.TagName); err != nil {
		return updateCheckOutcome{Tag: rel.TagName, Installed: true, Err: fmt.Errorf("hand off restart to %s: %w", rel.TagName, err)}
	}
	return updateCheckOutcome{Installed: true, Tag: rel.TagName}
}

// logUpdateCheckOutcome runs one check, records it into the health tracker, and
// logs the result.
//
// GUARD 6 — FAILURES ARE LOUD. A failed tick is logged and the loop continues,
// as the transcript loop continues past a failed batch, but the streak escalates
// from Warn to Error at the threshold and every outcome — refusals included — is
// recorded so the status surfaces can show a deliberately idle or a persistently
// failing updater rather than silence.
func (c *client) logUpdateCheckOutcome(ctx context.Context, f Config) {
	out := c.runUpdateCheckOnce(ctx, f)
	snap := c.updateHealth.Record(out, time.Now())
	switch {
	case out.Err != nil:
		if snap.ConsecutiveFailures >= updateCheckFailureEscalation {
			slog.Error("update check: persistent failure",
				"consecutive_failed_ticks", snap.ConsecutiveFailures, "error", out.Err)
		} else {
			slog.Warn("update check: failed (will retry next tick)",
				"consecutive_failed_ticks", snap.ConsecutiveFailures, "error", out.Err)
		}
	case out.Installed:
		slog.Info("update check: installed a newer release and handed off the restart", "target", out.Tag)
	case out.Skipped != "":
		slog.Debug("update check: no action", "reason", string(out.Skipped))
	default:
		slog.Debug("update check: up to date", "version", Version)
	}
}

// maybeStartUpdateCheck spawns the daemon's background update-check goroutines
// unless the disable gate says otherwise, in which case it spawns NOTHING —
// mirroring how maybeStartTranscriptUpload returns early under its own gate.
//
// Both goroutines run under ctx (c.wireCtx) so the shutdown drain unwinds them.
func (c *client) maybeStartUpdateCheck(ctx context.Context, f Config) {
	if !autoUpdateEnabled(f) {
		slog.Debug("update check skipped (automatic updates disabled)")
		return
	}
	c.updateHealth = newUpdateHealthTracker()
	go c.bootDelayUpdateCheck(ctx, f)
	go c.runUpdateCheckLoop(ctx, f)
	// React to a cloud client-version refusal by looking sooner, instead of
	// waiting out the daily interval. See client_update_refusal_watch.go.
	go c.runUpdateRefusalWatch(ctx, f)
}

// bootDelayUpdateCheck runs ONE check after the boot delay and returns.
//
// A ONE-SHOT, NOT A SECOND TICKER, and the distinction is not stylistic: a
// ticker at the boot delay would resolve the release endpoint every five
// minutes for the life of the process — 288 checks a day — inverting this
// loop's whole cadence argument by two orders of magnitude, and it is a lane
// that fires forever on one cause. Exits promptly on ctx.Done.
func (c *client) bootDelayUpdateCheck(ctx context.Context, f Config) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(updateCheckWaitFn(updateCheckBootDelay)):
		c.logUpdateCheckOutcome(ctx, f)
	}
}

// runUpdateCheckLoop fires a check on the daily cadence, plus jitter, until ctx
// is canceled. A per-tick failure is logged inside the outcome helper and the
// loop continues.
func (c *client) runUpdateCheckLoop(ctx context.Context, f Config) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateCheckWaitFn(updateCheckInterval)):
			c.logUpdateCheckOutcome(ctx, f)
		}
	}
}

// ---------------------------------------------------------------------------
// THE RESTART, PERFORMED BY HANDOFF TO THE NEWLY-INSTALLED BINARY.
//
// WHY A HANDOFF AND NOT A DIRECT CALL. The restart sequence begins by
// classifying the daemon-port owner and SIGTERMing that pid. Called from inside
// this loop, that pid IS this process: it would signal itself, the shutdown
// drain would begin, and the process would exit before reaching the restart or
// the version-identity assertion the ticket mandates. Merely exiting and
// letting a supervisor bring us back is not sufficient either — it skips the
// identity assertion, and a bare-process install has no supervisor at all, so a
// daemon that just exits there leaves the machine with nothing running.
//
// THE FAILURE MODE THAT SINKS THE OBVIOUS IMPLEMENTATION: THE CHILD IS KILLED
// BY THE STOP IT ISSUES. The two supervisors kill by different means, so the
// fix has two halves.
//
// HALF ONE, PROCESS GROUP (darwin and every unix). A child forked the ordinary
// way shares the parent's process group, and `launchctl bootout` — the exact
// call the unit stop makes — kills such a child. Spawning with
// SysProcAttr{Setpgid: true} gives the child its own process GROUP and it
// survives. Setpgid is NOT Setsid: it creates a process group, not a session,
// so the macOS session-leader caveat that governs Setsid is untouched here.
// Said explicitly because the next reader will otherwise see a SysProcAttr and
// conclude that caveat was ignored.
//
// HALF TWO, CGROUP ESCAPE (linux/systemd). Setpgid does not escape a cgroup, so
// half one does nothing for systemd: the generated unit sets no KillMode, so the
// default KillMode=control-group applies and stopping the unit signals every
// process in its cgroup — and an explicit stop also suppresses Restart=always,
// so nothing brings the daemon back. The child is therefore launched through a
// transient scope with its own cgroup. MEASURED under a real user systemd
// manager rather than reasoned: with the unit's own main process spawning the
// child, a plain child was KILLED by the unit stop while a scope-launched child
// SURVIVED it, the control and the candidate differing only in the launcher.
// ---------------------------------------------------------------------------

// installedClientPath resolves the path of the client binary the install just
// wrote — which is where the running client lives, symlinks resolved.
//
//nolint:gochecknoglobals // overridable path seam for testability.
var installedClientPath = func() (string, error) {
	dir, name, err := resolveClientTarget()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// handOffRestart spawns the NEWLY INSTALLED client binary as a detached child
// running the restart verb, and returns without waiting for it.
//
// It spawns the path the install just wrote, NOT this process's own executable:
// the point of the handoff is that the NEW binary performs the restart and the
// identity assertion.
//
// This process does not signal itself, does not call the restart sequence, and
// does not stop any listener. The child performs the stop; this daemon keeps
// serving until the child's signal arrives, at which point the ordinary
// shutdown drain runs.
func (c *client) handOffRestart(targetVersion string) error {
	if targetVersion == "" {
		return errors.New("refusing to hand off a restart with no target version: the restart's identity assertion compares the restarted daemon's version against this value, and an empty target inverts that guarantee into a check nothing can satisfy")
	}
	exe, err := installedClientPath()
	if err != nil {
		return fmt.Errorf("resolve the installed client path: %w", err)
	}

	// THE CHILD'S DURABLE SINK IS CHECKED BEFORE THE FORK, and this side is the
	// only side that can report the failure. Once the child is running, THIS
	// process is about to be stopped by it: a child that finds it cannot open the
	// log has only the inherited stderr to say so on, which is the stream that
	// may already be dead — an upgrade failing with the reason reaching nobody.
	// Checked here, the refusal happens while a live daemon with a working log is
	// still the one holding the error.
	//
	// The check is an open-and-close of the real path with the real flags, not a
	// stat: a directory that exists but is unwritable passes a stat and fails the
	// open.
	graphStorage, err := serviceGraphStorage()
	if err != nil {
		return fmt.Errorf("resolve the graph storage directory for the handoff child's log: %w", err)
	}
	if err := ensureDurableLogWritable(daemonLogPath(graphStorage)); err != nil {
		return fmt.Errorf("refusing to hand off a restart the child could not record: %w", err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open devnull: %w", err)
	}

	name, argv := restartHandoffArgv(exe, targetVersion)
	cmd := daemonExecCommand(name, argv...)
	cmd.Stdin = devNull
	// BOTH STREAMS ARE THIS PROCESS'S OWN STDERR and both must stay *os.File,
	// the same constraint the daemon spawn documents: exec.Cmd passes a raw fd
	// only for an *os.File, and any other writer becomes a pipe drained by a
	// parent-lifetime goroutine — which a child spawned to OUTLIVE us cannot
	// survive. Stderr rather than stdout so the child's lines never interleave
	// with user-facing output.
	//
	// THIS PROCESS IS A DAEMON, so that stderr is whatever started it — commonly
	// a pipe held by a container runtime or a supervisor — and the child is
	// spawned to outlive the shutdown this handoff triggers. The child survives
	// that pipe's reader going away because the restart verb installs the same
	// stderr resilience the daemon has (runRestartDaemon), not because it refuses
	// the stream: a handoff child dying mid-restart leaves no daemon at all.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// HALF ONE. The child gets its OWN process group so the stop it is about to
	// issue does not kill it. The attributes are built per-platform because
	// syscall.SysProcAttr is a different struct on each one — Setpgid on unix,
	// CREATE_NEW_PROCESS_GROUP on windows, neither field existing on the other's
	// struct — see handoff_procattr_unix.go and handoff_procattr_windows.go.
	cmd.SysProcAttr = handoffSysProcAttr()

	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		return fmt.Errorf("start restart handoff %s: %w", name, err)
	}
	// Release rather than Wait: the child must outlive this process, which it
	// is about to stop.
	_ = cmd.Process.Release()
	_ = devNull.Close()
	return nil
}
