// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// Two accounts, spelled as the gateway spells them. accountA is what the
// daemon's config selects; accountB is the account ONE request asks for.
const (
	accountA = "11111111-1111-4111-8111-111111111111"
	accountB = "2b2b2b2b-2b2b-4b2b-8b2b-2b2b2b2b2b2b"
)

// TestAccountHeader_BindsTheRequestDestination is requirement 1: a /mcp request
// carrying the account header is ANSWERED for that account — the destination it
// binds, the account it stamps on the wire, and the reference it hands back all
// name the header's account, while the very next unbound call on the SAME
// session is still answered for the config selection.
func TestAccountHeader_BindsTheRequestDestination(t *testing.T) {
	s := newAccountRoutedMCPSearchStack(t)

	text, isErr := s.as(accountB).callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "the header-bound call must succeed: %s", text)
	assert.Contains(t, text, "destination={Storage:cloud AccountID:"+accountB+"}",
		"the request must bind the header's account as its destination")
	assert.Contains(t, text, "account="+accountB)
	assert.Contains(t, text, "account_source="+AccountSourceHeader)
	assert.Contains(t, text, `"account":"`+accountB+`"`,
		"the reference the call returns must name the account it was answered for")

	assert.Equal(t, 1, s.byAccount.executesFor(accountB), "the cloud engine must have executed for the header's account")
	assert.Equal(t, 0, s.byAccount.executesFor(accountA), "nothing may execute for the selected account")
	assert.Equal(t, 0, s.byAccount.executesFor(""), "no request may reach the gateway unstamped")

	// The binding is PER REQUEST: the next call on the same session, with no
	// header, is answered for the config selection again.
	text, isErr = s.callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "the unbound call must succeed: %s", text)
	assert.Contains(t, text, "destination={Storage:cloud AccountID:"+accountA+"}")
	assert.Contains(t, text, "account_source="+AccountSourceGlobal)
	assert.Equal(t, 1, s.byAccount.executesFor(accountA))
}

// TestAccountHeader_BindsTheSearchDestinations is requirement 1's search leg: a
// header-bound search binds its CLOUD search destination to the header's
// account, not to the selection, so a federated read cannot read one account
// and report it as another's.
func TestAccountHeader_BindsTheSearchDestinations(t *testing.T) {
	s := newAccountRoutedMCPSearchStack(t)

	text, isErr := s.as(accountB).callText(t, "search", map[string]any{"query": "alpha"})
	require.False(t, isErr, "the header-bound search must succeed: %s", text)
	assert.Contains(t, text, "{Storage:cloud AccountID:"+accountB+"}",
		"the bound search destination list must carry the header's account: %s", text)
	assert.NotContains(t, text, "AccountID:"+accountA,
		"the selected account must not appear in a header-bound search: %s", text)
}

// TestAccountHeader_NonMemberIsRefusedWithNoFallback is requirement 2 over the
// real endpoint: the gateway's 403 for a header-named account surfaces as the
// tool error, nothing is read or written for another account, and the daemon
// goes on serving the account it selected — the rejection is a fact about the
// account the request stamped, not about this daemon.
func TestAccountHeader_NonMemberIsRefusedWithNoFallback(t *testing.T) {
	s := newAccountRoutedMCPSearchStack(t)
	s.byAccount.forbid(accountB)

	text, isErr := s.as(accountB).callText(t, "query", map[string]any{"id": "x"})
	assert.True(t, isErr, "a non-member account must surface the gateway's refusal: %s", text)
	assert.Equal(t, 0, s.byAccount.executesFor(accountA),
		"nothing may execute for the selected account when the header's account is refused")
	assert.Equal(t, 0, s.byAccount.executesFor(""), "no request may fall back to an unstamped call")
	for i, id := range s.byAccount.stampedAccounts() {
		assert.Equal(t, accountB, id, "request %d must carry exactly the header's account — no fallback", i)
	}

	// THE DECISIVE ROW: an ordinary call for the selected account still reaches
	// the engine after the foreign refusal.
	text, isErr = s.callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "the selected account must still be served after a foreign 403: %s", text)
	assert.Equal(t, 1, s.byAccount.executesFor(accountA))
}

// TestAccountHeader_MalformedIsRefusedNamingTheHeader is requirement 2's
// malformed arm. Every class is refused at the transport with the header named,
// nothing is dispatched, and the process selection is untouched — proven by the
// known-positive call that follows and is answered normally.
func TestAccountHeader_MalformedIsRefusedNamingTheHeader(t *testing.T) {
	s := newAccountRoutedMCPSearchStack(t)

	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "empty value", values: []string{""}},
		{name: "whitespace", values: []string{"   "}},
		{name: "a slug", values: []string{"my-account"}},
		{name: "one digit short", values: []string{accountB[:len(accountB)-1]}},
		{name: "36 characters that are not a uuid", values: []string{strings.Repeat("z", 36)}},
		{name: "sent twice", values: []string{accountA, accountB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, status := s.as(tc.values...).postStatus(t, map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "tools/call",
				"params": map[string]any{"name": "query", "arguments": map[string]any{"id": "x"}},
			}, s.session)
			assert.Equal(t, 400, status, "a malformed account header is a client error, got body %s", body)
			assert.Contains(t, body, auth.AccountHeaderName, "the refusal must name the header: %s", body)
		})
	}

	// Nothing was dispatched for any of the refused calls, on any account.
	assert.Empty(t, s.byAccount.stampedAccounts(), "a refused header must reach no backend at all")

	// Known positive: the daemon still answers, so the refusals above are the
	// header's doing and not a broken fixture.
	text, isErr := s.callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "the daemon must still answer after a refused header: %s", text)
	assert.Contains(t, text, "AccountID:"+accountA)
}

// TestAccountHeader_MalformedIsRefusedBeforeTheSession drives the handler
// directly, which is the only way to present a header value net/http's own
// client refuses to transmit (an embedded newline — the client error
// "invalid header field value" is itself that class's refusal on the wire).
//
// It also pins WHERE the refusal happens: this POST carries no MCP session at
// all, and a refusal that came after the session lookup would be the 404 that
// lookup returns. 400 means the header was refused first, so a malformed
// account can reach neither a session nor a dispatch.
func TestAccountHeader_MalformedIsRefusedBeforeTheSession(t *testing.T) {
	h := newCORSTestServer()

	for _, value := range []string{accountB + "\n", "urn:uuid:" + accountB + "\r"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.AccountHeaderName, value)
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "value %q must be refused, got body %s", value, rec.Body)
		assert.Contains(t, rec.Body.String(), auth.AccountHeaderName, "the refusal must name the header")
	}

	// Known positive through the same handler: a well-formed header gets PAST
	// the account check and is refused by the session lookup instead, so the
	// 400s above are the header's doing and not a handler that refuses
	// everything.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.AccountHeaderName, accountB)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusBadRequest, rec.Code,
		"a well-formed account header must not be refused as malformed: %s", rec.Body)
}

// TestAccountHeader_LoggedOutStatusDoesNotClaimTheAccount is the status half of
// requirement 5: a logged-out daemon serves the request LOCALLY, so reporting
// the header's account as this request's account would be a wrong-cause claim —
// a web page would render a cloud account's name over local data. The request
// reports no account, the source `none`, and a REASON naming what happened to
// the header, the way the harness session reports an unresolved one.
func TestAccountHeader_LoggedOutStatusDoesNotClaimTheAccount(t *testing.T) {
	s := newLoggedOutMCPSearchStack(t).as(accountB)

	text, isErr := s.callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "a logged-out local call must succeed: %s", text)
	assert.Contains(t, text, "account_source="+AccountSourceNone,
		"a logged-out daemon resolves no account for a header-carrying request: %s", text)
	assert.NotContains(t, text, "account="+accountB,
		"the header's account must not be reported as the account this request was answered for: %s", text)
	assert.Contains(t, text, "account_reason=header ignored: logged out",
		"the reason must name what happened to the header: %s", text)

	// The cloud arm is unchanged: it still refuses, naming the header.
	text, isErr = s.callText(t, "query", map[string]any{"id": "x", "storage": "cloud"})
	assert.True(t, isErr)
	assert.Contains(t, text, auth.AccountHeaderName)
}

// TestAccountHeader_CanonicalSpelling is the second half of the malformed row:
// an UPPERCASE header and a lowercase one name the SAME account, and the daemon
// resolves both to one canonical id — the id keys a client map and a segment
// cache directory, and two spellings must never become two of either.
func TestAccountHeader_CanonicalSpelling(t *testing.T) {
	s := newAccountRoutedMCPSearchStack(t)

	for _, spelling := range []string{accountB, strings.ToUpper(accountB)} {
		text, isErr := s.as(spelling).callText(t, "query", map[string]any{"id": "x"})
		require.False(t, isErr, "spelling %q must be accepted: %s", spelling, text)
		assert.Contains(t, text, "destination={Storage:cloud AccountID:"+accountB+"}",
			"spelling %q must resolve to the canonical account id", spelling)
	}
	assert.Equal(t, 2, s.byAccount.executesFor(accountB),
		"both spellings must be stamped as the one canonical account")
}

// TestAccountHeader_LoggedOutIgnoresItForCloud is requirement 5. A logged-out
// daemon is served LOCALLY with the header present — the header cannot conjure a
// cloud session — and an explicit cloud call is refused with an error that NAMES
// the header, so a browser page is told why its account did not take.
func TestAccountHeader_LoggedOutIgnoresItForCloud(t *testing.T) {
	s := newLoggedOutMCPSearchStack(t).as(accountB)

	text, isErr := s.callText(t, "query", map[string]any{"id": "x"})
	require.False(t, isErr, "a logged-out local call must succeed: %s", text)
	assert.Contains(t, text, "destination={Storage:local AccountID:}",
		"a logged-out daemon serves locally whatever the header says")

	text, isErr = s.callText(t, "query", map[string]any{"id": "x", "storage": "cloud"})
	assert.True(t, isErr, "a logged-out cloud call must fail")
	assert.Contains(t, text, "cloud storage requires signing in")
	assert.Contains(t, text, auth.AccountHeaderName,
		"the logged-out refusal must name the header the caller sent: %s", text)

	// Without the header the refusal is the one it has always been, and names
	// no header the caller did not send.
	text, isErr = newLoggedOutMCPSearchStack(t).callText(t, "query", map[string]any{"id": "x", "storage": "cloud"})
	assert.True(t, isErr)
	assert.Contains(t, text, "cloud storage requires signing in")
	assert.NotContains(t, text, auth.AccountHeaderName)
}
