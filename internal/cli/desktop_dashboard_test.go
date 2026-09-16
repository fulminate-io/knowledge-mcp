// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

func TestDesktopDashboardDocuments(t *testing.T) {
	store := withMemoryStore(t)
	if err := store.Set(t.Context(), auth.KeyRefreshToken, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	body := `{"documents":[{"id":"doc-1","name":"Dashboard","env_id":"env-1","draft_doc":{"version":1,"puck":{"content":[],"root":{}},"endpoints":[]},"secret":"not-public"}]}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(auth.AccountHeaderName) != "account-A" || r.URL.Path != "/v1/accounts/account-A/ui-documents" {
			t.Error("wrong account/document path")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	old := nativeManagementTransport
	t.Cleanup(func() { nativeManagementTransport = old })
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "access"})
	}
	req := dashboardRequest{Operation: "list", Account: "account-A"}
	result := performDashboard(t.Context(), req)
	if result.Code != "" || len(result.Documents) != 1 || result.Documents[0].Name != "Dashboard" {
		t.Fatalf("list: %+v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("empty")
	}
	for _, bad := range []string{`null`, `{}`, `{"documents":null}`, `{"documents":[{"id":"doc-1"}]}`, `{"documents":[]} trailing`} {
		body = bad
		if got := performDashboard(t.Context(), req); got.Code != "invalid_response" {
			t.Errorf("accepted %s: %+v", bad, got)
		}
	}
	before := calls
	if err := DesktopRemoteCmd([]string{"dashboard", `{"operation":"list","account":"account-A","url":"https://elsewhere"}`}); err == nil || calls != before {
		t.Fatal("arbitrary proxy accepted")
	}
}

func TestDesktopDashboardSavedEndpointSSH(t *testing.T) {
	store := withMemoryStore(t)
	if err := store.Set(t.Context(), auth.KeyRefreshToken, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	peer, count := dashboardPeer(t)
	oldRelay := dashboardRelayURL
	dashboardRelayURL = func() (string, error) { return strings.Replace(peer.URL, "http://", "ws://", 1) + wsProxyPath, nil }
	t.Cleanup(func() { dashboardRelayURL = oldRelay })
	draftPath, draftMethod := "/draft-must-not-run", "POST"
	endpointPath, method, envID, port := "/stats", "GET", "env-native", 8080
	corruptCA := false
	badCA := "wrong"
	wrongRelayEnv := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(auth.AccountHeaderName) != "account-A" {
			t.Error("lost account")
		}
		switch r.URL.Path {
		case "/v1/accounts/account-A/ui-documents/doc-1":
			envelope := map[string]any{"version": 1, "puck": map[string]any{"content": []any{}, "root": map[string]any{}}, "endpoints": []any{map[string]any{"id": "e1", "method": method, "path": endpointPath}}}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "doc-1", "name": "Live", "env_id": envID, "published_doc": envelope, "draft_doc": map[string]any{"version": 1, "puck": map[string]any{"content": []any{}}, "endpoints": []any{map[string]any{"id": "e1", "method": draftMethod, "path": draftPath}}}})
		case "/v1/accounts/account-A/dev-vm/env-native":
			_ = json.NewEncoder(w).Encode(map[string]any{"env_id": "env-native", "name": "saved-name", "state": "running", "hosted_port": port})
		case "/v1/dev-vm/connect":
			response, err := http.Post(peer.URL+"/v1/dev-vm/connect", "application/json", r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			var capability connectResponse
			decoder := json.NewDecoder(response.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&capability) != nil {
				t.Error("decode capability")
				return
			}
			if corruptCA {
				capability.HostCAPubKey = badCA
			}
			if wrongRelayEnv {
				capability.RelayToken = "eyJhbGciOiJIUzI1NiJ9.eyJkZXZfZW52X2lkIjoib3RoZXItZW52In0.eA"
			}
			_ = json.NewEncoder(w).Encode(capability)
		default:
			t.Error("unexpected API path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	old := nativeManagementTransport
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(api.URL, auth.StaticTokenSource{AccessToken: "native-access"})
	}
	t.Cleanup(func() { nativeManagementTransport = old })
	request := dashboardRequest{Operation: "request", Account: "account-A", Document: "doc-1", Environment: "env-native", Endpoint: "e1"}
	for _, tc := range []struct {
		path, method string
		status       int
	}{{path: "/stats", method: "", status: 200}, {path: "/stats", method: "GET", status: 200}, {path: "/write", method: "POST", status: 200}, {path: "/empty", method: "HEAD", status: 204}, {path: "/error", method: "PATCH", status: 502}, {path: "/redirect", method: "GET", status: 302}} {
		endpointPath, method = tc.path, tc.method
		before := count.Load()
		result := performDashboard(t.Context(), request)
		if result.Code != "" || result.Response == nil || result.Response.Status != tc.status || count.Load() != before+1 {
			t.Errorf("%+v result=%+v requests=%d", tc, result, count.Load()-before)
		}
	}

	// Authoring preview explicitly reads the saved draft; published runtime stays separate.
	endpointPath, method = "/write", "POST"
	draftPath, draftMethod = "/stats", "GET"
	preview := request
	preview.Operation = "read"
	preview.Draft = true
	beforePreview := count.Load()
	if got := performDashboard(t.Context(), preview); got.Code != "" || got.Response == nil || got.Response.Status != 200 || count.Load() != beforePreview+1 {
		t.Fatalf("saved draft preview: %+v", got)
	}
	preview.Draft = false
	if got := performDashboard(t.Context(), preview); got.Code != "dashboard_changed" || count.Load() != beforePreview+1 {
		t.Fatalf("runtime read must retain published POST refusal: %+v", got)
	}
	preview.Draft = true
	preview.Operation = "request"
	if got := performDashboard(t.Context(), preview); got.Code != "invalid_request" || count.Load() != beforePreview+1 {
		t.Fatalf("draft writes must be refused: %+v", got)
	}
	draftMethod = "POST"
	preview.Operation = "read"
	if got := performDashboard(t.Context(), preview); got.Code != "dashboard_changed" || count.Load() != beforePreview+1 {
		t.Fatalf("draft read must refuse POST: %+v", got)
	}
	method = "OPTIONS"
	beforeInvalid := count.Load()
	if got := performDashboard(t.Context(), request); got.Code != "invalid_endpoint" || count.Load() != beforeInvalid {
		t.Fatalf("unsupported saved method: %+v", got)
	}
	method = "GET"
	missing := request
	missing.Endpoint = "unknown-endpoint"
	if got := performDashboard(t.Context(), missing); got.Code != "invalid_endpoint" || count.Load() != beforeInvalid {
		t.Fatalf("unknown endpoint: %+v", got)
	}
	endpointPath, method = "/barrier", "GET"
	overlapCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	completed := make(chan dashboardResult, 3)
	for range 3 {
		go func() { completed <- performDashboard(overlapCtx, request) }()
	}
	for range 3 {
		if got := <-completed; got.Code != "" || got.Response == nil || got.Response.Body != "overlap" {
			t.Errorf("independent requests did not overlap: %+v", got)
		}
	}
	t.Logf("three request-scoped mint/SSH/HTTP operations overlapped in %s", time.Since(started))
	for _, bad := range []string{"https://elsewhere.invalid/", "//elsewhere.invalid/", "/bad\r\nInjected:yes", ""} {
		endpointPath = bad
		before := count.Load()
		if got := performDashboard(t.Context(), request); got.Code != "invalid_endpoint" || count.Load() != before {
			t.Errorf("invalid path %q: %+v", bad, got)
		}
	}
	endpointPath = "/stats"
	method = "GET"
	for _, bad := range []int{0, -1, 65536} {
		port = bad
		if got := performDashboard(t.Context(), request); got.Code != "no_hosted_port" {
			t.Errorf("port %d: %+v", bad, got)
		}
	}
	port = 8080
	envID = "other-env"
	if got := performDashboard(t.Context(), request); got.Code != "dashboard_changed" {
		t.Fatalf("env mismatch %+v", got)
	}
	envID = "env-native"
	corruptCA = true
	for _, value := range []string{"", "wrong"} {
		badCA = value
		before := count.Load()
		if got := performDashboard(t.Context(), request); got.Code != "connection_refused" || count.Load() != before {
			t.Errorf("missing/invalid CA refusal %+v", got)
		}
	}

	corruptCA = false
	wrongRelayEnv = true
	beforeRelay := count.Load()
	if got := performDashboard(t.Context(), request); got.Code != "connection_refused" || count.Load() != beforeRelay {
		t.Fatalf("relay environment mismatch: %+v", got)
	}
	wrongRelayEnv = false
	for _, mode := range []string{"wrong-principal", "expired", "wrong-ca", "raw"} {
		response, err := http.Get(peer.URL + "/fixture/host?mode=" + mode)
		if err != nil {
			t.Error(err)
			continue
		}
		_ = response.Body.Close()
		before := count.Load()
		got := performDashboard(t.Context(), request)
		if got.Code != "connection_refused" || count.Load() != before {
			t.Errorf("host %s was not refused: %+v", mode, got)
		}
	}
	response, err := http.Get(peer.URL + "/fixture/host?mode=valid")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	endpointPath = "/oversize"
	if got := performDashboard(t.Context(), request); got.Code != "response_too_large" {
		t.Fatalf("oversize %+v", got)
	}
}
