// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

type desktopAccountFixture struct {
	mode                   string
	memberships, refreshes int
	store                  *desktopStore
	endpoint               string
}

func desktopAccountTest(t *testing.T) (*desktopAccountFixture, desktopAuth) {
	t.Helper()
	f := &desktopAccountFixture{store: &desktopStore{values: map[string]string{auth.KeyAccessToken: "published", auth.KeyAccessTokenExpiry: "2099-01-02T03:04:05Z", auth.KeyRefreshToken: "refresh", auth.KeyClientID: "client"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": "https://fixture.invalid/mcp", "authorization_servers": []string{"https://fixture.invalid"}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": "https://fixture.invalid", "authorization_endpoint": "https://fixture.invalid/authorize", "token_endpoint": "https://fixture.invalid/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/token":
			f.refreshes++
			switch f.mode {
			case "terminal":
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"secret-provider-body"}`))
			case "refresh401":
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			case "refresh500":
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "renewed", "refresh_token": "rotated", "expires_in": 3600, "token_type": "Bearer"})
			}
		case "/v1/me/accounts":
			f.memberships++
			switch f.mode {
			case "terminal", "refresh401", "refresh500", "retry", "second401":
				if r.Header.Get("Authorization") != "Bearer renewed" || f.mode == "second401" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{}`))
					return
				}
			case "unreachable":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			case "403":
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{}`))
				return
			case "500":
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{}`))
				return
			case "malformed":
				_, _ = w.Write([]byte(`{`))
				return
			case "partial":
				_, _ = w.Write([]byte(`{"accounts":[],"count":1}`))
				return
			}
			_, _ = w.Write([]byte(twoAccountsBody))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	f.endpoint = srv.URL
	base := http.DefaultTransport
	http.DefaultTransport = desktopFixtureTransport{base: base, endpoint: srv.URL}
	t.Cleanup(func() { http.DefaultTransport = base })
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "acct_01ACME"); err != nil {
		t.Fatal(err)
	}
	prior := buildSyncTransportFn
	buildSyncTransportFn = func() (*auth.Transport, error) {
		return auth.NewSyncTransport(srv.URL, auth.NewOAuthTokenSource(f.store, srv.URL, map[string]struct{}{"fixture.invalid": {}}), auth.WithAccountSelection(auth.NewAccountSelection(path, time.Second))), nil
	}
	t.Cleanup(func() { buildSyncTransportFn = prior })
	tr, err := buildSyncTransportFn()
	if err != nil {
		t.Fatal(err)
	}
	return f, desktopAuth{store: f.store, configPath: path, transport: tr}
}

func TestDesktopAccountOutcomes(t *testing.T) {
	for _, tc := range []struct {
		mode, state, code string
		published         bool
	}{
		{mode: "valid", state: "signed_in", published: true},
		{mode: "retry", state: "signed_in", published: true},
		{mode: "terminal", state: "expired", code: "sign_in_required", published: true},
		{mode: "refresh401", state: "expired", code: "sign_in_required", published: true},
		{mode: "second401", state: "expired", code: "sign_in_required", published: true},
		{mode: "403", state: "signed_in", code: "account_unavailable", published: true},
		{mode: "500", state: "signed_in", code: "account_unavailable", published: true},
		{mode: "malformed", state: "signed_in", code: "account_unavailable", published: true},
		{mode: "partial", state: "signed_in", code: "account_unavailable", published: true},
		{mode: "unreachable", state: "signed_in", code: "account_unavailable", published: true},
		{mode: "valid", state: "signed_in"},
		{mode: "refresh500", state: "unpublished", code: "account_unavailable"},
		{mode: "terminal", state: "expired", code: "sign_in_required"},
	} {
		t.Run(tc.mode+map[bool]string{true: " published", false: " refresh only"}[tc.published], func(t *testing.T) {
			f, d := desktopAccountTest(t)
			f.mode = tc.mode
			if !tc.published {
				delete(f.store.values, auth.KeyAccessToken)
				delete(f.store.values, auth.KeyAccessTokenExpiry)
			}
			got := d.perform(context.Background(), "accounts", "")
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"secret-provider-body", "invalid_grant", "rotated", "renewed", "published"} {
				if strings.Contains(string(raw), secret) && secret != "published" {
					t.Errorf("public response leaked %s", secret)
				}
			}
			if got.State != tc.state || got.AccountCode != tc.code {
				t.Errorf("state=%s code=%s want=%s/%s", got.State, got.AccountCode, tc.state, tc.code)
			}
			if tc.code != "" {
				if got.Expiry != "" || got.Accounts != nil {
					t.Error("failed operation claims validated session")
				}
			} else {
				if got.Expiry == "" || got.Accounts == nil || len(*got.Accounts) != 2 {
					t.Error("successful operation missing session or memberships")
				}
			}
			if tc.mode == "valid" && tc.published && f.refreshes != 0 {
				t.Errorf("unnecessary refresh=%d", f.refreshes)
			}
			if tc.mode == "retry" {
				expiry, err := time.Parse(time.RFC3339, got.Expiry)
				if err != nil || time.Until(expiry) < 59*time.Minute || time.Until(expiry) > time.Hour {
					t.Error("response does not reflect refresh expiry")
				}
			}
			if tc.mode == "retry" && (f.refreshes != 1 || f.memberships != 2) {
				t.Errorf("retry counts=%d/%d", f.refreshes, f.memberships)
			}
		})
	}
}

func TestDesktopAccountStatusRecovery(t *testing.T) {
	f, d := desktopAccountTest(t)
	ctx := context.Background()
	f.mode = "terminal"
	for range 2 {
		got := d.perform(ctx, "accounts", "")
		if got.State != "expired" || got.AccountCode != "sign_in_required" {
			t.Error("terminal rejection lost")
		}
		fresh := desktopAuth{store: f.store, configPath: d.configPath}
		if got := fresh.perform(ctx, "status", ""); got.Expiry != "" || got.Accounts != nil {
			t.Error("stored-only status claims validation")
		}
	}
	d.transport, _ = buildSyncTransportFn()
	f.mode = "500"
	if got := d.perform(ctx, "accounts", ""); got.AccountCode != "account_unavailable" || got.Expiry != "" {
		t.Error("transient failure not distinct")
	}
	d.transport, _ = buildSyncTransportFn()
	f.mode = "valid"
	if got := d.perform(ctx, "accounts", ""); got.AccountCode != "" || got.Expiry == "" || got.Accounts == nil {
		t.Error("successful recovery missing")
	}
	f.store.values = map[string]string{}
	d.transport, _ = buildSyncTransportFn()
	if got := d.perform(ctx, "accounts", ""); got.State != "expired" || got.AccountCode != "sign_in_required" {
		t.Error("absent session not terminal")
	}
}

func TestDesktopAccountOperationCounts(t *testing.T) {
	f, d := desktopAccountTest(t)
	ctx := context.Background()
	prior := desktopBrowserFlow
	t.Cleanup(func() { desktopBrowserFlow = prior })
	withFakeDiscovery(t, "http://revocation.invalid")
	desktopBrowserFlow = func(context.Context, *auth.DiscoveredEndpoints) (string, *auth.TokenResponse, error) {
		return "client", &auth.TokenResponse{AccessToken: "published", RefreshToken: "refresh", ExpiresIn: 3600}, nil
	}
	for _, op := range []string{"login", "select"} {
		f.memberships = 0
		got := d.perform(ctx, op, "acct_01ACME")
		if f.memberships != 1 || got.Accounts == nil {
			t.Errorf("%s membership calls=%d returned=%v", op, f.memberships, got.Accounts != nil)
		}
	}
}
