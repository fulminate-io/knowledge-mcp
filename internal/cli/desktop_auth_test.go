// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

type desktopStore struct {
	values map[string]string
	fail   string
}

func (s *desktopStore) Get(_ context.Context, k string) (string, error) {
	if s.fail == "get:"+k {
		return "", errors.New("private diagnostic")
	}
	v, ok := s.values[k]
	if !ok {
		return "", auth.ErrNotFound
	}
	return v, nil
}
func (s *desktopStore) Set(_ context.Context, k, v string) error {
	if s.fail == "set:"+k {
		return errors.New("private diagnostic")
	}
	s.values[k] = v
	return nil
}
func (s *desktopStore) Delete(_ context.Context, k string) error {
	if s.fail == "delete:"+k {
		return errors.New("private diagnostic")
	}
	delete(s.values, k)
	return nil
}
func TestDesktopAuthStatusAndCleanup(t *testing.T) {
	ctx := context.Background()
	s := &desktopStore{values: map[string]string{}}
	d := desktopAuth{store: s, configPath: filepath.Join(t.TempDir(), "config")}
	if got := d.status(ctx); got.State != "signed_out" {
		t.Fatal(got)
	}
	s.values[auth.KeyRefreshToken] = "refresh"
	if got := d.status(ctx); got.State != "unpublished" {
		t.Fatal(got)
	}
	s.values[auth.KeyAccessToken] = "access"
	s.values[auth.KeyAccessTokenExpiry] = "2099-01-02T03:04:05Z"
	if got := d.status(ctx); got.State != "signed_in" || got.Expiry != "" {
		t.Fatal(got)
	}
	s.values[auth.KeyAccessTokenExpiry] = "2000-01-02T03:04:05Z"
	if got := d.status(ctx); got.State != "expired" {
		t.Fatal(got)
	}
	for _, key := range []string{auth.KeyRefreshToken, auth.KeyAccessToken, auth.KeyAccessTokenExpiry, auth.KeyClientID} {
		s.fail = "get:" + key
		if got := d.status(ctx); got.State != "unavailable" {
			t.Error(key, got)
		}
	}
	s.fail = "delete:" + auth.KeyRefreshToken
	for _, key := range []string{auth.KeyRefreshToken, auth.KeyAccessToken, auth.KeyAccessTokenExpiry, auth.KeyClientID} {
		s.values[key] = "value"
	}
	if err := deleteCredentials(ctx, s); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if len(s.values) != 1 {
		t.Fatal("cleanup did not attempt every key")
	}
	s.fail = ""
	if err := deleteCredentials(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := d.status(ctx); got.State != "signed_out" {
		t.Fatal(got)
	}
}

func TestDesktopAccountsIncomplete(t *testing.T) {
	for _, body := range []string{`{}`, `{"accounts":null,"count":0}`, `{"accounts":[],"count":2}`, `{"accounts":[{}],"count":1}`} {
		t.Run(body, func(t *testing.T) {
			serveAccounts(t, body)
			if _, err := fetchAccounts(context.Background()); err == nil {
				t.Fatal("incomplete membership response accepted")
			}
		})
	}
}

func TestDesktopLoginSelectionAndFailures(t *testing.T) {
	ctx := context.Background()
	original := desktopBrowserFlow
	t.Cleanup(func() { desktopBrowserFlow = original })
	desktopBrowserFlow = func(context.Context, *auth.DiscoveredEndpoints) (string, *auth.TokenResponse, error) {
		return "client", &auth.TokenResponse{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600}, nil
	}
	withFakeDiscovery(t, "http://revocation.invalid")
	cases := []struct{ name, selected, body, want, code string }{
		{name: "none", body: `{"accounts":[],"count":0}`, code: "account_ineligible"},
		{name: "one", body: `{"accounts":[{"id":"acct_A","name":"A","slug":"a","role":"owner","has_active_subscription":true}],"count":1}`, want: "acct_A"},
		{name: "multiple", body: `{"accounts":[{"id":"acct_A","name":"A","slug":"a","role":"owner","has_active_subscription":true},{"id":"acct_B","name":"B","slug":"b","role":"member","has_active_subscription":true}],"count":2}`, want: "acct_A"},
		{name: "unsubscribed", body: `{"accounts":[{"id":"acct_A","name":"A","slug":"a","role":"owner","has_active_subscription":false}],"count":1}`, want: "acct_A", code: "account_ineligible"},
		{name: "preserve healthy", selected: "acct_A", body: `{"accounts":[{"id":"acct_A","name":"A","slug":"a","role":"owner","has_active_subscription":true}],"count":1}`, want: "acct_A"},
		{name: "preserve lost membership", selected: "acct_A", body: `{"accounts":[],"count":0}`, want: "acct_A", code: "account_ineligible"},
		{name: "preserve lost subscription", selected: "acct_A", body: `{"accounts":[{"id":"acct_A","name":"A","slug":"a","role":"owner","has_active_subscription":false}],"count":1}`, want: "acct_A", code: "account_ineligible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serveAccounts(t, tc.body)
			path := filepath.Join(t.TempDir(), "config")
			if tc.selected != "" {
				if err := config.WriteSelectedAccountID(path, tc.selected); err != nil {
					t.Fatal(err)
				}
			}
			s := &desktopStore{values: map[string]string{}}
			tr, err := buildSyncTransportFn()
			if err != nil {
				t.Fatal(err)
			}
			d := desktopAuth{store: s, configPath: path, transport: tr}
			if _, code, err := d.login(ctx); code != tc.code || err != nil {
				t.Fatal(code)
			}
			got, err := config.ReadSelectedAccountID(path)
			if err != nil || got != tc.want {
				t.Fatal("selection mismatch", got, err)
			}
			if d.status(ctx).State != "signed_in" {
				t.Fatal("session not usable")
			}
		})
	}
	for _, key := range []string{auth.KeyClientID, auth.KeyRefreshToken, auth.KeyAccessToken, auth.KeyAccessTokenExpiry} {
		t.Run("write failure "+key, func(t *testing.T) {
			s := &desktopStore{values: map[string]string{}, fail: "set:" + key}
			d := desktopAuth{store: s, configPath: filepath.Join(t.TempDir(), "config")}
			if _, code, err := d.login(ctx); code != "session_incomplete" || err != nil {
				t.Fatal("write failure hidden", code)
			}
		})
	}
}

func TestDesktopAccountSelectionAndPublicErrors(t *testing.T) {
	ctx := context.Background()
	serveAccounts(t, twoAccountsBody)
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "acct_01ACME"); err != nil {
		t.Fatal(err)
	}
	s := &desktopStore{values: map[string]string{}}
	tr, err := buildSyncTransportFn()
	if err != nil {
		t.Fatal(err)
	}
	d := desktopAuth{store: s, configPath: path, transport: tr}
	for _, id := range []string{"not-a-member", "acct_01HOBBY"} {
		if _, code, err := d.selectAccount(ctx, id); code != "account_ineligible" || err != nil {
			t.Errorf("selecting %s: code=%s err=%v", id, code, err)
			continue
		}
		got, err := config.ReadSelectedAccountID(path)
		if err != nil || got != "acct_01ACME" {
			t.Errorf("selecting %s changed the selection to %s (err=%v)", id, got, err)
		}
	}
	d.configPath = t.TempDir()
	if result := d.status(ctx); result.AccountCode != "account_unavailable" {
		t.Fatal("config failure hidden")
	}
	if _, code, err := d.selectAccount(ctx, "acct_01ACME"); code != "account_write_failed" || err != nil {
		t.Fatal("config write failure hidden", code)
	}
	s.fail = "get:" + auth.KeyRefreshToken
	if result := d.perform(ctx, "status", ""); result.State != "unavailable" || result.Code != "credential_unavailable" {
		t.Fatal("store error classified absent")
	}
}

func TestDesktopLogoutReportsUnconfirmedRevocation(t *testing.T) {
	withFakeDiscovery(t, "")
	s := &desktopStore{values: map[string]string{auth.KeyRefreshToken: "refresh", auth.KeyClientID: "client", auth.KeyAccessToken: "access", auth.KeyAccessTokenExpiry: "2099-01-02T03:04:05Z"}}
	d := desktopAuth{store: s, configPath: filepath.Join(t.TempDir(), "config")}
	result := d.perform(context.Background(), "logout", "")
	if result.State != "signed_out" || result.Code != "revocation_unavailable" || len(s.values) != 0 {
		t.Fatal("revocation must be distinguished from completed local cleanup", result)
	}
}
