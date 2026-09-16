// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// notRunningDeps drives the "daemon NOT RUNNING" arm: a fully-wired deps whose
// liveness reports unhealthy, so handleServerStatus takes the branch that builds
// its own map instead of going through addLocalDaemonJSON.
func notRunningDeps() *fullDaemonDeps {
	d := newFullDaemonDeps(false)
	d.live = unhealthyLiveness{}
	return d
}

// statusJSONFor runs manage(status) on a ctx carrying hs and returns the
// decoded JSON. loggedIn selects the cloud or the local branch, because the
// harness-session keys must land on BOTH.
func statusJSONFor(t *testing.T, hs session.HarnessSession, loggedIn bool) map[string]any {
	t.Helper()
	ctx := session.ContextWithHarnessSession(opCtx(), hs)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(ctx, newFullDaemonDeps(loggedIn), "json"))), &got))
	return got
}

// TestManageStatus_ReportsHarnessSessionOnBothBranches: the three keys land on
// the logged-in cloud path and the logged-out local path alike, carrying the
// resolution this very call was made under.
func TestManageStatus_ReportsHarnessSessionOnBothBranches(t *testing.T) {
	hs := session.HarnessSession{ID: "sess-42", Source: session.HarnessSourceClaudeHook}
	for _, loggedIn := range []bool{true, false} {
		got := statusJSONFor(t, hs, loggedIn)
		assert.Equal(t, "sess-42", got["session"], "logged_in=%v", loggedIn)
		assert.Equal(t, session.HarnessSourceClaudeHook, got["session_source"], "logged_in=%v", loggedIn)
		assert.Empty(t, got["session_reason"], "a clean resolution carries no reason (logged_in=%v)", loggedIn)
	}
}

// TestManageStatus_HarnessSessionSourceVocabulary walks every source in the
// closed vocabulary and, for each unresolved or degraded one, the reason that
// names WHICH carrier was missing. Four distinct reasons, four rows: collapsing
// them into one generic "unresolved" reds this test, which is exactly what it
// exists for — a user must be able to tell "you sent no header" from "your
// Claude hook is not firing" from "your Codex hook is installed but untrusted".
func TestManageStatus_HarnessSessionSourceVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name string
		hs   session.HarnessSession
	}{
		{name: "header", hs: session.HarnessSession{ID: "h", Source: session.HarnessSourceHeader}},
		{name: "claude-hook", hs: session.HarnessSession{ID: "c", Source: session.HarnessSourceClaudeHook}},
		{name: "codex-hook", hs: session.HarnessSession{ID: "x", Source: session.HarnessSourceCodexHook}},
		{
			name: "codex-meta with the inert-hook reason",
			hs: session.HarnessSession{
				ID: "m", Source: session.HarnessSourceCodexMeta, Reason: session.ReasonCodexHookInert,
			},
		},
		{
			name: "none: no header, no _meta",
			hs: session.HarnessSession{
				Source: session.HarnessSourceNone,
				Reason: session.ReasonNoHeader + "; " + session.ReasonNoMeta,
			},
		},
		{
			name: "none: toolUseId undelivered",
			hs: session.HarnessSession{
				Source: session.HarnessSourceNone,
				Reason: session.ReasonNoHeader + "; " + session.ReasonNoClaudeHook,
			},
		},
		{
			name: "codex-hook carrying the hook/_meta conflict reason",
			hs: session.HarnessSession{
				ID: "hook-sess", Source: session.HarnessSourceCodexHook,
				Reason: session.ReasonHookMetaConflict,
			},
		},
		{
			name: "none: codex callId with no turn metadata",
			hs: session.HarnessSession{
				Source: session.HarnessSourceNone,
				Reason: session.ReasonNoHeader + "; " + session.ReasonNoCodexTurn,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := statusJSONFor(t, tc.hs, false)
			assert.Equal(t, tc.hs.ID, got["session"])
			assert.Equal(t, tc.hs.Source, got["session_source"])
			assert.Equal(t, tc.hs.Reason, got["session_reason"])
		})
	}
}

// TestManageStatus_UnstampedCallReportsNoneNotEmpty: a call that never ran
// through the HTTP resolver (a background ctx) reports source `none` with the
// not-resolved reason, NOT an empty source. An empty source would read to a
// consumer as a fifth, undocumented state; `none` is the answer, and T2's
// account selection fails on it rather than acting on a guess.
func TestManageStatus_UnstampedCallReportsNoneNotEmpty(t *testing.T) {
	var got map[string]any
	require.NoError(t, json.Unmarshal(
		[]byte(textBodyTools(handleServerStatus(context.Background(), newFullDaemonDeps(false), "json"))), &got))

	require.Contains(t, got, "session_source")
	assert.Equal(t, session.HarnessSourceNone, got["session_source"])
	assert.Empty(t, got["session"])
	assert.Equal(t, session.ReasonNotResolved, got["session_reason"])
}

// TestManageStatus_HarnessSessionOnTheTextArms is the sibling-arm row the JSON
// tests above do not reach. TEXT is the DEFAULT format of manage(status), so
// before this the common invocation showed nothing at all about the session it
// was calling under. Every text arm renders the same three facts: the local
// running arm, the cloud arm, and the not-running arm.
func TestManageStatus_HarnessSessionOnTheTextArms(t *testing.T) {
	resolved := session.HarnessSession{ID: "sess-text", Source: session.HarnessSourceClaudeHook}
	unresolved := session.HarnessSession{
		Source: session.HarnessSourceNone,
		Reason: session.ReasonNoHeader + "; " + session.ReasonNoMeta,
	}
	for _, tc := range []struct {
		name     string
		deps     ClientDeps
		hs       session.HarnessSession
		wantBits []string
	}{
		{
			name: "local running arm, resolved", deps: newFullDaemonDeps(false), hs: resolved,
			wantBits: []string{"Harness session: sess-text", session.HarnessSourceClaudeHook},
		},
		{
			name: "cloud arm, resolved", deps: newFullDaemonDeps(true), hs: resolved,
			wantBits: []string{"Harness session: sess-text", session.HarnessSourceClaudeHook},
		},
		{
			name: "local running arm, unresolved names the reason", deps: newFullDaemonDeps(false), hs: unresolved,
			wantBits: []string{"Harness session: none", session.ReasonNoMeta},
		},
		{
			// The commonest degraded state in production is a RESOLVED one:
			// a Codex session resolved by _meta whose hook is installed and
			// untrusted. The id must not swallow the reason.
			name: "a RESOLVED codex-meta session still names its inert-hook reason",
			deps: newFullDaemonDeps(false),
			hs: session.HarnessSession{
				ID: "codex-sess", Source: session.HarnessSourceCodexMeta,
				Reason: session.ReasonCodexHookInert,
			},
			wantBits: []string{"Harness session: codex-sess", session.HarnessSourceCodexMeta, session.ReasonCodexHookInert},
		},
		{
			name: "not-running arm", deps: notRunningDeps(), hs: unresolved,
			wantBits: []string{"Harness session: none", session.ReasonNoMeta},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := session.ContextWithHarnessSession(opCtx(), tc.hs)
			body := textBodyTools(handleServerStatus(ctx, tc.deps, ""))
			for _, want := range tc.wantBits {
				assert.Contains(t, body, want, "text render must name %q:\n%s", want, body)
			}
		})
	}
}

// TestManageStatus_NotRunningJSONArmCarriesTheSession: the not_running JSON arm
// builds its OWN map and never calls addLocalDaemonJSON, so it had to carry the
// three keys explicitly. The resolution is a fact about this CALL and is known
// whether or not a graph server is up.
func TestManageStatus_NotRunningJSONArmCarriesTheSession(t *testing.T) {
	hs := session.HarnessSession{ID: "sess-nr", Source: session.HarnessSourceHeader}
	ctx := session.ContextWithHarnessSession(opCtx(), hs)
	var got map[string]any
	require.NoError(t, json.Unmarshal(
		[]byte(textBodyTools(handleServerStatus(ctx, notRunningDeps(), "json"))), &got))

	assert.Equal(t, "not_running", got["status"])
	for _, k := range []string{"session", "session_source", "session_reason"} {
		require.Contains(t, got, k, "the not_running arm must carry %q", k)
	}
	assert.Equal(t, "sess-nr", got["session"])
	assert.Equal(t, session.HarnessSourceHeader, got["session_source"])
}
