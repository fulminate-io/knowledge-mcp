// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
)

// lifecycleHiveCaller is the HiveCaller fake injected for both the stamped and
// the raw caller slot. It counts each half of the seam SEPARATELY: the boot
// re-detection browse goes through Execute, while an agent hive op (register /
// send / claim) would go through Hive — the re-detection cases assert the latter
// stays at zero, which is what "re-detected with no new hive call" means.
type lifecycleHiveCaller struct {
	mu       sync.Mutex
	executes int
	hives    int
	nodes    []*knowledgev1.Node
}

func (c *lifecycleHiveCaller) Hive(
	_ context.Context, _ *knowledgev1.HiveRequest,
) (*knowledgev1.HiveResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hives++
	return &knowledgev1.HiveResponse{}, nil
}

func (c *lifecycleHiveCaller) Execute(
	_ context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executes++
	return &knowledgev1.ExecuteResponse{Nodes: c.nodes}, nil
}

func (c *lifecycleHiveCaller) counts() (executes, hives int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executes, c.hives
}

// newLifecycleLoops builds a hiveLoops with every dependency faked, matching
// what newHiveLoops assembles in production minus the stamping wrapper around
// the caller.
func newLifecycleLoops(
	reg *hivemonitor.Registry,
	caller hivemonitor.HiveCaller,
	snaps func() []hivemonitor.SessionSnapshot,
) *hiveLoops {
	if snaps == nil {
		snaps = func() []hivemonitor.SessionSnapshot { return nil }
	}
	return &hiveLoops{
		hive:            caller,
		snapshots:       snaps,
		registry:        reg,
		ban:             hivemonitor.NewBanSet(),
		reaperCfg:       hivemonitor.DefaultReaperConfig(),
		redetectChecked: map[string]bool{},
	}
}

// loopPointers exposes the loop instances themselves, so a test can tell "still
// running" apart from "torn down and rebuilt" — running() reports only presence.
func (l *hiveLoops) loopPointers() (*hivemonitor.Monitor, *hivemonitor.HiveReaper) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.monitor, l.reaper
}

// settleGoroutines polls until the goroutine count drops to at most baseline+1
// or the deadline passes, returning the last count read. Stopped loops unwind
// asynchronously, so a bare comparison right after Stop is racy. Mirrors the
// in-repo NumGoroutine-polling idiom (the repo carries no goleak dependency).
func settleGoroutines(baseline int) int {
	deadline := time.Now().Add(2 * time.Second)
	n := runtime.NumGoroutine()
	for time.Now().Before(deadline) {
		n = runtime.NumGoroutine()
		if n <= baseline+1 {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return n
}

// writeCodexRollout writes a minimal codex rollout whose session_meta declares
// cwd, so hivemonitor.ResolveTranscript's cwd-scan fallback binds a snapshot
// carrying that Cwd to harnessID. A snapshot PID of 0 short-circuits the
// deterministic lsof probe, so the fixture needs no live process, and HOME
// (which os.UserHomeDir reads) points the resolver at the temp tree.
func writeCodexRollout(t *testing.T, home, cwd, harnessID string) {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "01", "01")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	body := `{"type":"session_meta","payload":{"id":"` + harnessID + `","cwd":"` + cwd + `"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-"+harnessID+".jsonl"), []byte(body), 0o600))
}

// memberNodes is a hive_member browse result for harnessID with the given status.
func memberNodes(harnessID, status string) []*knowledgev1.Node {
	return []*knowledgev1.Node{{
		Id:       "member-" + harnessID,
		Type:     "hive_member",
		Status:   status,
		Metadata: map[string]string{"session": harnessID, "hive": "test-hive"},
	}}
}

// TestHiveLoops_ActivationStartsAndIdleStops is the core lifecycle assertion:
// nothing runs until a session is hive-active, both loops run while one is, and
// both stop when the last one ends.
func TestHiveLoops_ActivationStartsAndIdleStops(t *testing.T) {
	t.Parallel()

	reg := hivemonitor.NewRegistry()
	l := newLifecycleLoops(reg, &lifecycleHiveCaller{}, nil)
	t.Cleanup(l.stopAll)
	reg.SetHiveActivityHook(l.reconcile)

	monitor, reaper := l.running()
	require.False(t, monitor, "monitor must not run before any session is hive-active")
	require.False(t, reaper, "reaper must not run before any session is hive-active")

	reg.MarkHiveActive("s1")
	monitor, reaper = l.running()
	assert.True(t, monitor, "the first hive-active session must start the monitor")
	assert.True(t, reaper, "the first hive-active session must start the reaper")

	reg.EndHiveSession("s1")
	monitor, reaper = l.running()
	assert.False(t, monitor, "the last session ending must stop the monitor")
	assert.False(t, reaper, "the last session ending must stop the reaper")
}

// TestHiveLoops_ActivationIsIdempotent catches a startLocked that builds a second
// pair of loops for a second concurrent session and leaks the first: the loop
// INSTANCES must be the same objects after the second activation, not merely
// present.
func TestHiveLoops_ActivationIsIdempotent(t *testing.T) {
	t.Parallel()

	reg := hivemonitor.NewRegistry()
	l := newLifecycleLoops(reg, &lifecycleHiveCaller{}, nil)
	t.Cleanup(l.stopAll)
	reg.SetHiveActivityHook(l.reconcile)

	reg.MarkHiveActive("s1")
	firstMonitor, firstReaper := l.loopPointers()
	require.NotNil(t, firstMonitor)
	require.NotNil(t, firstReaper)

	reg.MarkHiveActive("s2")
	monitor, reaper := l.running()
	assert.True(t, monitor)
	assert.True(t, reaper)

	secondMonitor, secondReaper := l.loopPointers()
	assert.Same(t, firstMonitor, secondMonitor, "a second session must not rebuild the monitor")
	assert.Same(t, firstReaper, secondReaper, "a second session must not rebuild the reaper")

	// The first session ending is NOT the last one — the loops stay up.
	reg.EndHiveSession("s1")
	monitor, reaper = l.running()
	assert.True(t, monitor, "loops must stay up while a second session is still hive-active")
	assert.True(t, reaper, "loops must stay up while a second session is still hive-active")
}

// TestHiveLoops_GatesSuppressActivation proves the NoHiveMonitor / NoHiveReaper
// gates sit ON TOP of the lifecycle. The control leg runs first: without it, the
// gated leg's false/false is indistinguishable from a fixture that could never
// start loops at all.
//
// Deliberately NOT parallel: it reads runtime.NumGoroutine(), which is
// process-global, and the parallel lifecycle tests start and stop loops of their
// own. Serial tests never overlap the parallel ones.
func TestHiveLoops_GatesSuppressActivation(t *testing.T) {
	t.Run("control: an ungated controller does start both loops", func(t *testing.T) {
		reg := hivemonitor.NewRegistry()
		l := newLifecycleLoops(reg, &lifecycleHiveCaller{}, nil)
		t.Cleanup(l.stopAll)
		reg.SetHiveActivityHook(l.reconcile)

		reg.MarkHiveActive("s1")
		monitor, reaper := l.running()
		require.True(t, monitor, "control leg: the fixture must be able to start the monitor")
		require.True(t, reaper, "control leg: the fixture must be able to start the reaper")
	})

	t.Run("both gates on suppress activation entirely", func(t *testing.T) {
		baseline := runtime.NumGoroutine()

		reg := hivemonitor.NewRegistry()
		l := newLifecycleLoops(reg, &lifecycleHiveCaller{}, nil)
		l.noMonitor = true
		l.noReaper = true
		t.Cleanup(l.stopAll)
		reg.SetHiveActivityHook(l.reconcile)

		reg.MarkHiveActive("s1")
		monitor, reaper := l.running()
		assert.False(t, monitor, "NoHiveMonitor must hard-off the monitor even with a hive session active")
		assert.False(t, reaper, "NoHiveReaper must hard-off the reaper even with a hive session active")
		assert.LessOrEqual(t, runtime.NumGoroutine(), baseline+1,
			"a gated-off activation must spawn no loop goroutines")
	})
}

// TestHiveLoops_ChurnLeavesNoGoroutines drives twenty rapid session start/stop
// cycles and asserts the goroutine count returns to baseline. The POSITIVE half
// is asserted inside the loop — without it, a controller that never started
// anything would satisfy the final return-to-baseline vacuously.
//
// Deliberately NOT parallel: runtime.NumGoroutine() is process-global and the
// parallel lifecycle tests start loops of their own.
func TestHiveLoops_ChurnLeavesNoGoroutines(t *testing.T) {
	reg := hivemonitor.NewRegistry()
	l := newLifecycleLoops(reg, &lifecycleHiveCaller{}, nil)
	t.Cleanup(l.stopAll)
	reg.SetHiveActivityHook(l.reconcile)

	baseline := runtime.NumGoroutine()
	for i := range 20 {
		reg.MarkHiveActive("s1")
		if i == 0 {
			monitor, reaper := l.running()
			require.True(t, monitor, "cycle %d: the monitor must actually be up", i)
			require.True(t, reaper, "cycle %d: the reaper must actually be up", i)
			assert.Greater(t, runtime.NumGoroutine(), baseline,
				"cycle %d: running loops must show above the baseline goroutine count", i)
		}
		reg.EndHiveSession("s1")
	}

	assert.LessOrEqual(t, settleGoroutines(baseline), baseline+1,
		"twenty start/stop cycles must leave no goroutines behind")
}

// TestHiveLoops_BootWiringStartsNoLoops asserts the boot wiring INSTALLS the
// lifecycle instead of starting loops: a daemon that never joins a hive runs
// neither loop. The second leg is the positive control on the same wiring —
// without it the opening false/false is indistinguishable from a controller
// wired to nothing at all.
//
// Deliberately NOT parallel: it reads runtime.NumGoroutine().
func TestHiveLoops_BootWiringStartsNoLoops(t *testing.T) {
	newWiring := func() (*hivemonitor.Registry, *client, *graphclient.HTTPServer) {
		reg := hivemonitor.NewRegistry()
		// router stays nil: tests that build *client directly leave it nil, and
		// nothing here reaches the wire.
		c := &client{claimRegistry: reg, banSet: hivemonitor.NewBanSet()}
		hs := graphclient.NewHTTPServer(
			graphclient.NewMCPClient(graphclient.MCPClientConfig{Version: "test"}), 15029, nil, reg)
		return reg, c, hs
	}

	t.Run("startHiveLoops starts nothing and returns a callable stop closure", func(t *testing.T) {
		_, c, hs := newWiring()
		baseline := runtime.NumGoroutine()

		stop := c.startHiveLoops(Config{}, hs)
		require.NotNil(t, stop, "the boot wiring must still return a stop closure")
		assert.LessOrEqual(t, runtime.NumGoroutine(), baseline+1,
			"boot wiring must spawn no loop goroutines with no hive-active session")

		stop() // safe to call when nothing ever started
		assert.LessOrEqual(t, settleGoroutines(baseline), baseline+1)
	})

	t.Run("the installed controller follows the session lifecycle", func(t *testing.T) {
		reg, c, hs := newWiring()
		baseline := runtime.NumGoroutine()

		l := c.newHiveLoops(Config{}, hs)
		t.Cleanup(l.stopAll)

		monitor, reaper := l.running()
		require.False(t, monitor, "no loop may run before a session is hive-active")
		require.False(t, reaper, "no loop may run before a session is hive-active")

		reg.MarkHiveActive("s1")
		monitor, reaper = l.running()
		assert.True(t, monitor, "the boot wiring's activity hook must start the monitor")
		assert.True(t, reaper, "the boot wiring's activity hook must start the reaper")

		reg.EndHiveSession("s1")
		monitor, reaper = l.running()
		assert.False(t, monitor)
		assert.False(t, reaper)
		assert.LessOrEqual(t, settleGoroutines(baseline), baseline+1,
			"the loops must unwind when the last hive session ends")
	})
}

// TestHiveLoops_BootRedetectMarksExistingMember covers the restart case: a
// reconnecting session whose harness still holds a non-evicted hive_member is
// re-marked hive-active from the session-open hook alone, with NO new hive call,
// so a daemon restart does not silently drop a live worker's heartbeat. The
// bounds are asserted in the same test — a re-check, an unresolvable session, an
// evicted member, and a disarmed window.
func TestHiveLoops_BootRedetectMarksExistingMember(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	armed := func(caller hivemonitor.HiveCaller, snaps func() []hivemonitor.SessionSnapshot,
	) (*hivemonitor.Registry, *hiveLoops) {
		reg := hivemonitor.NewRegistry()
		l := newLifecycleLoops(reg, caller, snaps)
		t.Cleanup(l.stopAll)
		l.redetectUntil = time.Now().Add(time.Minute)
		reg.SetHiveActivityHook(l.reconcile)
		reg.SetSessionOpenHook(l.onSessionOpened)
		return reg, l
	}

	t.Run("a reconnecting member restarts the loops with no hive call", func(t *testing.T) {
		cwd := t.TempDir()
		writeCodexRollout(t, home, cwd, "harness-live")
		caller := &lifecycleHiveCaller{nodes: memberNodes("harness-live", "active")}
		snaps := []hivemonitor.SessionSnapshot{{ID: "mcp-live", Cwd: cwd, PID: 0, Comm: "codex"}}
		reg, l := armed(caller, func() []hivemonitor.SessionSnapshot { return snaps })

		reg.NoteSessionOpened("mcp-live")

		monitor, reaper := l.running()
		assert.True(t, monitor, "re-detection must start the monitor")
		assert.True(t, reaper, "re-detection must start the reaper")
		assert.Equal(t, 1, reg.HiveActiveCount())
		executes, hives := caller.counts()
		assert.Equal(t, 1, executes, "re-detection costs exactly ONE hive_member browse")
		assert.Zero(t, hives, "re-detection must issue NO hive call")

		// A second session-open event for an already-checked session browses again
		// for nothing.
		reg.NoteSessionOpened("mcp-live")
		executes, hives = caller.counts()
		assert.Equal(t, 1, executes, "an already-checked session must not be browsed twice")
		assert.Zero(t, hives)
	})

	t.Run("an unresolvable session is retried, not forfeited", func(t *testing.T) {
		cwd := t.TempDir() // no rollout yet — the transcript does not resolve
		caller := &lifecycleHiveCaller{nodes: memberNodes("harness-late", "active")}
		snaps := []hivemonitor.SessionSnapshot{{ID: "mcp-late", Cwd: cwd, PID: 0, Comm: "codex"}}
		reg, l := armed(caller, func() []hivemonitor.SessionSnapshot { return snaps })

		reg.NoteSessionOpened("mcp-late")
		executes, _ := caller.counts()
		require.Zero(t, executes, "an unresolved transcript must issue no browse")
		monitor, _ := l.running()
		require.False(t, monitor)

		// The same session becomes resolvable and a later event inside the window
		// still recovers it — proof it was never marked checked.
		writeCodexRollout(t, home, cwd, "harness-late")
		reg.NoteSessionOpened("mcp-late")

		executes, hives := caller.counts()
		assert.Equal(t, 1, executes, "a previously unresolvable session must be retried")
		assert.Zero(t, hives)
		assert.Equal(t, 1, reg.HiveActiveCount())
		monitor, reaper := l.running()
		assert.True(t, monitor)
		assert.True(t, reaper)
	})

	t.Run("an evicted member does not activate", func(t *testing.T) {
		cwd := t.TempDir()
		writeCodexRollout(t, home, cwd, "harness-evicted")
		caller := &lifecycleHiveCaller{nodes: memberNodes("harness-evicted", "evicted")}
		snaps := []hivemonitor.SessionSnapshot{{ID: "mcp-evicted", Cwd: cwd, PID: 0, Comm: "codex"}}
		reg, l := armed(caller, func() []hivemonitor.SessionSnapshot { return snaps })

		reg.NoteSessionOpened("mcp-evicted")

		executes, _ := caller.counts()
		require.Equal(t, 1, executes, "the member browse must have happened (else this zero proves nothing)")
		assert.Zero(t, reg.HiveActiveCount(), "an evicted member must not be re-marked hive-active")
		monitor, reaper := l.running()
		assert.False(t, monitor)
		assert.False(t, reaper)
	})

	t.Run("a disarmed window issues nothing", func(t *testing.T) {
		cwd := t.TempDir()
		writeCodexRollout(t, home, cwd, "harness-disarmed")
		caller := &lifecycleHiveCaller{nodes: memberNodes("harness-disarmed", "active")}
		snaps := []hivemonitor.SessionSnapshot{{ID: "mcp-disarmed", Cwd: cwd, PID: 0, Comm: "codex"}}
		reg, l := armed(caller, func() []hivemonitor.SessionSnapshot { return snaps })
		l.redetectUntil = time.Time{} // disarmed

		reg.NoteSessionOpened("mcp-disarmed")

		executes, hives := caller.counts()
		assert.Zero(t, executes, "a disarmed window must issue no browse")
		assert.Zero(t, hives)
		assert.Zero(t, reg.HiveActiveCount())

		// An expired window is disarmed too.
		l.redetectUntil = time.Now().Add(-time.Minute)
		reg.NoteSessionOpened("mcp-disarmed")
		executes, _ = caller.counts()
		assert.Zero(t, executes, "an expired window must issue no browse")
	})
}
