// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

func TestDesktopRemotePublicProjection(t *testing.T) {
	store := withMemoryStore(t)
	ctx := t.Context()
	if err := store.Set(ctx, auth.KeyRefreshToken, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "account-B"); err != nil {
		t.Fatal(err)
	}
	body := `[{"env_id":"env-1","name":"Workspace","state":"starting","power_state":"running","harness":{"harness_name":"Agent","state":"working","current_task":"Build"},"health":{"status":"healthy"},"custom_env_vars":[{"value":"private-secret"}],"monitoring":{"processes":[{"command":"private-command"}]}}]`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(auth.AccountHeaderName) != "account-A" || r.URL.Path != "/v1/accounts/account-A/dev-vm" {
			t.Error("account path/header not captured")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	original := nativeManagementTransport
	t.Cleanup(func() { nativeManagementTransport = original })
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "test-native"})
	}
	for _, account := range []string{"", "../account", "--account"} {
		if err := DesktopRemoteCmd([]string{"list", "--account", account}); err == nil {
			t.Errorf("DesktopRemoteCmd accepted invalid intended account %q", account)
		}
	}
	if calls != 0 {
		t.Fatal("invalid intended account reached management")
	}
	for _, machine := range []string{"", "fwh_machine"} {
		t.Setenv("KNOWLEDGE_AUTH_TOKEN", machine)
		out := performDesktopRemote(ctx, "account-A", "list", "")
		if out.Code != "" || out.Environments == nil {
			t.Errorf("performDesktopRemote projection failed: %+v", out)
			continue
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-secret", "private-command", "private-refresh"} {
			if strings.Contains(string(raw), secret) {
				t.Error("performDesktopRemote credential projection leaked")
			}
		}
		env := (*out.Environments)[0]
		if env.Power != "running" || env.State != "starting" || env.AgentState != "working" || env.Health != "healthy" {
			t.Errorf("performDesktopRemote display facts lost: %+v", env)
		}
	}
	for _, bad := range []string{`null`, `{}`, `[{"env_id":"env-1"}]`, `[{"env_id":"../bad","name":"N","state":"running","power_state":"running"}]`} {
		body = bad
		if got := performDesktopRemote(ctx, "account-A", "list", ""); got.Code != "invalid_response" {
			t.Errorf("performDesktopRemote bad response accepted: %+v", got)
		}
	}
	if err := store.Delete(ctx, auth.KeyRefreshToken); err != nil {
		t.Fatal(err)
	}
	before := calls
	for _, account := range []string{"", "../account", "--account"} {
		if out := performDesktopRemote(ctx, account, "list", ""); out.Code != "account_unavailable" || calls != before {
			t.Errorf("performDesktopRemote invalid account %q reached management: %+v", account, out)
		}
	}
	if out := performDesktopRemote(ctx, "account-A", "list", ""); out.Code != "sign_in_required" || calls != before {
		t.Fatal("machine-only request reached management")
	}
}
