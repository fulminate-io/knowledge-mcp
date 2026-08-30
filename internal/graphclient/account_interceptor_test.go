// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// accountCapture records the account header off the RAW inbound request, and
// whether it was present at all, before delegating to the real Engine handler.
// The claim under test is what LEAVES the process.
type accountCapture struct {
	next http.Handler

	mu      sync.Mutex
	seen    string
	present bool
}

func (c *accountCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	_, c.present = r.Header[http.CanonicalHeaderKey(auth.AccountHeaderName)]
	c.seen = r.Header.Get(auth.AccountHeaderName)
	c.mu.Unlock()
	c.next.ServeHTTP(w, r)
}

func (c *accountCapture) observed() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen, c.present
}

// newAccountCapture builds the real Engine handler behind a capturing wrapper.
// Going through the production constructors (rather than calling the
// interceptor directly) is the point: a direct call would pass even if nobody
// had installed the interceptor.
func newAccountCapture(t *testing.T) *accountCapture {
	t.Helper()
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(&stubEngine{
		respond: func(*knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return enginetest.ResponseWithNodes(), nil
		},
	})
	mux.Handle(path, hdlr)
	return &accountCapture{next: mux}
}

// installSelection points the process-wide account selection at a temp config
// carrying id (or none when id is ""), restoring the prior one on cleanup.
func installSelection(t *testing.T, id string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := "[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	if id != "" {
		require.NoError(t, config.WriteSelectedAccountID(path, id))
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, time.Second)))
}

// TestAccountHeader_ConnectCloudClient proves the selected account reaches the
// wire from the PRODUCTION cloud constructor.
func TestAccountHeader_ConnectCloudClient(t *testing.T) {
	const id = "acct_01CONNECTCONNECTCONNEC"

	installSelection(t, id)

	capt := newAccountCapture(t)
	srv := httptest.NewServer(capt)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-acct"}))
	_, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	})
	require.NoError(t, err)

	seen, present := capt.observed()
	assert.True(t, present, "the cloud client must carry the account header")
	assert.Equal(t, id, seen, "the cloud client must carry the selected account id on the wire")
}

// TestAccountHeader_AbsentWhenUnsetAndOnLocalClient holds the two negative
// controls: no selection means no header at all on the cloud client, and the
// local h2c client never stamps even when a selection IS set (the local server
// is single-tenant, and a refusal there would turn a cloud-side rejection into
// a total local outage).
func TestAccountHeader_AbsentWhenUnsetAndOnLocalClient(t *testing.T) {
	t.Run("cloud_client_no_selection", func(t *testing.T) {
		installSelection(t, "")

		capt := newAccountCapture(t)
		srv := httptest.NewServer(capt)
		t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

		gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-none"}))
		_, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err)

		seen, present := capt.observed()
		assert.False(t, present, "no selection must mean no header at all, got %q", seen)
	})

	t.Run("local_client_never_stamps", func(t *testing.T) {
		const id = "acct_01LOCALLOCALLOCALLOCAL"
		installSelection(t, id)

		capt := newAccountCapture(t)
		srv := httptest.NewServer(h2c.NewHandler(capt, &http2.Server{}))
		t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

		gc := closeIdleOnCleanup(t, NewGraphClientForURL(srv.URL))
		_, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err)

		seen, present := capt.observed()
		assert.False(t, present, "the local h2c client must never stamp the account header, got %q", seen)
	})
}

// TestAccountInterceptor_RefusesRejectedSelection proves the refusal half of the
// interceptor: once a rejection has been observed for the stored selection, the
// cloud RPC fails locally as permission-denied and the server is never reached.
func TestAccountInterceptor_RefusesRejectedSelection(t *testing.T) {
	const id = "acct_01REFUSECONNECTREFUSEC"
	installSelection(t, id)

	var hits int
	var mu sync.Mutex
	capt := newAccountCapture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		capt.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-refuse"}))
	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	}

	// Known-positive control: the same call reaches the server before the
	// rejection is recorded.
	_, err := gc.Execute(opCtx(), req)
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 1, hits)
	mu.Unlock()

	auth.SelectedAccount().MarkInvalid(id, "account_forbidden: you are not a member of this account")

	_, err = gc.Execute(opCtx(), req)
	require.Error(t, err, "a rejected selection must fail locally")
	require.ErrorIs(t, err, auth.ErrAccountSelectionRejected)
	mu.Lock()
	assert.Equal(t, 1, hits, "the refused call must never reach the server")
	mu.Unlock()
}
