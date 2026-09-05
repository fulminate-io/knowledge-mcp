// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// updateSeams swaps every seam the update tick reaches, so no test touches the
// network, brew, or disk. Returns a counter of install calls and a recorder of
// the releases they carried.
type updateSeams struct {
	resolves atomic.Int64
	installs atomic.Int64

	mu           sync.Mutex
	installedTag string
}

// installUpdateSeams installs stubs for the tick's four outward-facing seams
// and restores them afterwards. resolve may be nil, meaning "fail the resolve".
func installUpdateSeams(t *testing.T, s *updateSeams, latestTag string, resolveErr error, brew bool) {
	t.Helper()
	// HOME FIRST, AND IT IS ISOLATION RATHER THAN SETUP: the restart handoff
	// opens the daemon log file under ~/.knowledge as the spawned child's stdio,
	// so a tick driven under the developer's real HOME would reach into the
	// operator's live store directory.
	knowledgeDir(t)
	prevResolve, prevInstall, prevBrew := updateCheckResolveFn, updateCheckInstallFn, brewListFn
	prevOwner := serverOwnerFn

	updateCheckResolveFn = func(context.Context) (*releaseResponse, error) {
		s.resolves.Add(1)
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &releaseResponse{TagName: latestTag}, nil
	}
	updateCheckInstallFn = func(_ context.Context, rel *releaseResponse) error {
		s.installs.Add(1)
		s.mu.Lock()
		s.installedTag = rel.TagName
		s.mu.Unlock()
		return nil
	}
	brewListFn = func() bool { return brew }
	// isBrewManagedInstall consults the launchd owner on darwin too; keep that
	// leg quiet unless a case is deliberately exercising it.
	serverOwnerFn = func() string { return "" }

	t.Cleanup(func() {
		updateCheckResolveFn, updateCheckInstallFn, brewListFn = prevResolve, prevInstall, prevBrew
		serverOwnerFn = prevOwner
	})
}

// withVersionAndConfig pins the running version and the loaded config.
func withVersionAndConfig(t *testing.T, version string, autoUpdate *bool) {
	t.Helper()
	withVersion(t, version)
	t.Cleanup(config.SetForTest(&config.Config{AutoUpdate: autoUpdate}))
}

// TestUpdateCheck_GuardsAndGate drives the tick decision across the gate and
// every guard.
func TestUpdateCheck_GuardsAndGate(t *testing.T) {
	t.Run("disabled by the gate: no resolve, no install, and no goroutines", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v9.9.9", nil, false)
		withVersionAndConfig(t, "v1.0.0", new(false))

		c := &client{}
		out := c.runUpdateCheckOnce(t.Context(), Config{})
		if out.Skipped != skipDisabled {
			t.Errorf("skip reason = %q, want %q", out.Skipped, skipDisabled)
		}
		if got := s.resolves.Load(); got != 0 {
			t.Errorf("a disabled updater resolved %d time(s); the gate must refuse before any network call", got)
		}
		// The starter must spawn nothing at all, so a disabled daemon carries
		// no update goroutines and renders no update block.
		c2 := &client{}
		c2.maybeStartUpdateCheck(t.Context(), Config{})
		if c2.updateHealth != nil {
			t.Errorf("a disabled updater constructed a health tracker, so manage(status) would render a healthy zero block for a daemon that runs no updater")
		}
	})

	t.Run("dev-stamped build: no install even when a newer release exists", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v9.9.9", nil, false)
		withVersionAndConfig(t, "dev-v0.8.1-312-g214aaf97", nil)

		out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
		if out.Skipped != skipDevStamped {
			t.Errorf("skip reason = %q, want %q", out.Skipped, skipDevStamped)
		}
		if got := s.installs.Load(); got != 0 {
			t.Errorf("a dev build was clobbered by %d install(s)", got)
		}
	})

	t.Run("brew-managed install: no install", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v9.9.9", nil, true)
		withVersionAndConfig(t, "v1.0.0", nil)

		out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
		if out.Skipped != skipBrewManaged {
			t.Errorf("skip reason = %q, want %q", out.Skipped, skipBrewManaged)
		}
		if got := s.installs.Load(); got != 0 {
			t.Errorf("brew owns this install and it was updated anyway (%d install(s))", got)
		}
	})

	t.Run("container-managed install: no install, and the reason is recorded", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v9.9.9", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		t.Setenv(envManagedInstall, "container")

		c := &client{updateHealth: newUpdateHealthTracker()}
		c.logUpdateCheckOutcome(t.Context(), Config{})
		if got := s.installs.Load(); got != 0 {
			t.Errorf("a container image tried to update itself (%d install(s)); it carries a RELEASE stamp so the dev guard passes it and has no brew or launchd so the brew guard passes it, and its filesystem would fail every write daily forever", got)
		}
		snap, ok := c.UpdateCheckHealth()
		if !ok {
			t.Fatalf("the health snapshot must be readable")
		}
		if snap.NoActionReason != string(skipContainer) {
			t.Errorf("no-action reason = %q, want %q — an operator must see a deliberately idle updater rather than a silent one", snap.NoActionReason, skipContainer)
		}
	})

	t.Run("resolved release equal to the running version: no install", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v1.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)

		out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
		if out.Skipped != skipNotNewer {
			t.Errorf("skip reason = %q, want %q", out.Skipped, skipNotNewer)
		}
		if got := s.installs.Load(); got != 0 {
			t.Errorf("an equal release triggered %d install(s)", got)
		}
	})

	t.Run("resolved release OLDER than the running version: no install", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v0.9.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)

		out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
		if out.Skipped != skipNotNewer {
			t.Errorf("skip reason = %q, want %q — the loop must never downgrade", out.Skipped, skipNotNewer)
		}
		if got := s.installs.Load(); got != 0 {
			t.Errorf("an older release triggered %d install(s): that is a downgrade", got)
		}
	})

	// THE DISCRIMINATING CONTROL. Every refusal row above passes trivially
	// against an implementation that never installs anything; this is the row
	// that tells a correct guard set from a dead loop.
	t.Run("strictly newer with every guard clear: EXACTLY ONE install carrying that release", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v2.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		prevHandoff := installedClientPath
		prevExec := daemonExecCommand
		installedClientPath = func() (string, error) { return "/nonexistent/knowledge", nil }
		// A harmless real command so Start succeeds without spawning anything
		// that could restart a daemon. The handoff's own argv is asserted by
		// the restart-handoff test, not here.
		daemonExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("true") }
		t.Cleanup(func() { installedClientPath, daemonExecCommand = prevHandoff, prevExec })

		out := (&client{}).runUpdateCheckOnce(t.Context(), Config{})
		if out.Err != nil {
			t.Fatalf("the permitted case failed: %v", out.Err)
		}
		if !out.Installed {
			t.Fatalf("a strictly newer release with every guard clear must install")
		}
		if got := s.installs.Load(); got != 1 {
			t.Errorf("install called %d time(s), want exactly 1", got)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.installedTag != "v2.0.0" {
			t.Errorf("installed tag = %q, want the resolved release v2.0.0", s.installedTag)
		}
	})

	t.Run("a resolver error is recorded and the loop survives to the next tick", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "", errors.New("release endpoint unreachable"), false)
		withVersionAndConfig(t, "v1.0.0", nil)

		c := &client{updateHealth: newUpdateHealthTracker()}
		c.logUpdateCheckOutcome(t.Context(), Config{})
		c.logUpdateCheckOutcome(t.Context(), Config{})

		if got := s.resolves.Load(); got != 2 {
			t.Errorf("resolve called %d time(s) across two ticks; a failing tick must not stop the loop", got)
		}
		snap, _ := c.UpdateCheckHealth()
		if snap.ConsecutiveFailures != 2 {
			t.Errorf("consecutive failures = %d, want 2", snap.ConsecutiveFailures)
		}
		if snap.LastError == "" {
			t.Errorf("the failure must be recorded so a persistent one is visible rather than merely retried")
		}
	})
}

// TestUpdateCheck_BootDelayFiresExactlyOnce proves the boot delay is a ONE-SHOT.
//
// An implementer who writes it as a second ticker at updateCheckBootDelay
// satisfies every decision row above — the decision logic is identical — and
// ships a daemon making 288 release-endpoint requests a day instead of one.
// That is the shape this test rejects and nothing else in the plan would.
func TestUpdateCheck_BootDelayFiresExactlyOnce(t *testing.T) {
	s := &updateSeams{}
	// Resolve to the SAME version so no install is attempted; the count under
	// test is checks, not installs.
	installUpdateSeams(t, s, "v1.0.0", nil, false)
	withVersionAndConfig(t, "v1.0.0", nil)

	// Collapse the boot delay to something drivable while leaving the INTERVAL
	// long, so every check this test observes is the boot-delay goroutine's.
	prevWait := updateCheckWaitFn
	updateCheckWaitFn = func(base time.Duration) time.Duration {
		if base == updateCheckBootDelay {
			return 5 * time.Millisecond
		}
		return time.Hour
	}
	t.Cleanup(func() { updateCheckWaitFn = prevWait })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &client{}
	c.maybeStartUpdateCheck(ctx, Config{})

	// KNOWN-POSITIVE: the one-shot must actually fire, or "exactly one" would
	// be satisfied by a goroutine that never ran.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.resolves.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if s.resolves.Load() == 0 {
		t.Fatalf("the boot-delay check never fired, so the count below proves nothing")
	}

	// Now wait well past several boot-delay periods. A ticker at that cadence
	// would fire repeatedly; a one-shot fires once.
	time.Sleep(200 * time.Millisecond)
	if got := s.resolves.Load(); got != 1 {
		t.Errorf("the boot delay produced %d checks over ~40 boot-delay periods; it must be a ONE-SHOT, not a second ticker — a ticker at this cadence makes hundreds of release-endpoint requests a day", got)
	}
	cancel()
}
