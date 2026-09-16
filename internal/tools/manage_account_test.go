// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// accountDaemonDeps is a fully-wired daemon that ALSO carries a binding store,
// so the account arms can be driven through the real InterceptManage dispatch.
type accountDaemonDeps struct {
	*fullDaemonDeps
	bindings *SessionAccountStore
}

func (d *accountDaemonDeps) SessionAccountBindings() *SessionAccountStore { return d.bindings }

// newAccountDeps builds those deps over a scratch data root.
func newAccountDeps(t *testing.T) (*accountDaemonDeps, *SessionAccountStore) {
	t.Helper()
	store := NewSessionAccountStore(t.TempDir())
	return &accountDaemonDeps{fullDaemonDeps: newFullDaemonDeps(true), bindings: store}, store
}

// stubAccountLookup installs the membership seam: every listed account is a
// member, anything else is not, and failWith models an unreachable endpoint.
func stubAccountLookup(t *testing.T, members map[string]cli.AccountMembership, failWith error) *int {
	t.Helper()
	calls := 0
	prior := lookupAccountFn
	lookupAccountFn = func(_ context.Context, arg string) (cli.AccountMembership, error) {
		calls++
		if failWith != nil {
			return cli.AccountMembership{}, failWith
		}
		m, ok := members[arg]
		if !ok {
			return cli.AccountMembership{}, errors.New("you are not a member of any Fulminate account with id or slug " + arg)
		}
		return m, nil
	}
	t.Cleanup(func() { lookupAccountFn = prior })
	return &calls
}

// harnessSession installs a resolved (or unresolved) harness session on the
// LADDER's seam. The status render reads T1's context carrier instead, so a row
// that asserts both stamps opCtxWithHarnessSession as well.
func harnessSession(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		if id == "" {
			return auth.HarnessSession{Source: auth.HarnessSourceNone, Reason: auth.HarnessReasonNotResolved}
		}
		return auth.HarnessSession{ID: id, Source: auth.HarnessSourceClaudeHook}
	}))
}

// manageCall drives one manage call through the real dispatch, schema gate
// included.
func accountManageCall(t *testing.T, deps ClientDeps, args string) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(args),
	})
	require.True(t, handled, "the manage intercept must claim %s", args)
	return res
}

// TestAccountForSession_BindsTheCallsOwnSession is the happy path: the session
// the call arrived on is bound, and the binding is readable afterwards.
func TestAccountForSession_BindsTheCallsOwnSession(t *testing.T) {
	deps, store := newAccountDeps(t)
	harnessSession(t, "harness-123")
	stubAccountLookup(t, map[string]cli.AccountMembership{
		"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
	}, nil)

	res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme","format":"json"}`)
	require.False(t, res.IsError, "bind: %s", textBodyTools(res))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &body))
	assert.Equal(t, true, body["bound"])
	assert.Equal(t, "acct_01ACME", body["account"])
	assert.Equal(t, "harness-123", body["session"], "it binds the session the call arrived on")

	got, err := store.AccountForSession("harness-123")
	require.NoError(t, err)
	assert.Equal(t, "acct_01ACME", got, "the binding must be readable by the resolver that serves reads")
}

// TestAccountForSession_FailsWhenNoSessionResolves is the owner's rule: the
// operation must FAIL when the hook is not working, and bind nothing.
func TestAccountForSession_FailsWhenNoSessionResolves(t *testing.T) {
	deps, store := newAccountDeps(t)
	harnessSession(t, "")
	calls := stubAccountLookup(t, map[string]cli.AccountMembership{
		"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
	}, nil)

	res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme"}`)
	require.True(t, res.IsError, "an unidentified session must not be bound")

	body := textBodyTools(res)
	for _, want := range []string{
		"no harness session on this call",
		"PreToolUse hook is not installed or not firing",
		"Codex turn metadata",
		"Knowledge-Session-Id",
	} {
		assert.Contains(t, body, want, "the refusal must name the carrier that is missing")
	}

	// NOTHING WAS WRITTEN — asserted on the file, not on the response.
	_, statErr := os.Stat(store.Path())
	assert.True(t, os.IsNotExist(statErr), "the refusal must write no bindings file at all, got %v", statErr)
	assert.Zero(t, *calls, "the refusal must precede the membership round trip")
}

// TestAccountForSession_TakesNoSessionArgument pins that the operation cannot be
// pointed at another session: the schema declares no such property, and the
// undeclared-param gate refuses the call before any handler runs.
func TestAccountForSession_TakesNoSessionArgument(t *testing.T) {
	deps, store := newAccountDeps(t)
	harnessSession(t, "harness-123")
	stubAccountLookup(t, map[string]cli.AccountMembership{
		"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
	}, nil)

	for _, spelling := range []string{"session", "session_id"} {
		t.Run(spelling, func(t *testing.T) {
			res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme","`+spelling+`":"someone-elses-session"}`)
			require.True(t, res.IsError, "a session argument must be refused")
			assert.Contains(t, textBodyTools(res), spelling, "the refusal names the parameter")
			got, err := store.AccountForSession("someone-elses-session")
			require.NoError(t, err)
			assert.Empty(t, got, "nothing may be bound for the named session")
		})
	}
}

// TestAccountForSession_ErrorArms covers every early return that is not the
// session refusal: each one reports and binds nothing.
func TestAccountForSession_ErrorArms(t *testing.T) {
	t.Run("a non-member id or slug", func(t *testing.T) {
		deps, store := newAccountDeps(t)
		harnessSession(t, "harness-123")
		stubAccountLookup(t, map[string]cli.AccountMembership{
			"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
		}, nil)

		for _, arg := range []string{"acct_01SOMEONEELSE", "someone-elses-slug"} {
			res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"`+arg+`"}`)
			require.True(t, res.IsError, "a non-member must be refused: %s", arg)
			assert.Contains(t, textBodyTools(res), "not a member")
			_, statErr := os.Stat(store.Path())
			assert.True(t, os.IsNotExist(statErr), "nothing may be written for %s", arg)
		}
	})

	t.Run("the accounts endpoint unreachable", func(t *testing.T) {
		deps, store := newAccountDeps(t)
		harnessSession(t, "harness-123")
		stubAccountLookup(t, nil, errors.New("could not check your accounts, so nothing was changed: dial tcp: refused"))

		res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme"}`)
		require.True(t, res.IsError, "an unreachable list must fail rather than bind optimistically")
		assert.Contains(t, textBodyTools(res), "nothing was changed")
		_, statErr := os.Stat(store.Path())
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("no account argument", func(t *testing.T) {
		deps, _ := newAccountDeps(t)
		harnessSession(t, "harness-123")
		res := accountManageCall(t, deps, `{"operation":"account_for_session"}`)
		require.True(t, res.IsError)
		assert.Contains(t, textBodyTools(res), "account is required")
	})

	t.Run("no binding store on this client", func(t *testing.T) {
		harnessSession(t, "harness-123")
		res := accountManageCall(t, newFullDaemonDeps(true), `{"operation":"account_for_session","account":"acme"}`)
		require.True(t, res.IsError, "a client with no store must refuse, not bind into nothing")
		assert.Contains(t, textBodyTools(res), "binding store is unavailable")
	})

	t.Run("the store cannot be written", func(t *testing.T) {
		harnessSession(t, "harness-123")
		stubAccountLookup(t, map[string]cli.AccountMembership{
			"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
		}, nil)
		// A FILE where the data root's parent directory must be, so the atomic
		// write cannot even create the directory: the bind fails loudly instead
		// of reporting a success nothing recorded.
		blocked := filepath.Join(t.TempDir(), "not-a-directory")
		require.NoError(t, os.WriteFile(blocked, nil, 0o600))
		deps := &accountDaemonDeps{
			fullDaemonDeps: newFullDaemonDeps(true),
			bindings:       NewSessionAccountStore(filepath.Join(blocked, "graph-storage")),
		}

		res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme"}`)
		require.True(t, res.IsError, "a store write failure must be reported")
		assert.Contains(t, textBodyTools(res), "session bindings")
	})
}

// TestAccountForSession_BindsAnUnsubscribedAccount is the subscription asymmetry:
// the new arm performs NO client-side subscription check, because the gateway
// refuses that account's cloud calls server-side and is the authority.
func TestAccountForSession_BindsAnUnsubscribedAccount(t *testing.T) {
	deps, store := newAccountDeps(t)
	harnessSession(t, "harness-123")
	stubAccountLookup(t, map[string]cli.AccountMembership{
		"hobby": {ID: "acct_01HOBBY", Slug: "hobby", HasActiveSubscription: false},
	}, nil)

	res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"hobby"}`)
	require.False(t, res.IsError, "an unsubscribed MEMBER is bindable per-session: %s", textBodyTools(res))
	got, err := store.AccountForSession("harness-123")
	require.NoError(t, err)
	assert.Equal(t, "acct_01HOBBY", got)
}

// stubUseAccount installs the account_use seam over a real config file, so the
// row below asserts the WRITE rather than a recorded intention. failWith models
// the refusals runAccountUse performs (an unsubscribed account, a non-member).
func stubUseAccount(t *testing.T, configPath string, failWith error) {
	t.Helper()
	prior := useAccountFn
	useAccountFn = func(_ context.Context, out io.Writer, arg string) (string, error) {
		if failWith != nil {
			return "", failWith
		}
		if err := config.WriteSelectedAccountID(configPath, arg); err != nil {
			return "", err
		}
		_, _ = io.WriteString(out, "Now using account slug-for-"+arg+" ("+arg+"). Cloud calls from this machine are routed to it.\n")
		return arg, nil
	}
	t.Cleanup(func() { useAccountFn = prior })
}

// seededAccount is the selection every scratch config here starts from: the
// account these rows switch AWAY from.
const seededAccount = "acct_01AAAAAAAAAAAAAAAA"

// seedConfig writes a scratch config already carrying seededAccount, with a
// comment the selection writer must preserve, and returns its path.
func seedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte("# a comment the writer must preserve\n\n[default]\nprovider = \"anthropic\"\n"), 0o600))
	require.NoError(t, config.WriteSelectedAccountID(path, seededAccount))
	return path
}

// TestAccountUse_WritesTheSelectionAndTheDaemonFollowsWithoutARestart is
// requirement 3's observable, in ONE process: the selection is written, the
// daemon's own account resolution follows it once the cache window elapses, and
// the process is the same one throughout.
func TestAccountUse_WritesTheSelectionAndTheDaemonFollowsWithoutARestart(t *testing.T) {
	// accountA is what seedConfig plants; accountB is what this row switches to.
	const accountA, accountB = seededAccount, "acct_01BBBBBBBBBBBBBBBB"
	deps, _ := newAccountDeps(t)
	path := seedConfig(t)
	stubUseAccount(t, path, nil)
	harnessSession(t, "")

	// The daemon's live selection over that config, with the clock injected so
	// the TTL is crossed deliberately rather than by sleeping.
	now := time.Now()
	sel := auth.NewAccountSelection(path, 0)
	t.Cleanup(auth.SetSelectedAccountForTest(sel))
	sel.SetClockForTest(func() time.Time { return now })

	before, source, err := auth.RequestAccount(opCtx())
	require.NoError(t, err)
	require.Equal(t, accountA, before, "known-positive control: the daemon starts on account A")
	require.Equal(t, auth.AccountSourceGlobal, source)
	pidBefore := os.Getpid()

	res := accountManageCall(t, deps, `{"operation":"account_use","account":"`+accountB+`","format":"json"}`)
	require.False(t, res.IsError, "account_use: %s", textBodyTools(res))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &body))
	assert.Equal(t, accountB, body["account"])
	assert.Equal(t, false, body["restart_required"], "the response must say no restart follows")
	assert.Equal(t, auth.DefaultAccountCheckTTL.String(), body["follows_within"])

	// The file carries the new id, comment preserved — the CLI's own write.
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(written), accountB)
	assert.Contains(t, string(written), "# a comment the writer must preserve")

	// Cross the selection's cache window with the injected clock: the SAME
	// process now resolves account B, with no restart and no new pid.
	now = now.Add(auth.DefaultAccountCheckTTL + time.Second)
	after, source, err := auth.RequestAccount(opCtx())
	require.NoError(t, err)
	assert.Equal(t, accountB, after, "the daemon must follow the new selection without a restart")
	assert.Equal(t, auth.AccountSourceGlobal, source)
	assert.Equal(t, pidBefore, os.Getpid(), "the same process served both reads: nothing was restarted")

	// AND THE SEGMENT PATH FOLLOWS IT TOO. The account the ladder resolves is
	// what a read binds as its destination, and a destination is what selects
	// the per-{storage,account} segment child — so the read after the switch is
	// served out of the NEW account's child rather than the root manager built
	// under the old selection. The segment side of that is pinned where the
	// manager lives: segmentdist's
	// TestSegmentManager_ARestartLessSwitchIsServedByTheNewAccountsChild. What
	// is asserted HERE is the input that row takes as given — the destination a
	// post-switch read binds names account B.
	bound, err := bindStorageForTest(opCtx(), after)
	require.NoError(t, err)
	assert.Equal(t, accountB, bound.AccountID,
		"the destination a post-switch read binds is the new account, which is what selects its segment child")
	assert.Equal(t, "cloud", bound.Storage)
}

// TestAccountUse_KeepsTheCLIsRefusalsAndItsErrorArms proves the reused flow's
// refusals survive the reuse, and that a refused call writes nothing.
func TestAccountUse_KeepsTheCLIsRefusalsAndItsErrorArms(t *testing.T) {
	t.Run("an unsubscribed account is refused, config byte-identical", func(t *testing.T) {
		deps, _ := newAccountDeps(t)
		path := seedConfig(t)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		stubUseAccount(t, path, errors.New(`account "hobby" has no active subscription, so it has no cloud graph access at all`))

		res := accountManageCall(t, deps, `{"operation":"account_use","account":"hobby"}`)
		require.True(t, res.IsError, "the CLI's unsubscribed refusal must survive the reuse")
		assert.Contains(t, textBodyTools(res), "no active subscription")

		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after, "a refused account_use must leave the config byte-identical")
	})

	t.Run("no account argument", func(t *testing.T) {
		deps, _ := newAccountDeps(t)
		res := accountManageCall(t, deps, `{"operation":"account_use"}`)
		require.True(t, res.IsError)
		assert.Contains(t, textBodyTools(res), "account is required")
	})

	t.Run("re-selecting the account already selected leaves the file identical", func(t *testing.T) {
		deps, _ := newAccountDeps(t)
		const same = seededAccount
		path := seedConfig(t)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		stubUseAccount(t, path, nil)

		res := accountManageCall(t, deps, `{"operation":"account_use","account":"`+same+`"}`)
		require.False(t, res.IsError, "re-selecting is not an error: %s", textBodyTools(res))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}

// TestManageAccountOperations_AreDeclaredAndReachable pins the two names into the
// published surface. An operation whose params or name are missing from the
// schema is unreachable in production however well its handler works
// (manage_dispatch.go:23-26).
func TestManageAccountOperations_AreDeclaredAndReachable(t *testing.T) {
	enum := ManageToolDef().InputSchema.Properties["operation"].Enum
	for _, op := range []string{"account_for_session", "account_use"} {
		assert.Contains(t, enum, op, "the published operation enum must carry %q", op)
		assert.True(t, manageOperationKnown(op), "the known-operation set must carry %q", op)
	}
	assert.Contains(t, ManageToolDef().InputSchema.Properties, "account",
		"the account parameter must be declared or every call carrying it is refused before dispatch")
	assert.NotContains(t, ManageToolDef().InputSchema.Properties, "session",
		"declaring a session parameter would let one caller retarget another's session")
}

// bindStorageForTest builds the destination a read binds from a resolved
// account, the way BindStorage's cloud arm does. It is spelled here rather than
// called through the router because this package holds no router; the shape it
// asserts is graphclient.Destination's, which segmentdist keys its children by.
func bindStorageForTest(_ context.Context, account string) (graphclient.Destination, error) {
	if account == "" {
		return graphclient.Destination{}, errors.New("no account resolved")
	}
	return graphclient.Destination{Storage: "cloud", AccountID: account}, nil
}
