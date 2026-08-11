// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// headerCapture records the harness header off the RAW inbound request before
// delegating to the real Engine handler. The claim under test is what LEAVES the
// process, so the assertion reads the wire rather than the connect request the
// interceptor mutated.
type headerCapture struct {
	next http.Handler

	mu   sync.Mutex
	seen string
}

func (c *headerCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.seen = r.Header.Get(harnessSessionHeader)
	c.mu.Unlock()
	c.next.ServeHTTP(w, r)
}

func (c *headerCapture) observed() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// newHarnessCapture builds the real Engine handler behind a capturing wrapper.
// Going through the production constructors (rather than calling the
// interceptor directly) is the point: a direct call would pass even if nobody
// had installed the interceptor.
func newHarnessCapture(t *testing.T) *headerCapture {
	t.Helper()
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(&stubEngine{
		respond: func(*knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return enginetest.ResponseWithNodes(), nil
		},
	})
	mux.Handle(path, hdlr)
	return &headerCapture{next: mux}
}

// TestSessionInterceptor_StampsHeaderFromContext proves the harness session-id
// on the dispatch context reaches the wire as a header from BOTH production
// constructors, is absent when the transcript has not resolved, and cannot be
// moved by message content.
func TestSessionInterceptor_StampsHeaderFromContext(t *testing.T) {
	t.Run("local_h2c_client", func(t *testing.T) {
		// The local transport speaks HTTP/2 with prior knowledge, so the test
		// server needs the h2c wrapper.
		capt := newHarnessCapture(t)
		srv := httptest.NewServer(h2c.NewHandler(capt, &http2.Server{}))
		t.Cleanup(srv.Close)

		gc := NewGraphClientForURL(srv.URL)
		ctx := session.ContextWithHarnessSessionID(opCtx(), "harness-1208-abc")
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "harness-1208-abc", capt.observed(),
			"the local client must carry the ctx harness session-id on the wire")
	})

	t.Run("cloud_client", func(t *testing.T) {
		// The cloud transport is a plain http.Transport, so no h2c wrapper.
		capt := newHarnessCapture(t)
		srv := httptest.NewServer(capt)
		t.Cleanup(srv.Close)

		gc := NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok-1208"})
		ctx := session.ContextWithHarnessSessionID(opCtx(), "harness-1208-cloud")
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "harness-1208-cloud", capt.observed(),
			"the cloud client must carry the ctx harness session-id on the wire")
	})

	t.Run("unresolved_harness_sets_no_header", func(t *testing.T) {
		// The unresolved-transcript state: no harness id on ctx. The negative
		// control that keeps the positive legs honest — an interceptor stamping
		// unconditionally would look identical without it.
		capt := newHarnessCapture(t)
		srv := httptest.NewServer(h2c.NewHandler(capt, &http2.Server{}))
		t.Cleanup(srv.Close)

		gc := NewGraphClientForURL(srv.URL)
		_, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err, "an unresolved harness must not break the RPC")
		assert.Empty(t, capt.observed(), "no harness id in context means no header at all")
	})

	t.Run("message_fields_cannot_influence_header", func(t *testing.T) {
		capt := newHarnessCapture(t)
		srv := httptest.NewServer(h2c.NewHandler(capt, &http2.Server{}))
		t.Cleanup(srv.Close)

		gc := NewGraphClientForURL(srv.URL)
		ctx := session.ContextWithHarnessSessionID(opCtx(), "harness-1208-real")
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			// A decoy harness-looking value in the message body: the identity is
			// daemon-resolved, so nothing the agent can put in a message may move it.
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "harness-1208-decoy"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "harness-1208-real", capt.observed())
		assert.NotEqual(t, "harness-1208-decoy", capt.observed(),
			"message content must not be able to set the harness header")
	})
}
