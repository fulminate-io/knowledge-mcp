// SPDX-License-Identifier: Apache-2.0

// no_resurrection_boot_test.go — the regression for the two boot-time
// resurrection paths this package no longer has: the worker runtime's registry
// walk, and the hive loops' session-open re-detection. Both re-armed work from
// state a PREVIOUS process left behind, which is exactly what a background
// process interacting with a graph nobody asked about looks like.
//
// A crashed or restarted worker requires intervention: nothing here resumes its
// claims or leases, and the recovery path is an explicit new interaction. The
// controls below exercise that path rather than a compensating mechanism.

package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
)

// recordingWorkerLister is the worker-registry backend, holding rows a previous
// session persisted. It satisfies dream's unexported workerLister, whose single
// List method means EVERY call it can receive is recorded — there is no
// unprogrammed arm that could answer a zero value silently.
type recordingWorkerLister struct {
	mu      sync.Mutex
	calls   []string
	workers []dream.Worker
}

func (l *recordingWorkerLister) List(_ context.Context) ([]dream.Worker, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "worker-list")
	return append([]dream.Worker(nil), l.workers...), nil
}

func (l *recordingWorkerLister) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// recordingHiveCaller is the hive backend holding a live pre-restart member. It
// records EVERY request on both halves of the seam, including ones it was not
// programmed for: an unrecognized Execute is still appended (as its target and
// node types) rather than answered with a silent zero value, so an unexpected
// read shows up in the stream instead of vanishing into it.
type recordingHiveCaller struct {
	mu       sync.Mutex
	requests []string
	nodes    []*knowledgev1.Node
}

func (c *recordingHiveCaller) Hive(
	_ context.Context, req *knowledgev1.HiveRequest,
) (*knowledgev1.HiveResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, "hive:"+req.GetOp().String())
	return &knowledgev1.HiveResponse{}, nil
}

func (c *recordingHiveCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	types := strings.Join(req.GetQuery().GetSelection().GetNodeTypes(), ",")
	c.requests = append(c.requests, "execute:"+req.GetTarget().GetGraph()+":"+types)
	return &knowledgev1.ExecuteResponse{Nodes: c.nodes}, nil
}

func (c *recordingHiveCaller) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

// liveMemberNodes is the pre-restart hive membership the cloud still holds for
// harnessID — the state boot re-detection used to resurrect loops from.
func liveMemberNodes(harnessID string) []*knowledgev1.Node {
	return []*knowledgev1.Node{{
		Id:       "member-" + harnessID,
		Type:     "hive_member",
		Status:   "active",
		Metadata: map[string]string{"session": harnessID, "hive": "test-hive"},
	}}
}

// TestNoResurrectionOnBoot boots a client whose backend holds BOTH persisted,
// enabled worker rows AND a live pre-restart hive member, and asserts nothing
// starts until an interaction arrives. Each half pairs its zero with a
// known-positive control on the SAME fixture, so a zero can never come from a
// fixture that was never wired.
func TestNoResurrectionOnBoot(t *testing.T) {
	t.Run("worker: persisted enabled workers are not started at boot", func(t *testing.T) {
		lister := &recordingWorkerLister{workers: []dream.Worker{{
			Name:                "persisted-worker",
			Provider:            config.ProviderClaudeCLI,
			Model:               "test-model",
			SystemPrompt:        "persisted from a previous session",
			ToolAllowlist:       []string{"search"},
			MaxIterations:       1,
			MaxWallclockSeconds: 5,
			Enabled:             true,
			Triggers:            []dream.Trigger{{Event: dream.EventToolCompleted}},
		}}}

		bus := dream.NewEventBus()
		// Construct the Runner exactly as buildRuntime does. Construction IS the
		// whole of the boot wiring now.
		runner := dream.NewRunner(dream.NewRegistry(lister), bus, t.TempDir(), nil, nil)
		t.Cleanup(func() { runner.Stop(2 * time.Second) })

		assert.Equal(t, []string(nil), lister.recorded(),
			"wiring the worker runtime must issue NO registry read: a persisted worker "+
				"is data, and a daemon restart must not re-arm it")

		// The mechanism itself must be gone, not merely unreached. The control
		// immediately below asserts the manual path IS present, so a probe that
		// could never see any method is not what makes this pass.
		if _, ok := any(runner).(interface {
			Start(context.Context) error
		}); ok {
			t.Fatal("dream.Runner still exposes Start — the boot registry walk is back")
		}
		_, ok := any(runner).(interface {
			OnManualTrigger(context.Context, string, json.RawMessage) error
		})
		require.True(t, ok, "control: the manual-trigger entry point must still exist")

		// CONTROL — the explicit interaction that IS the recovery path. Its
		// completion event proves the same fixture really can dispatch, which is
		// what makes the zero above falsifiable.
		done := bus.Subscribe(dream.Trigger{Event: dream.EventWorkerCompleted})
		require.NoError(t, runner.OnManualTrigger(
			context.Background(), "persisted-worker", json.RawMessage(`"go"`)))

		select {
		case ev := <-done:
			assert.Equal(t, "persisted-worker", ev.Worker)
		case <-time.After(10 * time.Second):
			t.Fatal("control: the manual trigger never dispatched")
		}
		assert.Equal(t, []string{"worker-list"}, lister.recorded(),
			"control: the explicit trigger must reach the registry exactly once")
	})

	t.Run("hive: a live pre-restart member does not start the loops", func(t *testing.T) {
		reg := hivemonitor.NewRegistry()
		caller := &recordingHiveCaller{nodes: liveMemberNodes("harness-live")}
		// A session from before the restart, present and reconnecting.
		snaps := []hivemonitor.SessionSnapshot{{ID: "mcp-live", Cwd: t.TempDir(), Comm: "codex"}}
		l := newLifecycleLoops(reg, caller, func() []hivemonitor.SessionSnapshot { return snaps })
		t.Cleanup(l.stopAll)
		reg.SetHiveActivityHook(l.reconcile)

		monitor, reaper := l.running()
		require.False(t, monitor, "no loop may run from pre-restart membership alone")
		require.False(t, reaper, "no loop may run from pre-restart membership alone")
		assert.Equal(t, []string(nil), caller.recorded(),
			"a live pre-restart member must cost ZERO graph reads: nothing may look for it")

		// The seam re-detection traveled through must be gone. SetHiveActivityHook
		// is the known-positive control for the same probe shape — it must still be
		// there, so the two assertions cannot both pass vacuously.
		if _, ok := any(reg).(interface{ SetSessionOpenHook(func(string)) }); ok {
			t.Fatal("the claim Registry still exposes a session-open hook — boot re-detection is back")
		}
		if _, ok := any(reg).(interface{ NoteSessionOpened(string) }); ok {
			t.Fatal("the claim Registry still exposes the session-open notification — boot re-detection is back")
		}
		_, ok := any(reg).(interface{ SetHiveActivityHook(func()) })
		require.True(t, ok, "control: the hive-activity hook must still exist")

		// CONTROL — the explicit re-join. MarkHiveActive is what the hive tool
		// intercept calls on a this-process hive call.
		reg.MarkHiveActive("mcp-live")
		monitor, reaper = l.running()
		assert.True(t, monitor, "control: an in-process hive interaction must start the monitor")
		assert.True(t, reaper, "control: an in-process hive interaction must start the reaper")
	})
}

// TestBootWiringInstallsNoSessionOpenPath asserts the PRODUCTION wiring path —
// the one a daemon actually runs — arms nothing at boot. It complements the
// controller-level assertions above, which use the test fixture so requests can
// be recorded.
func TestBootWiringInstallsNoSessionOpenPath(t *testing.T) {
	reg := hivemonitor.NewRegistry()
	// router stays nil: tests that build *client directly leave it nil, and
	// nothing here reaches the wire.
	c := &client{claimRegistry: reg, banSet: hivemonitor.NewBanSet()}
	hs := graphclient.NewHTTPServer(
		graphclient.NewMCPClient(graphclient.MCPClientConfig{Version: "test"}), 15031, nil, reg)

	l := c.newHiveLoops(Config{}, hs)
	t.Cleanup(l.stopAll)

	monitor, reaper := l.running()
	require.False(t, monitor, "boot wiring must arm no monitor")
	require.False(t, reaper, "boot wiring must arm no reaper")
	assert.Zero(t, reg.HiveActiveCount(), "boot wiring must mark no session hive-active")

	// CONTROL: the installed controller does follow this-process hive activity.
	reg.MarkHiveActive("s1")
	monitor, reaper = l.running()
	assert.True(t, monitor, "control: the wiring's activity hook must start the monitor")
	assert.True(t, reaper, "control: the wiring's activity hook must start the reaper")
}
