// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// versionDeps embeds cloudStatusDeps (for all the ClientDeps + cloudStatusInfo methods),
// overrides LocalLiveness with an injectable fake, and implements versionInfo so the
// manage(status) version overlay renders. clientVer/daemonVer/daemonKnown drive the
// injected version stamps (daemonKnown=false models a failed daemon probe).
type versionDeps struct {
	*cloudStatusDeps
	live        LocalLiveness
	clientVer   string
	daemonVer   string
	daemonKnown bool
	// serverBinVer/serverBinKnown drive the INSTALLED knowledge-server binary
	// stamp. Zero values mean "not readable", which is the degrade the render
	// surfaces treat as no server-binary line and no binary skew — so every
	// pre-existing case in this file keeps its exact previous output.
	serverBinVer   string
	serverBinKnown bool
}

func (d *versionDeps) LocalLiveness() LocalLiveness {
	if d.live != nil {
		return d.live
	}
	return d.cloudStatusDeps.LocalLiveness()
}

func (d *versionDeps) ClientVersion() string         { return d.clientVer }
func (d *versionDeps) DaemonVersion() (string, bool) { return d.daemonVer, d.daemonKnown }
func (d *versionDeps) ServerBinaryVersion() (string, bool) {
	return d.serverBinVer, d.serverBinKnown
}

// unhealthyLiveness is a LocalLiveness whose Healthy() is false — drives the
// "daemon NOT RUNNING" branch without a nil-receiver panic.
type unhealthyLiveness struct{}

func (unhealthyLiveness) Healthy() bool                   { return false }
func (unhealthyLiveness) Status() (map[string]any, error) { return nil, nil }

// TestHandleServerStatus_Version_LocalPath drives the logged-OUT local running path:
// the version block renders in text + json when the deps implement versionInfo,
// the skew line fires on differing stamps and is quiet on equal, a failed daemon
// probe degrades to a client-version-only NON-error render, and a deps NOT
// implementing versionInfo renders no version block at all.
func TestHandleServerStatus_Version_LocalPath(t *testing.T) {
	live := fakeLiveness{status: runningStatusMap()}

	t.Run("skew fires and both lines present when stamps differ", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            live,
			clientVer:       "dev", daemonVer: "v0.4.10", daemonKnown: true,
		}
		res := handleServerStatus(opCtx(), deps, "")
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Contains(t, body, "Graph server: RUNNING")
		assert.Contains(t, body, "Client version: dev")
		assert.Contains(t, body, "Daemon version: v0.4.10")
		assert.Contains(t, body, "version skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "dev", got["client_version"])
		assert.Equal(t, "v0.4.10", got["daemon_version"])
		assert.Equal(t, true, got["version_skew"])
	})

	t.Run("skew quiet when stamps equal", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            live,
			clientVer:       "v0.4.10", daemonVer: "v0.4.10", daemonKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Client version: v0.4.10")
		assert.Contains(t, body, "Daemon version: v0.4.10")
		assert.NotContains(t, body, "version skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, false, got["version_skew"])
	})

	t.Run("failed daemon probe degrades to client-only, no error", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            live,
			clientVer:       "dev", daemonVer: "", daemonKnown: false,
		}
		res := handleServerStatus(opCtx(), deps, "")
		require.False(t, res.IsError, "status must not fail because the daemon probe did")
		body := textBodyTools(res)
		assert.Contains(t, body, "Client version: dev")
		assert.NotContains(t, body, "Daemon version:")
		assert.NotContains(t, body, "version skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "dev", got["client_version"])
		assert.NotContains(t, got, "daemon_version")
		assert.Equal(t, false, got["version_skew"])
	})

	t.Run("degrades when versionInfo not implemented", func(t *testing.T) {
		deps := &localNoHealthDeps{cloudStatusDeps: &cloudStatusDeps{loggedIn: false}, live: live}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Graph server: RUNNING")
		assert.NotContains(t, body, "Client version:")
		assert.NotContains(t, body, "Version:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.NotContains(t, got, "client_version")
		assert.NotContains(t, got, "version_skew")
	})
}

// TestHandleServerStatus_Version_NotRunning covers note-1's sub-state: with no
// daemon to probe, the "NOT RUNNING" branch still renders the always-known
// in-process client version (text + json), and never a daemon or skew line.
func TestHandleServerStatus_Version_NotRunning(t *testing.T) {
	deps := &versionDeps{
		cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
		live:            unhealthyLiveness{},
		clientVer:       "dev",
	}
	res := handleServerStatus(opCtx(), deps, "")
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "NOT RUNNING")
	assert.Contains(t, body, "Client version: dev")
	assert.NotContains(t, body, "Daemon version:")
	assert.NotContains(t, body, "version skew:")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
	assert.Equal(t, "not_running", got["status"])
	assert.Equal(t, "dev", got["client_version"])
	assert.NotContains(t, got, "daemon_version")
	assert.Equal(t, false, got["version_skew"])
}

// TestHandleServerStatus_Version_CloudPath drives the logged-IN cloud path: the
// version block (client + daemon lines, skew flag) renders in text + json, and a
// deps NOT implementing versionInfo renders no version block.
func TestHandleServerStatus_Version_CloudPath(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 1000, EdgeCount: 500, BinaryVectorCount: 200}

	t.Run("renders with skew on the cloud path", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"},
			clientVer:       "dev", daemonVer: "v0.4.10", daemonKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Backend: cloud (https://dev.fulminate.io)", "routed to the cloud status path")
		assert.Contains(t, body, "Client version: dev")
		assert.Contains(t, body, "Daemon version: v0.4.10")
		assert.Contains(t, body, "version skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "cloud", got["backend"])
		assert.Equal(t, "dev", got["client_version"])
		assert.Equal(t, "v0.4.10", got["daemon_version"])
		assert.Equal(t, true, got["version_skew"])
	})

	t.Run("degrades on the cloud path when versionInfo not implemented", func(t *testing.T) {
		deps := &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Backend: cloud")
		assert.NotContains(t, body, "Client version:")
		assert.NotContains(t, body, "Version:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.NotContains(t, got, "client_version")
		assert.NotContains(t, got, "version_skew")
	})
}
