// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// viewerFixtureKey is a fixture credential. Assertions below report whether
// the stub saw it, never the value.
const viewerFixtureKey = "linear-viewer-fixture-key"

func viewerServer(t *testing.T, hits *atomic.Int32, rawAuth *atomic.Bool, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		rawAuth.Store(r.Header.Get("Authorization") == viewerFixtureKey)
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckViewer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantReason string
		wantSentry error
	}{
		{name: "IdentifiedViewer", status: http.StatusOK, body: `{"data":{"viewer":{"id":"viewer-fixture"}}}`},
		{name: "Unauthorized", status: http.StatusUnauthorized, body: `{"errors":[{"message":"authentication failed"}]}`, wantErr: true, wantReason: backends.ReasonAuth},
		{name: "Forbidden", status: http.StatusForbidden, body: `{}`, wantErr: true, wantReason: backends.ReasonAuth},
		{name: "RateLimited", status: http.StatusTooManyRequests, body: `slow down`, wantErr: true, wantReason: backends.ReasonRateLimited},
		{name: "ServerError", status: http.StatusInternalServerError, body: `boom`, wantErr: true, wantReason: backends.ReasonHTTP5xx},
		{name: "NoViewerNamed", status: http.StatusOK, body: `{"data":{"viewer":{}}}`, wantErr: true, wantReason: backends.ReasonGraphQL, wantSentry: ErrViewerUnidentified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var rawAuth atomic.Bool
			srv := viewerServer(t, &hits, &rawAuth, tc.status, tc.body)
			client := &Client{APIKey: viewerFixtureKey, Endpoint: srv.URL, HTTP: srv.Client()}
			err := client.CheckViewer(t.Context())
			if tc.wantErr == (err == nil) {
				t.Fatalf("CheckViewer() error = %v, want an error: %v", err, tc.wantErr)
			}
			if hits.Load() != 1 {
				t.Errorf("the stub saw %d requests; want exactly 1 — the check must make the call", hits.Load())
			}
			if !rawAuth.Load() {
				t.Errorf("Authorization did not carry the key in its raw form; a Linear personal key takes no Bearer prefix")
			}
			if !tc.wantErr {
				return
			}
			var backendErr *backends.Error
			if !errors.As(err, &backendErr) {
				t.Fatalf("CheckViewer() error = %v, want a classified *backends.Error", err)
			}
			if backendErr.Reason != tc.wantReason {
				t.Errorf("CheckViewer() reason = %q, want %q", backendErr.Reason, tc.wantReason)
			}
			if tc.wantSentry != nil && !errors.Is(err, tc.wantSentry) {
				t.Errorf("CheckViewer() error = %v, want it to wrap %v", err, tc.wantSentry)
			}
		})
	}
}

func TestCheckViewerAsksTheViewerQuery(t *testing.T) {
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		query.Store(string(body[:n]))
		if _, err := w.Write([]byte(`{"data":{"viewer":{"id":"viewer-fixture"}}}`)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	client := &Client{APIKey: viewerFixtureKey, Endpoint: srv.URL, HTTP: srv.Client()}
	if err := client.CheckViewer(t.Context()); err != nil {
		t.Fatalf("CheckViewer() = %v, want no error", err)
	}
	sent, _ := query.Load().(string)
	if !strings.Contains(sent, "viewer") {
		t.Errorf("the request body carried %d bytes and no viewer query", len(sent))
	}
	if strings.Contains(sent, viewerFixtureKey) {
		t.Errorf("the request BODY carried the credential; the key belongs in the header alone")
	}
}

func TestCheckViewerUnreachableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	client := &Client{APIKey: viewerFixtureKey, Endpoint: url, HTTP: &http.Client{Timeout: 5 * time.Second}}
	err := client.CheckViewer(t.Context())
	if err == nil {
		t.Fatalf("CheckViewer() against a closed listener = nil, want a network error")
	}
	var backendErr *backends.Error
	if !errors.As(err, &backendErr) || backendErr.Reason != backends.ReasonNetwork {
		t.Errorf("CheckViewer() error = %v, want a classified network error", err)
	}
}
