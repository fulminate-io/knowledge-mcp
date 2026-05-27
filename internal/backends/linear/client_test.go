// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// captured is the test fixture for inspecting the request the fake Linear
// server received. Mirrors domains/llm/anthropic/anthropic_test.go:22.
type captured struct {
	method  string
	path    string
	headers http.Header
	body    json.RawMessage
}

// newFakeServer stands up an httptest.Server that records each request and
// replies with respBody under respStatus. Mirrors
// domains/llm/anthropic/anthropic_test.go:33 newFakeServer.
func newFakeServer(t *testing.T, respStatus int, respBody string) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.headers = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		cap.body = json.RawMessage(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// withClient builds a *Client backed by the fake server. Use generic
// placeholder values throughout — never the developer's real workspace key.
func withClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{APIKey: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestClientDoRequestShape(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{"data":{"viewer":{"id":"u_1"}}}`)
	c := withClient(t, srv)

	var out struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	err := c.do(context.Background(), `query { viewer { id } }`, nil, &out)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.Viewer.ID != "u_1" {
		t.Errorf("Viewer.ID = %q, want u_1", out.Viewer.ID)
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/graphql" {
		t.Errorf("path = %q, want /graphql", cap.path)
	}
	// Linear personal-API-key convention: NO "Bearer " prefix.
	if got := cap.headers.Get("Authorization"); got != "lin_api_test" {
		t.Errorf("Authorization = %q, want %q (raw key, no Bearer prefix)", got, "lin_api_test")
	}
	if !strings.Contains(string(cap.body), `"query":`) {
		t.Errorf("body missing query field: %s", string(cap.body))
	}
}

func TestClientDoVariablesEncoded(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{"data":null}`)
	c := withClient(t, srv)

	err := c.do(context.Background(), `query Q($k:String!) { x(key:$k) }`,
		map[string]any{"k": "ABC"}, nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if !strings.Contains(string(cap.body), `"variables":{"k":"ABC"}`) {
		t.Errorf("body missing or malformed variables: %s", string(cap.body))
	}
}

func TestClientDoUnauthorized(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusUnauthorized, `{"error":"bad key"}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestClientDoForbidden(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusForbidden, `{}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized for 403", err)
	}
}

func TestClientDoServerError(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusInternalServerError, `internal`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want 500-shaped wrap", err)
	}
}

func TestClientDoGraphQLError(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK,
		`{"data":null,"errors":[{"message":"team not found"}]}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{team(key:"NOPE"){id}}`, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "team not found") {
		t.Errorf("err = %v, want surfacing of GraphQL error message", err)
	}
}

// TestClientDoTransportError exercises the transport-failure path — close
// the fake server BEFORE calling do() so the underlying http.Client returns
// a connect error. Asserts the returned error is wrapped (contains "POST"
// and the URL) rather than nil-or-bare.
func TestClientDoTransportError(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{"data":null}`)
	c := withClient(t, srv)
	srv.Close() // force connect failure on the next request
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	if err == nil {
		t.Fatalf("expected transport error, got nil")
	}
	if !strings.Contains(err.Error(), "POST") {
		t.Errorf("err = %v, want wrapped transport error containing POST/URL", err)
	}
}

// TestClientDoCtxCancel exercises ctx-cancel — cancel the context BEFORE
// calling do() so the request fails immediately. Asserts the wrapped error
// surfaces context.Canceled.
func TestClientDoCtxCancel(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{"data":null}`)
	c := withClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before do() runs
	err := c.do(ctx, `query{viewer{id}}`, nil, nil)
	if err == nil {
		t.Fatalf("expected ctx-canceled error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
}

// --- Typed *backends.Error wrap assertions (T4 Phase 0) ---

// TestClient_HTTP429_IsTransientRateLimited asserts 429 wraps as
// *backends.Error{Transient:true, Reason:rate_limited}.
func TestClient_HTTP429_IsTransientRateLimited(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusTooManyRequests, `{"error":"slow down"}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if !be.Transient || be.Reason != backends.ReasonRateLimited {
		t.Errorf("got Transient=%v Reason=%q, want true/%q", be.Transient, be.Reason, backends.ReasonRateLimited)
	}
}

// TestClient_HTTP500_IsTransientHTTP5xx asserts 500 wraps as
// *backends.Error{Transient:true, Reason:http_5xx}.
func TestClient_HTTP500_IsTransientHTTP5xx(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusInternalServerError, `internal`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if !be.Transient || be.Reason != backends.ReasonHTTP5xx {
		t.Errorf("got Transient=%v Reason=%q, want true/%q", be.Transient, be.Reason, backends.ReasonHTTP5xx)
	}
}

// TestClient_HTTP400_IsTerminalValidation asserts 400 (not 401/403/429)
// wraps as *backends.Error{Transient:false, Reason:validation}.
func TestClient_HTTP400_IsTerminalValidation(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusBadRequest, `bad input`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Transient || be.Reason != backends.ReasonValidation {
		t.Errorf("got Transient=%v Reason=%q, want false/%q", be.Transient, be.Reason, backends.ReasonValidation)
	}
}

// TestClient_HTTP401_IsTerminalAuth asserts the typed wrap on 401
// preserves errors.Is(err, ErrUnauthorized) AND surfaces as
// *backends.Error{Reason:auth, Transient:false}.
func TestClient_HTTP401_IsTerminalAuth(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusUnauthorized, `{}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("errors.Is(err, ErrUnauthorized) = false; want true")
	}
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Transient || be.Reason != backends.ReasonAuth {
		t.Errorf("got Transient=%v Reason=%q, want false/%q", be.Transient, be.Reason, backends.ReasonAuth)
	}
}

// TestClient_TransportError_IsTransientNetwork — closing the test server
// forces a connection-refused which classifies as ReasonNetwork.
func TestClient_TransportError_IsTransientNetwork(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{"data":null}`)
	c := withClient(t, srv)
	srv.Close()
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if !be.Transient || be.Reason != backends.ReasonNetwork {
		t.Errorf("got Transient=%v Reason=%q, want true/%q", be.Transient, be.Reason, backends.ReasonNetwork)
	}
}

// TestClient_CtxCancel_IsTransientTimeout — pre-canceled ctx classifies
// as ReasonTimeout (transient because operator may retry).
func TestClient_CtxCancel_IsTransientTimeout(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{"data":null}`)
	c := withClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.do(ctx, `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if !be.Transient || be.Reason != backends.ReasonTimeout {
		t.Errorf("got Transient=%v Reason=%q, want true/%q", be.Transient, be.Reason, backends.ReasonTimeout)
	}
}

// TestClient_GraphQLError_IsTerminalGraphQL — top-level errors[] in a
// 200 response wraps as terminal ReasonGraphQL.
func TestClient_GraphQLError_IsTerminalGraphQL(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK,
		`{"data":null,"errors":[{"message":"bad query"}]}`)
	c := withClient(t, srv)
	err := c.do(context.Background(), `query{viewer{id}}`, nil, nil)
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Transient || be.Reason != backends.ReasonGraphQL {
		t.Errorf("got Transient=%v Reason=%q, want false/%q", be.Transient, be.Reason, backends.ReasonGraphQL)
	}
}

func TestEnabledReadsEnv(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	if Enabled() {
		t.Errorf("Enabled() with empty key = true, want false")
	}
	t.Setenv("LINEAR_API_KEY", "lin_api_xxx")
	if !Enabled() {
		t.Errorf("Enabled() with set key = false, want true")
	}
}
