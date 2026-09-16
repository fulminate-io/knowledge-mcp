// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestAccountRejection_LatchesTheStampedAccount is the ticket's Direction, at
// the level the poisoning happened: a gateway 403 for the account THIS request
// stamped marks THAT account, and leaves the process selection working.
//
// Before this, the classifier marked the selection whatever account the request
// carried, so one refused header-bound call disabled every cloud call in the
// daemon until the user re-selected an account or restarted it.
func TestAccountRejection_LatchesTheStampedAccount(t *testing.T) {
	const selected = "11111111-1111-4111-8111-111111111111"
	const bound = "22222222-2222-4222-8222-222222222222"
	installSelection(t, selected)

	var mu sync.Mutex
	stamped := []string{}
	member := newAccountCapture(t) // the REAL Engine handler, for the accepted arm
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(auth.AccountHeaderName)
		mu.Lock()
		stamped = append(stamped, id)
		mu.Unlock()
		if id != bound {
			// Any other account is a member: the call is SERVED, so this
			// fixture can tell a refusal from an outage.
			member.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account_forbidden","error_description":"caller is not a member of the requested account"}`))
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	}
	gcBound := closeIdleOnCleanup(t, newCloudGraphClient(srv.URL,
		auth.StaticTokenSource{AccessToken: "tok-bound"}, &Destination{Storage: "cloud", AccountID: bound}))
	_, err := gcBound.Execute(opCtx(), req)
	require.Error(t, err, "the gateway's refusal for the bound account must surface")

	// THE DECISIVE ASSERTION: the process selection is untouched.
	id, selErr := auth.SelectedAccount().IDForRequest(opCtx())
	require.NoError(t, selErr, "a refusal for another account must not poison the selection")
	assert.Equal(t, selected, id)

	// And a call for the selection still reaches the gateway, stamped with the
	// selection — not refused locally, and never retried as somebody else.
	gcSelected := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-sel"}))
	_, err = gcSelected.Execute(opCtx(), req)
	require.NoError(t, err, "the selected account must still be served after a foreign refusal")

	mu.Lock()
	assert.Equal(t, []string{bound, selected}, stamped,
		"each call must carry exactly the account it was for, with no unstamped retry")
	mu.Unlock()

	// The refused account IS latched, on its own: a second call bound to it is
	// refused locally rather than re-attempted.
	_, err = gcBound.Execute(opCtx(), req)
	require.ErrorIs(t, err, auth.ErrAccountSelectionRejected)
	mu.Lock()
	assert.Len(t, stamped, 2, "the refused account's second call must never reach the gateway")
	mu.Unlock()
}

// TestAccountRejection_ConnectChainClassifiesAndLatches drives a 403
// account_forbidden through the PRODUCTION cloud constructor and proves the
// two halves of the contract: the membership remedy is surfaced to the user,
// and the second call is refused locally with the server's request count still
// at one.
//
// The remedy surfaces on the SECOND call rather than the first, and that is
// structural rather than a shortcut: connect-go parses a non-200 body into a
// wire error declaring only code/message/details, so the gateway's
// error/error_description body unmarshals with an EMPTY message and the first
// call's connect error cannot carry it. The round-tripper classifies the raw
// response, latches the selection, and the request-side interceptor then
// refuses with the classified reason.
func TestAccountRejection_ConnectChainClassifiesAndLatches(t *testing.T) {
	const id = "acct_01CONNECTREJECTCONNECT"
	installSelection(t, id)

	var mu sync.Mutex
	var hits int
	var headers []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		headers = append(headers, r.Header.Get(auth.AccountHeaderName))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account_forbidden","error_description":"caller is not a member of the requested account"}`))
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-reject"}))
	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	}

	_, err := gc.Execute(opCtx(), req)
	require.Error(t, err, "the gateway rejection must surface as an error")
	mu.Lock()
	require.Equal(t, 1, hits, "the first call must reach the gateway")
	mu.Unlock()

	// Second call: refused locally, carrying the membership remedy.
	_, err = gc.Execute(opCtx(), req)
	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrAccountSelectionRejected)
	assert.Contains(t, err.Error(), "not a member of the selected Fulminate account",
		"the refusal must surface the membership remedy")
	assert.Contains(t, err.Error(), "knowledge account use")

	mu.Lock()
	assert.Equal(t, 1, hits, "the refused call must never reach the server")
	for i, h := range headers {
		assert.Equal(t, id, h, "request %d must carry exactly the selected account — no fallback", i)
	}
	mu.Unlock()
}
