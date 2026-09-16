// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestConstructClient_SessionBindingsFollowGraphStorage is requirement 4's
// directory row: the bindings file lands under the daemon's OWN --graph-storage
// data root, so a daemon started with a non-default root keeps its bindings
// there and never in a HOME-fixed path.
func TestConstructClient_SessionBindingsFollowGraphStorage(t *testing.T) {
	dialer := fakeDialer(t)
	fakeAuthStoreForTest(t)
	root := filepath.Join(t.TempDir(), "a-non-default-graph-storage")

	c, err := constructClient(Config{LocalDialer: dialer, GraphStorage: root})
	require.NoError(t, err)
	require.NotNil(t, c.SessionAccountBindings())

	assert.Equal(t, filepath.Join(root, "session_accounts.json"), c.SessionAccountBindings().Path(),
		"the bindings must live under the daemon's own data root")
	assert.False(t, strings.Contains(c.SessionAccountBindings().Path(), ".knowledge/session_accounts.json") &&
		!strings.HasPrefix(c.SessionAccountBindings().Path(), root),
		"the path must not be resolved from the home directory")
}

// TestConstructClient_InstallsTheBindingsAsTheSessionRung proves the store the
// manage arm WRITES through is the one the read ladder CONSULTS. Two stores over
// two files would let a binding be recorded and never take effect, with nothing
// on either side able to notice.
func TestConstructClient_InstallsTheBindingsAsTheSessionRung(t *testing.T) {
	dialer := fakeDialer(t)
	fakeAuthStoreForTest(t)
	// Keep the process selection isolated: this test installs a resolver on it.
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))

	c, err := constructClient(Config{LocalDialer: dialer, GraphStorage: t.TempDir()})
	require.NoError(t, err)

	// Known-positive control: with no binding, the ladder resolves nothing.
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		return auth.HarnessSession{ID: "harness-123", Source: auth.HarnessSourceClaudeHook}
	}))
	id, source, err := auth.RequestAccount(context.Background())
	require.NoError(t, err)
	require.Empty(t, id)
	require.Equal(t, auth.AccountSourceNone, source)

	// Write through the store the manage arm would use…
	_, bindErr := c.SessionAccountBindings().Bind("harness-123", "acct_01SESSION")
	require.NoError(t, bindErr)

	// …and the ladder resolves it, with no further wiring.
	id, source, err = auth.RequestAccount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acct_01SESSION", id, "the constructor must install the bindings as the session rung")
	assert.Equal(t, auth.AccountSourceSession, source)
}

// fakeDialer hands constructClient a throwaway local client pointed at an unused
// URL. No RPC is issued by either test above, so it never dials.
func fakeDialer(t *testing.T) func(int) *graphclient.GraphClient {
	t.Helper()
	return func(int) *graphclient.GraphClient {
		gc := graphclient.NewGraphClientForURL("http://local.invalid")
		t.Cleanup(gc.Close)
		return gc
	}
}

// fakeAuthStoreForTest points constructClient at an in-memory credential store.
// The real one is off-limits to a test binary, and constructClient resolves its
// auth sources before anything else — so without this the constructor refuses
// before it ever reaches the wiring under test.
func fakeAuthStoreForTest(t *testing.T) {
	t.Helper()
	store := newFakeAuthStore()
	prior := newAuthStoreFn
	newAuthStoreFn = func() (auth.Store, error) { return store, nil }
	t.Cleanup(func() { newAuthStoreFn = prior })

	// A not-logged-in constructClient starts the :15022 drop-detection keepalive,
	// whose goroutine outlives the test and trips the suite's leak gate. The
	// wiring under test is unrelated to it, so it is stubbed the same way the
	// keepalive gate test stubs it.
	priorKeepalive := startKeepaliveFn
	startKeepaliveFn = func(*graphclient.GraphClient, context.Context) {}
	t.Cleanup(func() { startKeepaliveFn = priorKeepalive })
}

// TestConstructClient_ReleasesTheSessionRungOnShutdown is the finding's row.
//
// The install is PROCESS-WIDE — the account ladder belongs to the process, not
// to a client — so a client that took it and never gave it back leaves the
// ladder pointed at a store it owns. A test binary builds clients routinely;
// after one is torn down and its data root deleted, every session-carrying read
// in that process answers from a store over a directory nobody can see.
func TestConstructClient_ReleasesTheSessionRungOnShutdown(t *testing.T) {
	fakeAuthStoreForTest(t)
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		return auth.HarnessSession{ID: "harness-123", Source: auth.HarnessSourceClaudeHook}
	}))

	root := t.TempDir()
	c, err := constructClient(Config{LocalDialer: fakeDialer(t), GraphStorage: root})
	require.NoError(t, err)
	_, bindErr := c.SessionAccountBindings().Bind("harness-123", "acct_01FIRST")
	require.NoError(t, bindErr)

	// Known-positive control: while the client holds the rung, it answers.
	id, source, err := auth.RequestAccount(context.Background())
	require.NoError(t, err)
	require.Equal(t, "acct_01FIRST", id)
	require.Equal(t, auth.AccountSourceSession, source)

	// The client shuts down.
	c.drainOnShutdown()

	// The ladder must no longer consult that store: this session's calls stop
	// resolving to the account the torn-down client had bound.
	id, source, err = auth.RequestAccount(context.Background())
	require.NoError(t, err)
	assert.Empty(t, id, "a torn-down client must not leave the process ladder pointed at its own store")
	assert.Equal(t, auth.AccountSourceNone, source)

	// …and the file going bad afterwards is not this process's problem either.
	// Still installed, THIS is a refusal on every session-carrying call in the
	// binary — over a directory the harness is about to delete.
	require.NoError(t, os.WriteFile(filepath.Join(root, "session_accounts.json"), []byte("{ not json"), 0o600))
	_, _, err = auth.RequestAccount(context.Background())
	require.NoError(t, err, "a released store's corruption must not refuse anybody's calls")
}

// TestConstructClient_ASupersededReleaseIsANoOp pins the compare-and-swap in the
// restore closure: installs do not unwind in the order they were made — a
// process can build a second client before the first shuts down — and a closure
// that restored its own prior unconditionally would undo the LATER install.
func TestConstructClient_ASupersededReleaseIsANoOp(t *testing.T) {
	fakeAuthStoreForTest(t)
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		return auth.HarnessSession{ID: "harness-123", Source: auth.HarnessSourceClaudeHook}
	}))

	first, err := constructClient(Config{LocalDialer: fakeDialer(t), GraphStorage: t.TempDir()})
	require.NoError(t, err)
	second, err := constructClient(Config{LocalDialer: fakeDialer(t), GraphStorage: t.TempDir()})
	require.NoError(t, err)
	_, bindErr := second.SessionAccountBindings().Bind("harness-123", "acct_01SECOND")
	require.NoError(t, bindErr)

	// The FIRST client shuts down second, out of install order. Its release is
	// superseded and must do nothing.
	first.drainOnShutdown()

	id, source, err := auth.RequestAccount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acct_01SECOND", id, "a superseded release must not undo the current install")
	assert.Equal(t, auth.AccountSourceSession, source)
}
