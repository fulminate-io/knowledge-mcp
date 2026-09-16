// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncTransport_StampsTheRequestAccountNotTheSelection is the SYNC arm of the
// precedence requirement. The /v1/sync surface is the second stamping chokepoint,
// and stamping the machine-wide selection here while the Connect chokepoint
// stamped a session-bound account would split one session's data across two
// accounts with nothing on either side able to detect it.
//
// The observable is the header ON THE WIRE, read off the inbound request.
func TestSyncTransport_StampsTheRequestAccountNotTheSelection(t *testing.T) {
	const (
		globalAcct  = "acct_01GLOBALGLOBALGLOBAL"
		sessionAcct = "acct_01SESSIONSESSIONSES"
		headerAcct  = "acct_01HEADERHEADERHEAD"
	)

	var seen string
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header[http.CanonicalHeaderKey(AccountHeaderName)]
		seen = r.Header.Get(AccountHeaderName)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), globalAcct), time.Second)
	sel.SetSessionAccountResolver(fakeBindings{bySession: map[string]string{"session-a": sessionAcct}})
	tr := NewSyncTransport(srv.URL, StaticTokenSource{
		AccessToken: "tok",
		Permissions: PermissionSet{PermMCPKnowledgeWrite: {}},
	}, WithAccountSelection(sel))

	t.Run("no session — the machine-wide selection, as before", func(t *testing.T) {
		withHarnessSession(t, "")
		require.NoError(t, tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}))
		require.True(t, present)
		assert.Equal(t, globalAcct, seen)
	})

	t.Run("a bound session — the session's account", func(t *testing.T) {
		withHarnessSession(t, "session-a")
		require.NoError(t, tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}))
		require.True(t, present)
		assert.Equal(t, sessionAcct, seen, "the sync surface must follow the session binding, not the selection")
	})

	t.Run("a header outranks both", func(t *testing.T) {
		withHarnessSession(t, "session-a")
		ctx := WithHeaderAccount(context.Background(), headerAcct)
		require.NoError(t, tr.PushGraph(ctx, "knowledge", "default", []byte{0x01}))
		require.True(t, present)
		assert.Equal(t, headerAcct, seen)
	})
}

// TestSyncTransport_RejectionLatchIsScopedToTheStampedAccount is contract 6 on
// the RAW SYNC surface — the half the first cut fixed on the Connect chokepoint
// and left undone here.
//
// A 403 earned by a SESSION-BOUND account must latch that account and no other.
// The rejection marker self-clears only when the STORED selection moves, so a
// latch landing on the machine-wide account disables every other session's cloud
// calls, and the daemon's own background work, until someone runs
// `knowledge account use` — on the strength of one foreign refusal.
func TestSyncTransport_RejectionLatchIsScopedToTheStampedAccount(t *testing.T) {
	const (
		globalAcct  = "acct_01GLOBALGLOBALGLOBAL"
		sessionAcct = "acct_01SESSIONSESSIONSES"
	)

	var stamped string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stamped = r.Header.Get(AccountHeaderName)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account_forbidden","error_description":"you are not a member of the selected Fulminate account"}`))
	}))
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), globalAcct), time.Second)
	sel.SetSessionAccountResolver(fakeBindings{bySession: map[string]string{"session-a": sessionAcct}})
	tr := NewSyncTransport(srv.URL, StaticTokenSource{
		AccessToken: "tok",
		Permissions: PermissionSet{PermMCPKnowledgeWrite: {}},
	}, WithAccountSelection(sel))
	withHarnessSession(t, "session-a")

	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	require.Error(t, err, "the gateway refused this account")

	// The request really did carry the SESSION's account: without this the
	// assertions below would be about a request that named the global one, and a
	// latch on the global would be correct rather than a defect.
	require.Equal(t, sessionAcct, stamped, "the refused request must be the session-bound one")

	require.Error(t, sel.RejectionFor(sessionAcct), "the refused account must be latched")
	require.NoError(t, sel.RejectionFor(globalAcct),
		"a session-bound account's 403 must not disable the machine-wide account")

	// The observable behind that assertion: a later call on a DIFFERENT session,
	// with no binding, still resolves and stamps rather than refusing locally.
	withHarnessSession(t, "")
	id, source, resolveErr := sel.RequestAccount(context.Background())
	require.NoError(t, resolveErr, "an unaffected caller must still resolve an account")
	assert.Equal(t, globalAcct, id)
	assert.Equal(t, AccountSourceGlobal, source)
}
