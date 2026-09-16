// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// fakeBindings is a SessionAccountResolver over an in-memory map, with an
// optional failure that models an unreadable binding store.
type fakeBindings struct {
	bySession map[string]string
	failWith  error
}

func (f fakeBindings) AccountForSession(sessionID string) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}
	return f.bySession[sessionID], nil
}

// selectionOver builds a selection over a scratch config carrying id (or none
// when id is ""). No test here touches the developer's real config.
func selectionOver(t *testing.T, id string) *AccountSelection {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte("[default]\nprovider = \"anthropic\"\n"), 0o600))
	if id != "" {
		require.NoError(t, config.WriteSelectedAccountID(path, id))
	}
	return NewAccountSelection(path, 0)
}

// withHarnessSession installs a resolved harness session for the test, or an
// unresolved one when id is "".
func withHarnessSession(t *testing.T, id string) {
	t.Helper()
	if id == "" {
		t.Cleanup(SetHarnessSessionResolverForTest(func(context.Context) HarnessSession {
			return HarnessSession{Source: HarnessSourceNone, Reason: HarnessReasonNotResolved}
		}))
		return
	}
	t.Cleanup(SetHarnessSessionResolverForTest(func(context.Context) HarnessSession {
		return HarnessSession{ID: id, Source: HarnessSourceClaudeHook}
	}))
}

// TestRequestAccount_PrecedenceMatrix walks all eight cells of
// {header set, unset} x {session bound, unbound} x {global set, unset} and pins
// both the account and the source each one resolves to.
func TestRequestAccount_PrecedenceMatrix(t *testing.T) {
	const (
		headerAcct  = "acct_01HEADER"
		sessionAcct = "acct_01SESSION"
		globalAcct  = "acct_01GLOBAL"
	)
	cases := []struct {
		name       string
		header     bool
		bound      bool
		global     bool
		wantID     string
		wantSource AccountSource
	}{
		{"header + session + global", true, true, true, headerAcct, AccountSourceHeader},
		{"header + no session + global", true, false, true, headerAcct, AccountSourceHeader},
		{"header + session + no global", true, true, false, headerAcct, AccountSourceHeader},
		{"header + no session + no global", true, false, false, headerAcct, AccountSourceHeader},
		{"no header + session + global", false, true, true, sessionAcct, AccountSourceSession},
		{"no header + session + no global", false, true, false, sessionAcct, AccountSourceSession},
		{"no header + no session + global", false, false, true, globalAcct, AccountSourceGlobal},
		{"no header + no session + no global", false, false, false, "", AccountSourceNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := ""
			if tc.global {
				stored = globalAcct
			}
			sel := selectionOver(t, stored)
			// The session identity resolves in every cell; whether a BINDING
			// exists for it is the axis under test, so an unbound cell still
			// carries a session and proves the ladder fell through rather than
			// never having looked.
			withHarnessSession(t, "session-a")
			bindings := fakeBindings{bySession: map[string]string{}}
			if tc.bound {
				bindings.bySession["session-a"] = sessionAcct
			}
			sel.SetSessionAccountResolver(bindings)

			ctx := context.Background()
			if tc.header {
				ctx = WithHeaderAccount(ctx, headerAcct)
			}
			id, source, err := sel.RequestAccount(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}

// TestRequestAccount_NoSessionLayerIsTheGlobal covers the process that has no
// per-session layer at all — a CLI invocation rather than the daemon. An absent
// rung is not a degraded one: the selection answers, as it always did.
func TestRequestAccount_NoSessionLayerIsTheGlobal(t *testing.T) {
	sel := selectionOver(t, "acct_01GLOBAL")
	withHarnessSession(t, "session-a")

	id, source, err := sel.RequestAccount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acct_01GLOBAL", id)
	assert.Equal(t, AccountSourceGlobal, source)
}

// TestRequestAccount_UnreadableBindingsRefuseOnlySessionCarryingCalls is the
// "no fallback" pair: a call that carries a harness session is REFUSED when the
// bindings cannot be read, and a call that carries none proceeds on the global
// selection exactly as it did before bindings existed.
func TestRequestAccount_UnreadableBindingsRefuseOnlySessionCarryingCalls(t *testing.T) {
	storeErr := errors.New("session binding store unreadable at /x/session_accounts.json: repair or delete it")

	t.Run("with a session — refused", func(t *testing.T) {
		sel := selectionOver(t, "acct_01GLOBAL")
		withHarnessSession(t, "session-a")
		sel.SetSessionAccountResolver(fakeBindings{failWith: storeErr})

		id, source, err := sel.RequestAccount(context.Background())
		require.Error(t, err, "an unreadable binding store must never resolve to the machine-wide account")
		require.ErrorIs(t, err, storeErr)
		assert.Empty(t, id)
		assert.Equal(t, AccountSourceNone, source)
	})

	t.Run("without a session — proceeds on the global selection", func(t *testing.T) {
		sel := selectionOver(t, "acct_01GLOBAL")
		withHarnessSession(t, "")
		sel.SetSessionAccountResolver(fakeBindings{failWith: storeErr})

		id, source, err := sel.RequestAccount(context.Background())
		require.NoError(t, err, "a call with no session never consults the store, so its state cannot refuse the call")
		assert.Equal(t, "acct_01GLOBAL", id)
		assert.Equal(t, AccountSourceGlobal, source)
	})
}

// TestRequestAccount_RejectionIsPerAccount proves a gateway rejection refuses the
// account it was earned by and no other: the header and session rungs are checked
// against the marker, and a DIFFERENT account still resolves.
func TestRequestAccount_RejectionIsPerAccount(t *testing.T) {
	sel := selectionOver(t, "acct_01GLOBAL")
	withHarnessSession(t, "session-a")
	sel.SetSessionAccountResolver(fakeBindings{bySession: map[string]string{"session-a": "acct_01SESSION"}})

	// Known-positive control: before the rejection the session rung answers.
	id, source, err := sel.RequestAccount(context.Background())
	require.NoError(t, err)
	require.Equal(t, "acct_01SESSION", id)
	require.Equal(t, AccountSourceSession, source)

	sel.MarkInvalid("acct_01SESSION", "account_forbidden: you are not a member of this account")

	_, _, err = sel.RequestAccount(context.Background())
	require.Error(t, err, "a rejected session-bound account must refuse, not slide down to the global")
	require.ErrorIs(t, err, ErrAccountSelectionRejected)

	// The SAME selection still resolves a header naming another account, which is
	// what keeps one session's refusal from disabling the daemon.
	id, source, err = sel.RequestAccount(WithHeaderAccount(context.Background(), "acct_01OTHER"))
	require.NoError(t, err)
	assert.Equal(t, "acct_01OTHER", id)
	assert.Equal(t, AccountSourceHeader, source)
}

// TestRejectionFor_IsKeyedByAccount is the unit behind the row above.
func TestRejectionFor_IsKeyedByAccount(t *testing.T) {
	sel := selectionOver(t, "acct_01GLOBAL")
	require.NoError(t, sel.RejectionFor("acct_01ANY"))

	sel.MarkInvalid("acct_01BAD", "account_forbidden")
	require.Error(t, sel.RejectionFor("acct_01BAD"))
	require.NoError(t, sel.RejectionFor("acct_01GOOD"))
	require.NoError(t, sel.RejectionFor(""))
}
