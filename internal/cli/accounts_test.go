// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// serveAccounts stands up a fake /v1/me/accounts endpoint returning body, and
// points the package's transport seam at it for the duration of the test.
func serveAccounts(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/accounts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prior := buildSyncTransportFn
	buildSyncTransportFn = func() (*auth.Transport, error) {
		return auth.NewSyncTransport(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}), nil
	}
	t.Cleanup(func() { buildSyncTransportFn = prior })
}

// TestAccountsCmd_RendersEveryFieldAndUsability proves every contract field
// reaches the output and that the usability marking discriminates: an
// unsubscribed account is UNAVAILABLE while a subscribed one is available.
func TestAccountsCmd_RendersEveryFieldAndUsability(t *testing.T) {
	serveAccounts(t, `{"accounts":[
		{"id":"acct_01SUBBED","name":"Acme Inc","slug":"acme","role":"owner","has_active_subscription":true},
		{"id":"acct_01FREE","name":"Hobby Co","slug":"hobby","role":"member","has_active_subscription":false}
	],"count":2}`)

	out, err := captureStdout(t, func() error { return AccountsCmd(nil) })
	if err != nil {
		t.Fatalf("AccountsCmd: %v", err)
	}

	for _, want := range []string{
		"acct_01SUBBED", "Acme Inc", "acme", "owner",
		"acct_01FREE", "Hobby Co", "hobby", "member",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per account, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "cloud graph: available") || strings.Contains(lines[0], "UNAVAILABLE") {
		t.Errorf("subscribed account not marked available: %q", lines[0])
	}
	if !strings.Contains(lines[1], "UNAVAILABLE (no active subscription)") {
		t.Errorf("unsubscribed account not marked UNAVAILABLE with a reason: %q", lines[1])
	}
}

// TestAccountsCmd_EmptyListIsNotAnError pins the zero-membership case: an
// explicit line, and no error.
func TestAccountsCmd_EmptyListIsNotAnError(t *testing.T) {
	serveAccounts(t, `{"accounts":[],"count":0}`)

	out, err := captureStdout(t, func() error { return AccountsCmd(nil) })
	if err != nil {
		t.Fatalf("AccountsCmd with zero memberships must not error: %v", err)
	}
	if !strings.Contains(out, "No accounts") {
		t.Errorf("zero memberships must print an explicit line, got %q", out)
	}
}
