// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// account_session_precedence_test.go drives the account resolution ladder over
// the REAL /mcp endpoint — the JSON-RPC mux an MCP client posts to — so what is
// observed is what a user observes: the storage binding, the interceptor stamp
// and the account that reaches the wire, end to end.
//
// TWO ACCOUNTS, ONE CLOUD URL. The Router is built with a single cloud endpoint
// (mcp_search_stack_test.go), so two accounts cannot be told apart by two engine
// URLs. They are told apart INSIDE one handler, which routes on the inbound
// Knowledge-Account-Id header into one counting engine per account — which makes
// the assertion "the account that actually left the process", not "the account
// the client believed it had picked".

// The three accounts this file routes between. They are UUIDs because the
// inbound header reader canonicalizes through uuid.Parse and refuses anything
// else at the transport, so a fixture that is not one cannot reach the ladder.
const (
	acctGlobal  = "11111111-1111-4111-8111-111111111111"
	acctSession = "33333333-3333-4333-8333-333333333333"
	acctHeader  = "22222222-2222-4222-8222-222222222222"
)

// sessionBindings is a SessionAccountResolver over a fixed map.
type sessionBindings struct {
	mu        sync.Mutex
	bySession map[string]string
}

func (b *sessionBindings) AccountForSession(sessionID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bySession[sessionID], nil
}

// bindSessionForTest installs a resolved harness session and the binding store it
// resolves against, on the process selection the stack uses.
func bindSessionForTest(t *testing.T, sessionID, account string) {
	t.Helper()
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		if sessionID == "" {
			return auth.HarnessSession{Source: auth.HarnessSourceNone, Reason: auth.HarnessReasonNotResolved}
		}
		return auth.HarnessSession{ID: sessionID, Source: auth.HarnessSourceClaudeHook}
	}))
	bindings := &sessionBindings{bySession: map[string]string{}}
	if sessionID != "" && account != "" {
		bindings.bySession[sessionID] = account
	}
	t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(bindings))
}

// accountRoutedStack builds the signed-in MCP stack against the two-counter cloud.
func accountRoutedStack(t *testing.T, cloudURL string) *mcpSearchStack {
	t.Helper()
	localURL, local, stopLocal := startHealthyCountingEngine(t)
	localClient := closeIdleOnCleanup(t, NewGraphClientForURL(localURL))
	r := NewRouterWithMachineAuth(localClient, cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	return newMCPSearchStack(t, r, localClient, local, nil, stopLocal)
}

// TestAccountPrecedence_OverTheMCPEndpoint walks the session and global rungs of
// the ladder over the real endpoint and asserts WHICH account's engine served the
// call. The header rung is pinned separately below.
func TestAccountPrecedence_OverTheMCPEndpoint(t *testing.T) {
	t.Run("a bound session outranks the machine-wide selection", func(t *testing.T) {
		cloudURL, routed := startAccountRoutedEngine(t)
		installSelection(t, acctGlobal)
		bindSessionForTest(t, "session-a", acctSession)
		s := accountRoutedStack(t, cloudURL)

		text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"})
		require.False(t, isError, "the bound session's read must succeed: %s", text)

		assert.Positive(t, routed.executesFor(acctSession), "the SESSION's account must serve the call")
		assert.Zero(t, routed.executesFor(acctGlobal), "the machine-wide account must not serve it")
		assert.Contains(t, text, acctSession, "the bound destination names the session's account")
	})

	t.Run("a bound session works with no machine-wide selection at all", func(t *testing.T) {
		cloudURL, routed := startAccountRoutedEngine(t)
		installSelection(t, "")
		bindSessionForTest(t, "session-a", acctSession)
		s := accountRoutedStack(t, cloudURL)

		text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"})
		require.False(t, isError, "a session binding must not need a machine-wide selection: %s", text)
		assert.Positive(t, routed.executesFor(acctSession))
	})

	t.Run("no session — the machine-wide selection serves, as before", func(t *testing.T) {
		cloudURL, routed := startAccountRoutedEngine(t)
		installSelection(t, acctGlobal)
		bindSessionForTest(t, "", "")
		s := accountRoutedStack(t, cloudURL)

		text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"})
		require.False(t, isError, "%s", text)
		assert.Positive(t, routed.executesFor(acctGlobal), "the regression row: an unbound call still uses the selection")
		assert.Zero(t, routed.executesFor(acctSession))
	})

	t.Run("nothing selected, nothing bound, no header — the refusal is unchanged", func(t *testing.T) {
		cloudURL, routed := startAccountRoutedEngine(t)
		installSelection(t, "")
		bindSessionForTest(t, "session-a", "")
		s := accountRoutedStack(t, cloudURL)

		text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"})
		require.True(t, isError, "a cloud call with no account anywhere must be refused, not sent unstamped")
		assert.Contains(t, text, "select a cloud account before using cloud storage")
		assert.Contains(t, text, "knowledge account use")
		assert.Zero(t, int32(routed.executesFor("")), "the refusal happens before any RPC")
	})
}

// TestAccountPrecedence_HeaderOutranksABoundSession is the PENDING PIN of this
// lane, CONVERTED. It asserted that the /mcp endpoint did not read an inbound
// account header yet — "when the per-request header reader lands, THIS is the
// assertion that flips" — and the reader has landed, so it is flipped: over the
// real endpoint, a header outranks a bound session AND the machine-wide
// selection, which is the top of the ladder observed end to end.
func TestAccountPrecedence_HeaderOutranksABoundSession(t *testing.T) {
	cloudURL, routed := startAccountRoutedEngine(t)
	installSelection(t, acctGlobal)
	bindSessionForTest(t, "session-a", acctSession)
	s := accountRoutedStack(t, cloudURL)
	s.accountHeader = []string{acctHeader}

	text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"})
	require.False(t, isError, "the header-bound read must succeed: %s", text)

	assert.Positive(t, routed.executesFor(acctHeader), "the HEADER's account must serve the call")
	assert.Zero(t, routed.executesFor(acctSession), "the session binding must not win over a header")
	assert.Zero(t, routed.executesFor(acctGlobal), "nor the machine-wide selection")
}

// TestAccountRejection_IsScopedToTheAccountThatEarnedIt is contract 6: a gateway
// rejection earned by a session-bound account must not disable the machine-wide
// selection, or one session's foreign 403 would take every other session's cloud
// calls down with it.
func TestAccountRejection_IsScopedToTheAccountThatEarnedIt(t *testing.T) {
	var refusedHits, allowedHits int32
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get(auth.AccountHeaderName) == acctSession {
			refusedHits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"account_forbidden","error_description":"you are not a member of this account"}`))
			return
		}
		allowedHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	installSelection(t, acctGlobal)
	sel := auth.SelectedAccount()
	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))

	// The session-bound call earns the 403.
	_, err := gc.Execute(WithDestination(opCtx(), Destination{Storage: "cloud", AccountID: acctSession}), graphNamesQuery())
	require.Error(t, err, "the gateway refused this account")
	require.Error(t, sel.RejectionFor(acctSession), "the refused account is latched")

	// The machine-wide selection is untouched, so another session still reaches
	// the server. Without the fix this reads as a local permission_denied and
	// allowedHits stays at zero.
	require.NoError(t, sel.RejectionFor(acctGlobal), "a foreign 403 must not latch the machine-wide selection")
	_, err = gc.Execute(WithDestination(opCtx(), Destination{Storage: "cloud", AccountID: acctGlobal}), graphNamesQuery())
	mu.Lock()
	defer mu.Unlock()
	assert.Positive(t, allowedHits, "a call on the unaffected account must still reach the server (err=%v)", err)
	assert.Positive(t, refusedHits)
}

// TestAccountInterceptor_BoundDestinationCarryingNoAccountFallsThrough is the
// regression for a defect this change introduced and the bootstrap suite caught:
// the first cut REFUSED any bound cloud destination whose AccountID was empty.
//
// That state is not "the bound account changed" — it is "nothing resolved an
// account", which is the ordinary state of a logged-in client with no selection
// and of the background reconcile, and which the surfaces that REQUIRE an
// account refuse for themselves with an actionable message. Refusing it here
// turned those calls into permission_denied with a message about an account
// switch that never happened.
func TestAccountInterceptor_BoundDestinationCarryingNoAccountFallsThrough(t *testing.T) {
	installSelection(t, "")

	capt := newAccountCapture(t)
	srv := httptest.NewServer(capt)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))

	ctx := WithDestination(opCtx(), Destination{Storage: "cloud"})
	if _, err := gc.Execute(ctx, graphNamesQuery()); err != nil {
		t.Fatalf("a bound destination carrying no account must reach the server: %v", err)
	}
	if seen, present := capt.observed(); present {
		t.Errorf("no account resolved, so no header may be sent; got %q", seen)
	}

	// KNOWN POSITIVE on the same instrument: a destination that DOES carry an
	// account stamps it, so the absence above is about the empty account rather
	// than about a capture that never sees a header.
	ctx = WithDestination(opCtx(), Destination{Storage: "cloud", AccountID: acctSession})
	if _, err := gc.Execute(ctx, graphNamesQuery()); err != nil {
		t.Fatalf("a bound destination carrying an account must reach the server: %v", err)
	}
	if seen, present := capt.observed(); !present || seen != acctSession {
		t.Errorf("header = %q present=%v, want %q", seen, present, acctSession)
	}
}

// TestAccountInterceptor_UnboundCallStampsTheLadderNotTheSelection is the row
// behind the fall-through reading the ladder.
//
// A cloud RPC that carries NO bound destination still belongs to whichever
// session issued it, so the account it stamps must come from the same ladder the
// raw sync surface reads. Resolving the machine-wide selection here instead is
// invisible today — every user MCP call binds first — and becomes a split the
// moment a second destination producer lands, which is exactly the class of
// defect one ladder exists to foreclose.
func TestAccountInterceptor_UnboundCallStampsTheLadderNotTheSelection(t *testing.T) {
	installSelection(t, acctGlobal)
	bindSessionForTest(t, "session-a", acctSession)

	capt := newAccountCapture(t)
	srv := httptest.NewServer(capt)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))

	// No WithDestination on this context: the unbound arm is the one under test.
	if _, err := gc.Execute(opCtx(), graphNamesQuery()); err != nil {
		t.Fatalf("an unbound cloud call must reach the server: %v", err)
	}
	seen, present := capt.observed()
	if !present || seen != acctSession {
		t.Errorf("header = %q present=%v, want the SESSION's account %q — an unbound call must read the ladder",
			seen, present, acctSession)
	}
}

// unreadableBindings is a SessionAccountResolver whose store cannot be read.
type unreadableBindings struct{}

func (unreadableBindings) AccountForSession(string) (string, error) {
	return "", fmt.Errorf("%w at /x/session_accounts.json: repair or delete it", auth.ErrSessionBindingsUnreadable)
}

// TestAccountInterceptor_ClassifiesTheFaultItReports separates the two ways the
// account resolution can fail, because they send the reader to different places.
//
// A REJECTED ACCOUNT is the gateway's decision, re-reported locally: permission
// denied, and the user looks at their membership. A BINDING STORE THIS DAEMON
// CANNOT READ is this process failing to read its own state: internal, and the
// user looks at the file the message names. Reporting the second as the first
// is what this row exists to stop.
func TestAccountInterceptor_ClassifiesTheFaultItReports(t *testing.T) {
	capt := newAccountCapture(t)
	srv := httptest.NewServer(capt)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	// The cloud client captures the process selection AT CONSTRUCTION
	// (newCloudGraphClient), so each row builds its own after installing the
	// selection it means to drive — a client built first would hold the suite's
	// neutralized one and observe nothing.
	newClient := func(t *testing.T) *GraphClient {
		t.Helper()
		return closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	}

	t.Run("an unreadable binding store is an INTERNAL fault", func(t *testing.T) {
		installSelection(t, acctGlobal)
		t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
			return auth.HarnessSession{ID: "session-a", Source: auth.HarnessSourceClaudeHook}
		}))
		t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(unreadableBindings{}))

		_, err := newClient(t).Execute(opCtx(), graphNamesQuery())
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
			"a store this daemon cannot read is its own fault, not the caller's permissions")
		assert.ErrorContains(t, err, "repair or delete it")
	})

	t.Run("a rejected account is PERMISSION DENIED", func(t *testing.T) {
		installSelection(t, acctGlobal)
		bindSessionForTest(t, "", "")
		auth.SelectedAccount().MarkInvalid(acctGlobal, "account_forbidden: you are not a member of this account")

		_, err := newClient(t).Execute(opCtx(), graphNamesQuery())
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
			"the gateway refused this account, and that is a permission decision")
	})
}
