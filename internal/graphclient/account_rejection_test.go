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
	t.Cleanup(srv.Close)

	gc := NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-reject"})
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
