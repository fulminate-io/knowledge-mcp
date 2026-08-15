// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// loginHarness wires the browser-flow seams so loginBrowserPKCE can be driven
// end to end without a browser, a listener, or a keychain.
func loginHarness(t *testing.T) {
	t.Helper()
	withMemoryStore(t)
	withFakeDiscovery(t, "http://revocation.invalid")
	withFakeBrowserFlow(t, "cli-acct", &auth.TokenResponse{
		AccessToken:  "at_login",
		RefreshToken: "frt_login",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	})
}

// runLogin drives the whole login and returns everything it printed.
func runLogin(t *testing.T) string {
	t.Helper()
	out, err := captureStdout(t, func() error { return loginBrowserPKCE(context.Background()) })
	if err != nil {
		t.Fatalf("loginBrowserPKCE: %v", err)
	}
	return out
}

// TestLogin_EstablishesSelectionWhenAbsent proves login writes the config entry
// when none is stored: a single membership writes that account, several write
// the first (oldest) entry.
func TestLogin_EstablishesSelectionWhenAbsent(t *testing.T) {
	t.Run("single membership", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01ONLY","name":"Only Co","slug":"only","role":"owner","has_active_subscription":true}
		],"count":1}`)
		path := useHomeWithConfig(t, "")

		out := runLogin(t)

		got, err := config.ReadSelectedAccountID(path)
		if err != nil {
			t.Fatalf("ReadSelectedAccountID: %v", err)
		}
		if got != "acct_01ONLY" {
			t.Errorf("selection after login = %q, want acct_01ONLY", got)
		}
		if !strings.Contains(out, "acct_01ONLY") || !strings.Contains(out, "knowledge account use") {
			t.Errorf("login output does not name the account and how to change it:\n%s", out)
		}
	})

	t.Run("multiple memberships take the first", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01OLDEST","name":"Oldest","slug":"oldest","role":"owner","has_active_subscription":true},
			{"id":"acct_01NEWER","name":"Newer","slug":"newer","role":"member","has_active_subscription":true}
		],"count":2}`)
		path := useHomeWithConfig(t, "")

		runLogin(t)

		got, _ := config.ReadSelectedAccountID(path)
		if got != "acct_01OLDEST" {
			t.Errorf("selection after login = %q, want the first (oldest) entry acct_01OLDEST", got)
		}
	})

	t.Run("an unsubscribed account is still recorded, with a warning", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01FREE","name":"Free","slug":"free","role":"owner","has_active_subscription":false}
		],"count":1}`)
		path := useHomeWithConfig(t, "")

		out := runLogin(t)

		got, _ := config.ReadSelectedAccountID(path)
		if got != "acct_01FREE" {
			t.Errorf("selection = %q, want acct_01FREE — login records the account the gateway resolves anyway", got)
		}
		if !strings.Contains(out, "no active subscription") {
			t.Errorf("no warning about the missing subscription:\n%s", out)
		}
	})

	t.Run("zero memberships write nothing", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[],"count":0}`)
		path := useHomeWithConfig(t, "")
		before := readConfig(t, path)

		out := runLogin(t)

		if got, _ := config.ReadSelectedAccountID(path); got != "" {
			t.Errorf("selection = %q, want empty with no memberships", got)
		}
		if after := readConfig(t, path); after != before {
			t.Errorf("the config was rewritten with no memberships:\n%s", after)
		}
		if !strings.Contains(out, "No accounts") {
			t.Errorf("no explicit no-accounts line:\n%s", out)
		}
	})
}

// TestLogin_RevalidatesStoredSelection proves the two revalidation warnings.
func TestLogin_RevalidatesStoredSelection(t *testing.T) {
	t.Run("membership lost", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01OTHER","name":"Other","slug":"other","role":"member","has_active_subscription":true}
		],"count":1}`)
		useHomeWithConfig(t, "acct_01GONE")

		out := runLogin(t)

		if !strings.Contains(out, "no longer one of your accounts") {
			t.Errorf("no lost-membership warning:\n%s", out)
		}
		if !strings.Contains(out, "knowledge accounts") || !strings.Contains(out, "knowledge account use") {
			t.Errorf("the warning does not name the remedy:\n%s", out)
		}
	})

	t.Run("subscription lost", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01LAPSED","name":"Lapsed","slug":"lapsed","role":"owner","has_active_subscription":false}
		],"count":1}`)
		useHomeWithConfig(t, "acct_01LAPSED")

		out := runLogin(t)

		if !strings.Contains(out, "has no active subscription") {
			t.Errorf("no lost-subscription warning:\n%s", out)
		}
		if !strings.Contains(out, "no cloud graph access") {
			t.Errorf("the warning does not state the consequence:\n%s", out)
		}
	})

	t.Run("a healthy selection warns about nothing", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01FINE","name":"Fine","slug":"fine","role":"owner","has_active_subscription":true}
		],"count":1}`)
		useHomeWithConfig(t, "acct_01FINE")

		out := runLogin(t)

		if strings.Contains(out, "Warning") {
			t.Errorf("a healthy selection produced a warning:\n%s", out)
		}
	})
}

// TestLogin_RevalidationNeverFailsLoginOrWrites proves a broken accounts
// endpoint leaves the login successful and the stored selection untouched.
func TestLogin_RevalidationNeverFailsLoginOrWrites(t *testing.T) {
	loginHarness(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	t.Cleanup(srv.Close)
	prior := buildSyncTransportFn
	buildSyncTransportFn = func() (*auth.Transport, error) {
		return auth.NewSyncTransport(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}), nil
	}
	t.Cleanup(func() { buildSyncTransportFn = prior })

	path := useHomeWithConfig(t, "acct_01KEEP")
	before := readConfig(t, path)

	out, err := captureStdout(t, func() error { return loginBrowserPKCE(context.Background()) })
	if err != nil {
		t.Fatalf("a 500 from the accounts endpoint failed the login: %v", err)
	}
	if !strings.Contains(out, "Logged in.") {
		t.Errorf("login did not report success:\n%s", out)
	}
	if strings.Contains(out, "Warning") {
		t.Errorf("an unreachable endpoint produced a warning about the account:\n%s", out)
	}
	if got, _ := config.ReadSelectedAccountID(path); got != "acct_01KEEP" {
		t.Errorf("selection = %q, want it unchanged", got)
	}
	if after := readConfig(t, path); after != before {
		t.Errorf("the config was rewritten despite the failed check:\n%s", after)
	}
}

// TestLogin_NeverOverwritesExistingSelection proves login only ever ESTABLISHES
// a selection, never replaces or clears one — including when the stored account
// has lost its membership or its subscription.
func TestLogin_NeverOverwritesExistingSelection(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "stored account still healthy",
			body: `{"accounts":[
				{"id":"acct_01MINE","name":"Mine","slug":"mine","role":"owner","has_active_subscription":true},
				{"id":"acct_01FIRST","name":"First","slug":"first","role":"owner","has_active_subscription":true}
			],"count":2}`,
		},
		{
			name: "stored account lost its membership",
			body: `{"accounts":[
				{"id":"acct_01FIRST","name":"First","slug":"first","role":"owner","has_active_subscription":true}
			],"count":1}`,
		},
		{
			name: "stored account lost its subscription",
			body: `{"accounts":[
				{"id":"acct_01MINE","name":"Mine","slug":"mine","role":"owner","has_active_subscription":false},
				{"id":"acct_01FIRST","name":"First","slug":"first","role":"owner","has_active_subscription":true}
			],"count":2}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loginHarness(t)
			serveAccounts(t, tc.body)
			path := useHomeWithConfig(t, "acct_01MINE")

			runLogin(t)

			got, err := config.ReadSelectedAccountID(path)
			if err != nil {
				t.Fatalf("ReadSelectedAccountID: %v", err)
			}
			if got != "acct_01MINE" {
				t.Errorf("selection = %q, want the user's stored acct_01MINE — login must neither overwrite nor clear it", got)
			}
		})
	}

	// Known-positive control: the SAME harness DOES establish a selection when
	// none is stored, so the never-overwritten results above are a real
	// distinction rather than a write path that never fires.
	t.Run("control: an absent selection IS established", func(t *testing.T) {
		loginHarness(t)
		serveAccounts(t, `{"accounts":[
			{"id":"acct_01FIRST","name":"First","slug":"first","role":"owner","has_active_subscription":true}
		],"count":1}`)
		path := useHomeWithConfig(t, "")

		runLogin(t)

		if got, _ := config.ReadSelectedAccountID(path); got != "acct_01FIRST" {
			t.Errorf("control: selection = %q, want acct_01FIRST", got)
		}
	})
}
