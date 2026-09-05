// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManageStatus_ServerBinaryVersionAndSkew asserts manage(status) renders the
// installed server BINARY's version and its skew in text, and carries the
// matching JSON keys.
func TestManageStatus_ServerBinaryVersionAndSkew(t *testing.T) {
	t.Run("differing stamps render the binary line and its skew", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            fakeLiveness{status: runningStatusMap()},
			clientVer:       "v0.4.11", daemonVer: "v0.4.11", daemonKnown: true,
			serverBinVer: "v0.4.10", serverBinKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Server binary version: v0.4.10")
		assert.Contains(t, body, "binary skew:")
		assert.NotContains(t, body, "version skew:",
			"the daemon agrees here, so the DAEMON skew line must stay quiet — the two skews are independent")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "v0.4.10", got["server_binary_version"])
		assert.Equal(t, true, got["server_binary_skew"])
		assert.Equal(t, false, got["version_skew"])
	})

	// THE DISCRIMINATING CONTROL.
	t.Run("equal stamps render the line but report no skew", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            fakeLiveness{status: runningStatusMap()},
			clientVer:       "v0.4.10", daemonVer: "v0.4.10", daemonKnown: true,
			serverBinVer: "v0.4.10", serverBinKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Server binary version: v0.4.10")
		assert.NotContains(t, body, "binary skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, false, got["server_binary_skew"])
	})

	t.Run("an unreadable server binary renders neither line and carries no key", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            fakeLiveness{status: runningStatusMap()},
			clientVer:       "v0.4.10", daemonVer: "v0.4.10", daemonKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.NotContains(t, body, "Server binary version:")
		assert.NotContains(t, body, "binary skew:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.NotContains(t, got, "server_binary_version",
			"an unknown stamp must be ABSENT rather than an empty string that reads like an answer")
		assert.Equal(t, false, got["server_binary_skew"])
	})

	// The binary stamp is readable off disk with NO daemon running, so this
	// branch — which has no daemon line at all — still carries it.
	t.Run("the not-running branch still reports the installed binary", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            unhealthyLiveness{},
			clientVer:       "v0.4.11",
			serverBinVer:    "v0.4.10", serverBinKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "NOT RUNNING")
		assert.Contains(t, body, "Server binary version: v0.4.10")
		assert.Contains(t, body, "binary skew:")
		assert.NotContains(t, body, "Daemon version:")
	})

	t.Run("the cloud branch renders it too", func(t *testing.T) {
		deps := &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{gc: &modFake{}, loggedIn: true, host: "https://dev.fulminate.io"},
			clientVer:       "v0.4.11", daemonVer: "v0.4.11", daemonKnown: true,
			serverBinVer: "v0.4.10", serverBinKnown: true,
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Backend: cloud")
		assert.Contains(t, body, "Server binary version: v0.4.10")
		assert.Contains(t, body, "binary skew:")
	})
}
