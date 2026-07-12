// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newByteTransport builds a Transport wired at a caller-provided
// httptest.Server. httptest.Server responses are instant, so the default
// 5-minute per-request timeout is fine.
func newByteTransport(t *testing.T, srv *httptest.Server, src TokenSource) *Transport {
	t.Helper()
	return NewSyncTransport(srv.URL, src)
}

// TestTransport_PushGraph_PostsBytes exercises the happy path of
// [Transport.PushGraph]: bytes arrive at the server unchanged, the URL
// path carries graphType+name, and the Authorization header is set.
func TestTransport_PushGraph_PostsBytes(t *testing.T) {
	want := []byte{0x01, 0x02, 0x03, 0x04, 0xFF}
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := newByteTransport(t, srv, src)

	if err := tr.PushGraph(context.Background(), "knowledge", "default", want); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q want POST", gotMethod)
	}
	if gotPath != "/v1/sync/push/knowledge/default" {
		t.Errorf("path: got %q want /v1/sync/push/knowledge/default", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header: got %q want %q", gotAuth, "Bearer tok")
	}
	if !bytes.Equal(gotBody, want) {
		t.Errorf("body mismatch: got % x want % x", gotBody, want)
	}
}

// TestTransport_PushGraph_RejectsEmptyArgs guards against caller mistakes
// the transport must catch before touching the network.
func TestTransport_PushGraph_RejectsEmptyArgs(t *testing.T) {
	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := NewSyncTransport("http://unused", src)

	cases := []struct{ gt, name string }{
		{"", "default"},
		{"knowledge", ""},
	}
	for _, c := range cases {
		if err := tr.PushGraph(context.Background(), c.gt, c.name, []byte{1}); err == nil {
			t.Errorf("PushGraph(%q,%q): expected error, got nil", c.gt, c.name)
		}
	}
}

// TestTransport_PushGraph_SurfacesNon2xx verifies that a non-success
// response is turned into a *SyncHTTPError rather than swallowed.
func TestTransport_PushGraph_SurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"scope missing"}`))
	}))
	t.Cleanup(srv.Close)

	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := newByteTransport(t, srv, src)

	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{1})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *SyncHTTPError
	if !errors.As(err, &se) || se.StatusCode != http.StatusForbidden {
		t.Errorf("expected SyncHTTPError 403, got %v", err)
	}
}

// TestTransport_SegmentControlJSON_RoutesToSegmentsPrefix proves SegmentControlJSON
// POSTs to /v1/segments/<path> (NOT /v1/sync/...), sets the Bearer header, and
// returns the 2xx JSON body verbatim.
func TestTransport_SegmentControlJSON_RoutesToSegmentsPrefix(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte
	respBody := []byte(`{"chunks":[{"upload_url":"https://gcs/x","object_path":"segments/a/b/c/h.seg"}]}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(srv.Close)

	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := newByteTransport(t, srv, src)

	reqBody := []byte(`{"graph_type":"knowledge","name":"default","format":"hnsw","chunks":[{"content_hash":"h"}]}`)
	out, err := tr.SegmentControlJSON(context.Background(), "presign", reqBody)
	if err != nil {
		t.Fatalf("SegmentControlJSON: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q want POST", gotMethod)
	}
	if gotPath != "/v1/segments/presign" {
		t.Errorf("path: got %q want /v1/segments/presign", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header: got %q want %q", gotAuth, "Bearer tok")
	}
	if !bytes.Equal(gotBody, reqBody) {
		t.Errorf("request body mismatch: got %q want %q", gotBody, reqBody)
	}
	if !bytes.Equal(out, respBody) {
		t.Errorf("response body not returned verbatim: got %q want %q", out, respBody)
	}
}

// TestTransport_SegmentControlJSON_401Refreshes proves the /v1/segments/ channel
// shares the same 401 → force-refresh → retry core as PushGraph: the first call
// 401s, one ForceRefresh happens, and the retry (with the rotated token) reaches
// the segments route and returns the body.
func TestTransport_SegmentControlJSON_401Refreshes(t *testing.T) {
	var first atomic.Bool
	first.Store(true)
	var seenAuth []string
	var seenPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		seenPath = r.URL.Path
		if first.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"expired"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"found":false}`))
	}))
	t.Cleanup(srv.Close)

	src := &refreshingStub{current: "tok-stale"}
	tr := newByteTransport(t, srv, src)

	out, err := tr.SegmentControlJSON(context.Background(), "manifest/read", []byte(`{}`))
	if err != nil {
		t.Fatalf("SegmentControlJSON: %v", err)
	}
	if src.refreshCnt != 1 {
		t.Errorf("expected 1 refresh call, got %d", src.refreshCnt)
	}
	if len(seenAuth) < 2 || seenAuth[1] != "Bearer tok-refreshed" {
		t.Errorf("expected retry with refreshed token, seenAuth=%v", seenAuth)
	}
	if seenPath != "/v1/segments/manifest/read" {
		t.Errorf("path: got %q want /v1/segments/manifest/read", seenPath)
	}
	if string(out) != `{"found":false}` {
		t.Errorf("body: got %q", out)
	}
}

// refreshingStub is a RefreshingTokenSource used to prove that 401
// triggers ForceRefresh and the retry reaches the server with a new token.
type refreshingStub struct {
	mu         sync.Mutex
	current    string
	refreshCnt int
}

func (r *refreshingStub) Token(_ context.Context) (string, PermissionSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current, PermissionSet{PermMCPKnowledgeWrite: {}}, nil
}

func (r *refreshingStub) ForceRefresh(_ context.Context) (string, PermissionSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshCnt++
	r.current = "tok-refreshed"
	return r.current, PermissionSet{PermMCPKnowledgeWrite: {}}, nil
}

// TestTransport_401TriggersRefresh proves the sendWithAuthBytes path
// implements 401 → force-refresh → retry. Uses PushGraph as the smallest
// vehicle.
func TestTransport_401TriggersRefresh(t *testing.T) {
	var first atomic.Bool
	first.Store(true)
	var seenAuth []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if first.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"expired"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	src := &refreshingStub{current: "tok-stale"}
	tr := newByteTransport(t, srv, src)

	if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{1, 2, 3}); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}
	if src.refreshCnt != 1 {
		t.Errorf("expected 1 refresh call, got %d", src.refreshCnt)
	}
	if len(seenAuth) < 2 {
		t.Fatalf("expected 2 calls (first 401, retry 200), got %d", len(seenAuth))
	}
	if seenAuth[1] != "Bearer tok-refreshed" {
		t.Errorf("retry auth header wrong: %q", seenAuth[1])
	}
}

// TestTransport_401NoRefresherSurfaces verifies that a 401 from the
// server is passed through unchanged when the TokenSource does not
// implement RefreshingTokenSource — callers with a StaticTokenSource
// must see the auth error so they can prompt a re-login.
func TestTransport_401NoRefresherSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := newByteTransport(t, srv, src)
	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{1})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *SyncHTTPError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected SyncHTTPError 401, got %v", err)
	}
}

// TestOAuthTokenSource_ForceRefresh proves the RefreshingTokenSource
// implementation on OAuthTokenSource discards its cache and calls the
// refresh endpoint even when the in-memory access token is unexpired.
// This is the guarantee Transport relies on when handling 401s.
func TestOAuthTokenSource_ForceRefresh(t *testing.T) {
	newAccess := signTestJWT(t, []string{PermMCPKnowledgeWrite}, time.Now().Add(time.Hour).Unix())
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(TokenResponse{ //nolint:gosec // test fixture — OAuth response with signed JWT from signTestJWT
			AccessToken:  newAccess,
			RefreshToken: "frt_new",
			ExpiresIn:    3600,
		})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	store := newTestStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "frt_old"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := newOAuthSourceForTest(store, srv.URL)
	// Pre-populate cache with an unexpired token; ForceRefresh must
	// still contact the server.
	src.accessToken = "stale"
	src.expiresAt = time.Now().Add(time.Hour)

	tok, _, err := src.ForceRefresh(context.Background())
	if err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if tok != newAccess {
		t.Error("ForceRefresh did not rotate access token")
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 network call, got %d", calls.Load())
	}
}

// TestTransport_ErrorBodyTruncation verifies readHTTPError truncates
// pathologically large error bodies at maxErrorBodyBytes. Regression
// guard: the full-graph shape makes it easier for an upstream proxy
// to stream megabytes of HTML into an error response; agent logs must
// not bloat.
func TestTransport_ErrorBodyTruncation(t *testing.T) {
	large := strings.Repeat("x", maxErrorBodyBytes*2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(large))
	}))
	t.Cleanup(srv.Close)

	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := newByteTransport(t, srv, src)
	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{1})
	var se *SyncHTTPError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SyncHTTPError, got %v", err)
	}
	if len(se.Body) > maxErrorBodyBytes {
		t.Errorf("Body not truncated: got %d bytes, cap %d",
			len(se.Body), maxErrorBodyBytes)
	}
}
