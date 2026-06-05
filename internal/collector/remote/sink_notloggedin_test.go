// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// emptyAuthStore is an auth.Store that never holds a refresh token, so an
// AuthState built over it always reports IsLoggedIn=false (the free path).
type emptyAuthStore struct{}

func (emptyAuthStore) Get(context.Context, string) (string, error) { return "", auth.ErrNotFound }
func (emptyAuthStore) Set(context.Context, string, string) error   { return nil }
func (emptyAuthStore) Delete(context.Context, string) error        { return nil }

// staticTok is a non-refreshing token source (never exercised on the free path).
type staticTok struct{}

func (staticTok) Token(context.Context) (string, auth.PermissionSet, error) { return "", nil, nil }

// startIngestServer stands up an h2c ingest server and returns its URL plus the
// finalize counter, so a real *GraphClient (NewGraphClientForURL) can be pointed
// at it via the Router.
func startIngestServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	eng := &countingIngest{}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewIngestServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv.URL, &eng.finalize
}

// TestUploadSink_NotLoggedIn_RoutesLocal is the free-path regression:
// with a REAL Router whose AuthState reports NOT logged in, the collect
// UploadSink picker (Router.IngestClient → pick(ctx)) selects the LOCAL server.
// The cloud URL is unreachable, so a mis-route to cloud would fail the
// WriteResult rather than pass silently.
func TestUploadSink_NotLoggedIn_RoutesLocal(t *testing.T) {
	localURL, localFinalize := startIngestServer(t)

	as := auth.NewAuthState(emptyAuthStore{}, time.Hour) // empty store → not logged in
	localGC := graphclient.NewGraphClientForURL(localURL)
	r := graphclient.NewRouter(localGC, "http://cloud.invalid:0", staticTok{}, as)

	sink := NewUploadSinkFunc(r.IngestClient)

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: "free-path-repo",
		Nodes:     []*knowledgev1.Node{{Id: "n1", Type: "func", Summary: "n1"}},
	}

	require.NoError(t, sink.WriteResult(context.Background(), "", result),
		"not-logged-in WriteResult must succeed against the local server (free path)")
	assert.Equal(t, int32(1), localFinalize.Load(),
		"not-logged-in collect must route to the local backend (the picker selects local when IsLoggedIn=false)")
}
