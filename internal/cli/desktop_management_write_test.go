// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

func TestDesktopManagementWrites(t *testing.T) {
	store := withMemoryStore(t)
	if err := store.Set(t.Context(), auth.KeyRefreshToken, "fixture-refresh"); err != nil {
		t.Fatal(err)
	}
	var method, path, payload string
	calls := 0
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != method || r.URL.Path != path || r.Header.Get(auth.AccountHeaderName) != "account-A" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != payload {
			t.Errorf("wrong body %s want %s", body, payload)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "private-provider-error")
			return
		}
		if method == "DELETE" {
			if strings.Contains(path, "ui-documents") {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
			return
		}
		if strings.Contains(path, "ui-documents") {
			_, _ = io.WriteString(w, `{"id":"doc-1","name":"Dashboard","env_id":"env-1","doc_version":1,"draft_doc":{"version":1,"puck":{"content":[]},"endpoints":[]}}`)
			return
		}
		if method == "POST" {
			w.WriteHeader(http.StatusAccepted)
		}
		_, _ = io.WriteString(w, `{"env_id":"env-1","name":"workspace","state":"starting","power_state":"running","hosted_port":8080,"custom_env_vars":[{"name":"TOKEN","value":"editable-secret"}],"provider_token":"not-public"}`)
	}))
	defer server.Close()
	old := nativeManagementTransport
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "fixture-native"})
	}
	t.Cleanup(func() { nativeManagementTransport = old })
	for _, tc := range []struct{ kind, operation, id, method, payload string }{
		{kind: "environment", operation: "create", method: "POST", payload: `{"name":"workspace","base_template":"base"}`},
		{kind: "environment", operation: "update", id: "env-1", method: "PUT", payload: `{"custom_env_vars":[{"name":"TOKEN","value":"editable-secret"}],"knowledge_config":{"enabled":false,"summarizer":{"provider":"anthropic","model":"model"}},"hosted_port":8080}`},
		{kind: "environment", operation: "delete", id: "env-1", method: "DELETE", payload: `{"keep_volume":true,"volume_name":"saved"}`},
		{kind: "environment", operation: "delete", id: "env-1", method: "DELETE"},
		{kind: "dashboard", operation: "create", method: "POST", payload: `{"name":"Dashboard","env_id":"env-1","doc_version":1,"draft_doc":{"version":1,"puck":{"content":[]},"endpoints":[]}}`},
		{kind: "dashboard", operation: "update", id: "doc-1", method: "PUT", payload: `{"name":"Dashboard","env_id":"env-1","doc_version":1,"draft_doc":{"version":1,"puck":{"content":[]},"endpoints":[]}}`},
		{kind: "dashboard", operation: "publish", id: "doc-1", method: "POST"},
		{kind: "dashboard", operation: "delete", id: "doc-1", method: "DELETE"},
	} {
		t.Run(tc.kind+"-"+tc.operation, func(t *testing.T) {
			method, payload = tc.method, tc.payload
			path = "/v1/accounts/account-A/dev-vm"
			idKey := "environment"
			if tc.kind == "dashboard" {
				path = "/v1/accounts/account-A/ui-documents"
				idKey = "document"
			}
			if tc.id != "" {
				path += "/" + tc.id
			}
			if tc.operation == "publish" {
				path += "/publish"
			}
			req := map[string]any{"operation": tc.operation, "account": "account-A"}
			if tc.id != "" {
				req[idKey] = tc.id
			}
			if payload != "" {
				req["payload"] = json.RawMessage(payload)
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			before := calls
			if err := DesktopRemoteCmd([]string{tc.kind, string(raw)}); err != nil {
				t.Fatal(err)
			}
			if calls != before+1 {
				t.Fatalf("calls=%d", calls-before)
			}
			if tc.kind == "environment" {
				out := performEnvironmentWrite(t.Context(), environmentRequest{Operation: tc.operation, Account: "account-A", Environment: tc.id, Payload: json.RawMessage(payload)})
				if out.Code != "" {
					t.Fatal(out.Code)
				}
				if tc.operation == "delete" {
					if !out.Accepted {
						t.Fatal("missing acknowledgement")
					}
				} else if out.Environments == nil || (*out.Environments)[0].Configuration.CustomEnvVars[0].Value != "editable-secret" {
					t.Fatal("editable config lost")
				}
			} else {
				out := performDashboard(t.Context(), dashboardRequest{Operation: tc.operation, Account: "account-A", Document: tc.id, Payload: json.RawMessage(payload)})
				if out.Code != "" {
					t.Fatal(out.Code)
				}
				if tc.operation == "delete" {
					if !out.Deleted {
						t.Fatal("missing delete")
					}
				} else if out.Document == nil || out.Document.Version != 1 {
					t.Fatal("document version lost")
				}
			}
		})
	}
	method, path, payload = "PUT", "/v1/accounts/account-A/dev-vm/env-1", `{}`
	for _, tc := range []struct {
		status int
		code   string
	}{
		{status: 400, code: "invalid_request"},
		{status: 401, code: "sign_in_required"},
		{status: 403, code: "permission_denied"},
		{status: 404, code: "not_found"},
		{status: 409, code: "conflict"},
		{status: 429, code: "rate_limited"},
		{status: 500, code: "unavailable"},
	} {
		status = tc.status
		got := performEnvironmentWrite(t.Context(), environmentRequest{Operation: "update", Account: "account-A", Environment: "env-1", Payload: json.RawMessage(payload)})
		if got.Code != tc.code {
			t.Errorf("status%d code=%s", status, got.Code)
		}
	}
}

func TestDesktopManagementRejectsInvalidWrites(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"create","account":"../bad","payload":{"name":"a","base_template":"b"}}`,
		`{"operation":"create","account":"a","payload":{"name":"a","base_template":"b","custom_image":"c"}}`,
		`{"operation":"update","account":"a","environment":"e","payload":{"hosted_port":22}}`,
		`{"operation":"update","account":"a","environment":"e","payload":{"unexpected":true}}`,
		`{"operation":"update","account":"a","environment":"e","payload":{"knowledge_config":{"api_key":"secret"}}}`,
		`{"operation":"delete","account":"a","environment":"e","payload":{"keep_volume":true}}`,
		`{"operation":"update","account":"a","environment":"e","payload":null}`,
	} {
		if err := DesktopRemoteCmd([]string{"environment", raw}); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{
		`{"operation":"publish","account":"a","document":"../bad"}`,
		`{"operation":"delete","account":"a","document":"d","payload":{}}`,
		`{"operation":"create","account":"a","payload":{"name":"n","env_id":"e","doc_version":2}}`,
		`{"operation":"create","account":"a","payload":{"name":"n","env_id":"e","doc_version":1,"draft_doc":{}}}`,
	} {
		if err := DesktopRemoteCmd([]string{"dashboard", raw}); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestDesktopManagementConfigurationProjection(t *testing.T) {
	raw := []byte(`{"env_id":"env-1","name":"workspace","state":"running","power_state":"running","hosted_port":8080,"custom_env_vars":[{"name":"KEY","value":"editable"}],"provider_token":"private-provider"}`)
	for _, op := range []string{"get", "create", "update", "start", "stop"} {
		result := projectDesktopEnvironments("account-A", op, "env-1", raw)
		if result.Code != "" || result.Environments == nil {
			t.Errorf("%s: %+v", op, result)
			continue
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
			continue
		}
		if strings.Contains(string(encoded), "private-provider") {
			t.Error("provider credential leaked")
		}
		want := op == "get" || op == "create" || op == "update"
		if strings.Contains(string(encoded), "editable") != want {
			t.Errorf("%s configuration projection wrong", op)
		}
	}
	if got := projectDesktopEnvironments("account-A", "update", "wrong-id", raw); got.Code != "invalid_response" {
		t.Fatal("wrong response identity accepted")
	}
}

func TestDesktopManagementAcceptedCreationReceipt(t *testing.T) {
	store := withMemoryStore(t)
	if err := store.Set(t.Context(), auth.KeyRefreshToken, "fixture-refresh"); err != nil {
		t.Fatal(err)
	}
	const fallback = `{"env_id":"env-created","name":"workspace","base_template":"base","account_id":"account-A","state":"running","power_state":"","machine":{"vcpu":4,"ram_gb":8,"arch":"arm64"},"enabled_provider_ids":null,"custom_env_vars":null,"hosted_port":8080,"healthcheck_path":"/health"}`
	const full = `{"env_id":"env-created","name":"workspace","state":"provisioning","power_state":"provisioning","enabled_provider_ids":[],"custom_env_vars":[],"hosted_port":8080,"healthcheck_path":"/health"}`
	body := fallback
	creates, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(auth.AccountHeaderName) != "account-A" {
			t.Error("lost captured account")
		}
		switch r.Method {
		case http.MethodPost:
			creates++
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			reads++
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	old := nativeManagementTransport
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "fixture-native"})
	}
	t.Cleanup(func() { nativeManagementTransport = old })
	req := environmentRequest{Operation: "create", Account: "account-A", Payload: json.RawMessage(`{"name":"workspace","base_template":"base"}`)}
	result := performEnvironmentWrite(t.Context(), req)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if result.Code != "" || receipt["accepted"] != true || receipt["environment"] != "env-created" || receipt["readback_pending"] != true {
		t.Fatalf("accepted identity lost: %s", raw)
	}
	if result.Environments != nil || strings.Contains(string(raw), "power_state") || strings.Contains(string(raw), "configuration") {
		t.Fatalf("incomplete receipt fabricated detail: %s", raw)
	}
	if creates != 1 || reads != 0 {
		t.Fatalf("creation replayed or hidden read: posts=%d gets=%d", creates, reads)
	}
	for _, op := range []string{"get", "update"} {
		if got := projectDesktopEnvironments("account-A", op, "env-created", []byte(fallback)); got.Code != "invalid_response" {
			t.Errorf("%s accepted incomplete detail", op)
		}
	}
	body = full
	detail := performDesktopRemote(t.Context(), "account-A", "get", "env-created")
	if detail.Code != "" || detail.Environments == nil || (*detail.Environments)[0].Power != "provisioning" {
		t.Fatalf("later read unusable: %+v", detail)
	}
	complete := performEnvironmentWrite(t.Context(), req)
	if complete.Code != "" || complete.Environments == nil {
		t.Fatalf("full202 rejected: %+v", complete)
	}
	if creates != 2 || reads != 1 {
		t.Fatalf("unexpected requests posts=%d gets=%d", creates, reads)
	}
}
