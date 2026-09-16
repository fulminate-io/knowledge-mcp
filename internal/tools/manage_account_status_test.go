// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// TestStatus_ReportsTheCallersAccountAndItsSource walks the three sources a
// status render must distinguish, on BOTH branches that carry the fields.
// statusHarnessSession is the session id every row here binds under: the rows
// are about the ACCOUNT the session resolves to, so which session it is carries
// no information and a parameter no caller varies would read as a choice.
const statusHarnessSession = "harness-123"

// harnessCtx stamps a resolved harness session on the request context — the
// session package's carrier, which is what the status render reads and what the
// account ladder's session rung resolves through. One stamp feeds both, which is
// the point of there being one carrier.
func harnessCtx(t *testing.T) context.Context {
	t.Helper()
	return session.ContextWithHarnessSession(opCtx(), session.HarnessSession{
		ID: statusHarnessSession, Source: session.HarnessSourceClaudeHook,
	})
}

func TestStatus_ReportsTheCallersAccountAndItsSource(t *testing.T) {
	const (
		sessionAccount = "acct_01SESSIONSESSIONS"
		headerAccount  = "acct_01HEADERHEADERHEA"
	)

	t.Run("no session — the machine-wide selection, with the unresolved reason", func(t *testing.T) {
		for _, loggedIn := range []bool{true, false} {
			deps, _ := newAccountDeps(t)
			deps.fullDaemonDeps = newFullDaemonDeps(loggedIn)
			selectAccountForTest(t)
			harnessSession(t, "")

			got := statusJSON(opCtx(), t, deps)
			assert.Equal(t, statusAccountGlobal, got["account"], "loggedIn=%v", loggedIn)
			assert.Equal(t, "global", got["account_source"], "loggedIn=%v", loggedIn)
			assert.Empty(t, got["session"], "loggedIn=%v", loggedIn)
			assert.Equal(t, session.HarnessSourceNone, got["session_source"], "loggedIn=%v", loggedIn)
			assert.NotEmpty(t, got["session_reason"], "loggedIn=%v: an unresolved session says which carrier was missing", loggedIn)
		}
	})

	t.Run("a bound session — the session's account and source", func(t *testing.T) {
		deps, store := newAccountDeps(t)
		selectAccountForTest(t)
		t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(store))
		require.NoError(t, bindOK(t, store, "harness-123", sessionAccount))

		got := statusJSON(harnessCtx(t), t, deps)
		assert.Equal(t, sessionAccount, got["account"])
		assert.Equal(t, "session", got["account_source"])
		assert.Equal(t, "harness-123", got["session"])
		assert.Equal(t, session.HarnessSourceClaudeHook, got["session_source"])
		assert.Empty(t, got["account_reason"], "a clean resolution explains nothing")
	})

	t.Run("a per-request header outranks both", func(t *testing.T) {
		deps, store := newAccountDeps(t)
		selectAccountForTest(t)
		t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(store))
		require.NoError(t, bindOK(t, store, "harness-123", sessionAccount))

		var got map[string]any
		ctx := auth.WithHeaderAccount(harnessCtx(t), headerAccount)
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(ctx, deps, "json"))), &got))
		assert.Equal(t, headerAccount, got["account"])
		assert.Equal(t, "header", got["account_source"])
		assert.Equal(t, "harness-123", got["session"], "the session is still reported, it just did not win")
	})

	t.Run("an unreadable binding store is reported, and the account falls to global", func(t *testing.T) {
		deps, store := newAccountDeps(t)
		selectAccountForTest(t)
		t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(store))
		harnessSession(t, "")
		require.NoError(t, os.WriteFile(store.Path(), []byte("{ not json"), 0o600))

		got := statusJSON(opCtx(), t, deps)
		assert.Contains(t, got, "session_binding_store_error", "status must say the bindings are not in effect")
		assert.Contains(t, got["session_binding_store_error"], "repair or delete it")
		assert.Equal(t, "global", got["account_source"],
			"a call with no session still resolves the machine-wide account")
	})
}

// TestStatus_TextArmCarriesTheSameFacts pins the human render: the four facts
// appear there too, so the two arms cannot drift.
func TestStatus_TextArmCarriesTheSameFacts(t *testing.T) {
	deps, store := newAccountDeps(t)
	selectAccountForTest(t)
	t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(store))
	const boundAccount = "acct_01SESSIONSESSIONS"
	require.NoError(t, bindOK(t, store, "harness-123", boundAccount))
	ctx := harnessCtx(t)

	for _, arm := range []struct {
		name string
		body string
	}{
		{"logged out", textBodyTools(handleServerStatus(ctx, newLoggedOutAccountDeps(t, store), ""))},
		{"logged in", textBodyTools(handleServerStatus(ctx, deps, ""))},
	} {
		t.Run(arm.name, func(t *testing.T) {
			assert.Contains(t, arm.body, "Account: "+boundAccount+" (session)")
			assert.Contains(t, arm.body, "harness-123", "the harness-session line is rendered beside it")
		})
	}
}

// newLoggedOutAccountDeps is the logged-out twin of newAccountDeps over one store.
func newLoggedOutAccountDeps(t *testing.T, store *SessionAccountStore) *accountDaemonDeps {
	t.Helper()
	return &accountDaemonDeps{fullDaemonDeps: newFullDaemonDeps(false), bindings: store}
}

// TestStatus_NotRunningBodyCarriesTheAccountKeys covers the THIRD render path:
// handleServerStatus's not-running branch, which does NOT go through the shared
// local-daemon helper and so lands the four keys itself.
//
// THE ACCOUNT ROUTING IS NOT A LOCAL-DAEMON FACT. It is resolved from the
// calling request's own context and is just as true when no local graph server
// is running, so it is present on every arm. A consumer reads account_source's
// PRESENCE to decide whether this daemon can answer the question at all, and a
// body that omitted it would read as a daemon too old to know rather than as one
// whose local server happens to be down.
func TestStatus_NotRunningBodyCarriesTheAccountKeys(t *testing.T) {
	selectAccountForTest(t)
	harnessSession(t, "")
	deps := &cloudStatusDeps{gc: nil, local: closedGraphClient(t), loggedIn: false}

	got := statusJSON(opCtx(), t, deps)
	require.Equal(t, "not_running", got["status"], "this is the branch under test")
	for _, k := range []string{"account", "account_source", "session", "session_source"} {
		assert.Contains(t, got, k, "the not_running body must carry the per-request key %q", k)
	}
	assert.Equal(t, statusAccountGlobal, got["account"], "and it is the CALLING request's account, not a placeholder")
	assert.Equal(t, "global", got["account_source"])

	// The LOCAL-DAEMON facts are still absent here, which is what keeps the rows
	// above a statement about the account keys rather than about this branch
	// having quietly grown the whole local-daemon block.
	for _, k := range []string{"pid", "graph_path", "coverage", "doctor"} {
		assert.NotContains(t, got, k, "the not_running body still carries no local-daemon field %q", k)
	}

	// The text arm of the same branch carries the same facts.
	body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
	assert.Contains(t, body, "Graph server: NOT RUNNING")
	assert.Contains(t, body, "Account: "+statusAccountGlobal+" (global)")
}

// TestStatus_AccountKeysOnEveryArm is the ALWAYS-EMITS pin for the four
// per-request keys, over all THREE render paths at once: the logged-in cloud
// body, the logged-out local body, and the not-running body.
//
// The three are separate functions with separate composition — two go through
// the shared local-daemon helper and one does not — so "present on every arm" is
// a claim no single-arm test makes. A consumer decides what this daemon can
// answer from these keys' presence, and it reaches every arm.
func TestStatus_AccountKeysOnEveryArm(t *testing.T) {
	selectAccountForTest(t)
	harnessSession(t, "")

	arms := map[string]ClientDeps{
		"logged in (cloud body)":             newFullDaemonDeps(true),
		"logged out (local body)":            newFullDaemonDeps(false),
		"no local server (not_running body)": &cloudStatusDeps{gc: nil, local: closedGraphClient(t), loggedIn: false},
	}
	for name, deps := range arms {
		t.Run(name, func(t *testing.T) {
			got := statusJSON(opCtx(), t, deps)
			for _, k := range []string{"account", "account_source", "session", "session_source"} {
				require.Contains(t, got, k, "every status arm must carry %q", k)
			}
			assert.Equal(t, statusAccountGlobal, got["account"])
			assert.Equal(t, "global", got["account_source"])
			assert.Contains(t, textBodyTools(handleServerStatus(opCtx(), deps, "")), "Account: "+statusAccountGlobal+" (global)",
				"the text arm carries the same facts")
		})
	}
}

// TestStatus_ReportsTheGlobalSelectionBesideTheEffectiveAccount is requirement
// 6's fourth clause: the effective account AND its source AND the session
// identity AND the global selection.
//
// THE PAIR IS THE POINT. With only the effective account reported, a caller
// whose header or session binding decided the answer cannot tell a binding that
// TOOK from a selection that happens to name the same account, and cannot see
// what the daemon falls back to when the binding expires. Both arms carry it,
// always, and empty is a state rather than an omission.
func TestStatus_ReportsTheGlobalSelectionBesideTheEffectiveAccount(t *testing.T) {
	const headerAccount = "22222222-2222-4222-8222-222222222222"
	selectAccountForTest(t)
	deps, _ := newAccountDeps(t)
	ctx := auth.WithHeaderAccount(opCtx(), headerAccount)

	got := statusJSON(ctx, t, deps)
	require.Equal(t, headerAccount, got["account"], "the header decided this call")
	assert.Equal(t, statusAccountGlobal, got["account_global"],
		"and the machine-wide selection is reported beside it, not replaced by it")

	body := textBodyTools(handleServerStatus(ctx, deps, ""))
	assert.Contains(t, body, "Account: "+headerAccount+" (header)")
	assert.Contains(t, body, "Global selection: "+statusAccountGlobal,
		"the text arm carries the same pair")

	t.Run("no selection stored is a state, reported as such", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		require.NoError(t, os.WriteFile(path, []byte("[default]\nprovider = \"anthropic\"\n"), 0o600))
		t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))

		got := statusJSON(ctx, t, deps)
		assert.Contains(t, got, "account_global", "the key is always present")
		assert.Empty(t, got["account_global"])
		assert.Contains(t, textBodyTools(handleServerStatus(ctx, deps, "")), "Global selection: none")
	})
}

// TestStatus_ReportsALadderRefusal is the REFUSAL arm of both writers, which
// nothing observed: auth.RequestAccount returns an error on two paths — an
// account the gateway has been watched reject, and a session-carrying call whose
// binding store cannot be read — and both writers have an arm for it.
//
// A refusal is not "no account": it is an account this daemon will not use and
// the reason why. Reporting it as a silent empty would send a user looking for
// a binding that never applied, with nothing naming the cause.
func TestStatus_ReportsALadderRefusal(t *testing.T) {
	t.Run("a gateway-rejected account", func(t *testing.T) {
		selectAccountForTest(t)
		deps, _ := newAccountDeps(t)
		auth.SelectedAccount().MarkInvalid(statusAccountGlobal, "account_forbidden: you are not a member of this account")

		got := statusJSON(opCtx(), t, deps)
		assert.Empty(t, got["account"], "a refused account is not the account this call uses")
		assert.Equal(t, "none", got["account_source"])
		assert.Contains(t, got["account_reason"], "account_forbidden",
			"the gateway's own reason is what the user acts on")
		assert.Contains(t, got["account_reason"], "knowledge accounts")

		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "account_forbidden", "the text arm carries the same refusal")
	})

	t.Run("a session-carrying call with an unreadable binding store", func(t *testing.T) {
		selectAccountForTest(t)
		deps, store := newAccountDeps(t)
		t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(store))
		require.NoError(t, os.WriteFile(store.Path(), []byte("{ not json"), 0o600))
		ctx := harnessCtx(t)

		got := statusJSON(ctx, t, deps)
		assert.Empty(t, got["account"],
			"a session whose bindings cannot be read resolves no account — it does not fall to the global")
		assert.Equal(t, "none", got["account_source"])
		assert.Contains(t, got["account_reason"], "repair or delete it")

		body := textBodyTools(handleServerStatus(ctx, deps, ""))
		assert.Contains(t, body, "repair or delete it", "the text arm carries the same refusal")
		assert.Contains(t, body, "Session bindings are NOT in effect:",
			"and names the store, which is the line the JSON twin's session_binding_store_error is")
	})
}
