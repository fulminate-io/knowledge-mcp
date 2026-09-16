// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestPlatformRequest_StatusIsNotAnError pins the one way PlatformRequest
// differs from every other method in management.go: the platform's refusal
// reaches the caller as a status, because the Desktop shell's own signed-out
// state is what must render on a 401.
func TestPlatformRequest_StatusIsNotAnError(t *testing.T) {
	var seen *http.Request
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	// The selection is bound to a scratch path this test seeded, so the header
	// assertion below is a statement about that file and cannot read the
	// operator's own account selection.
	tr := NewSyncTransport(server.URL, StaticTokenSource{AccessToken: "platform-test"},
		WithAccountSelection(NewAccountSelection(seedSelection(t, t.TempDir(), "account-A"), time.Second)))
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		status = code
		got, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: "/v1/accounts/account-A/profiles"})
		if err != nil {
			t.Errorf("status %d surfaced as an error: %v", code, err)
			continue
		}
		if got.Status != code || string(got.Body) != `{"ok":true}` {
			t.Errorf("PlatformRequest() = %d %s, want status %d and the body verbatim", got.Status, got.Body, code)
		}
	}
	if seen.Header.Get("Authorization") == "" {
		t.Error("no bearer was attached to the platform request")
	}
	// The header comes from the selection this transport was given, not from
	// the account argument: one mechanism, so the stamped account and the
	// account the proxy asserted cannot disagree.
	if seen.Header.Get(AccountHeaderName) != "account-A" {
		t.Errorf("account header not stamped from the bound selection: %q", seen.Header.Get(AccountHeaderName))
	}
	if seen.Header.Get("Accept") != jsonAccept {
		t.Errorf("Accept not %q: %q", jsonAccept, seen.Header.Get("Accept"))
	}
}

// TestPlatformRequest_RefusesMalformedPath pins the second wall under the
// Desktop proxy's template allowlist: this method validates the path it is
// given rather than trusting its caller, and it issues nothing when it refuses.
func TestPlatformRequest_RefusesMalformedPath(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	// The selection is bound to an empty scratch path, so a caller-scoped
	// request in this test cannot read the operator's own account selection.
	tr := NewSyncTransport(server.URL, StaticTokenSource{AccessToken: "platform-test"},
		WithAccountSelection(NewAccountSelection(seedSelection(t, t.TempDir(), ""), time.Second)))
	for _, path := range []string{"", "v1/accounts", "/v1/../v1/accounts", "/v1//accounts", "/v1/accounts/a/../b"} {
		if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: path}); err == nil {
			t.Errorf("PlatformRequest accepted path %q", path)
		}
	}
	for _, account := range []string{"../account", "account/../b", "-leading"} {
		if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: account, Method: http.MethodGet, Path: "/auth/me"}); err == nil {
			t.Errorf("PlatformRequest accepted account %q", account)
		}
	}
	if calls != 0 {
		t.Fatalf("a refused PlatformRequest still issued %d requests", calls)
	}
	// The same-run known positive: the instrument that counted zero counts a
	// request for a well-formed call.
	if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Method: http.MethodGet, Path: "/auth/me"}); err != nil || calls != 1 {
		t.Fatalf("known positive did not reach the server: %v (calls %d)", err, calls)
	}
	// An empty account stamps NO account header, which is what lets /auth/me
	// answer for a user whose selection the gateway has rejected.
	if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Method: http.MethodGet, Path: "/auth/me"}); err != nil {
		t.Fatalf("caller-scoped request refused: %v", err)
	}
	// THE PATH GUARD STILL REFUSES WHAT IT REFUSED, with a query present as
	// well as absent: the query moved out of the path, and the guard did not
	// move with it.
	for _, path := range []string{"/v1/../v1/accounts", "/v1//accounts"} {
		if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: path, Query: "severity=info"}); err == nil {
			t.Errorf("PlatformRequest(%q with a query) accepted the path", path)
		}
	}
}

// TestPlatformRequest_QueryIsJoinedAfterThePathGuard pins the reason the query
// is a separate value rather than part of the path.
//
// The guard above refuses any path containing ".." or "//", and a URL encoder
// does not escape a dot — so a filter value carrying two dots, which the
// website's own audit-log export accepts as free user text, would be refused as
// a malformed PATH if it rode the path argument. The query is therefore joined
// after the guard has run, and this test is what says so: the same value that
// is refused in the path is delivered verbatim in the query.
func TestPlatformRequest_QueryIsJoinedAfterThePathGuard(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	tr := NewSyncTransport(server.URL, StaticTokenSource{AccessToken: "platform-test"},
		WithAccountSelection(NewAccountSelection(seedSelection(t, t.TempDir(), "account-A"), time.Second)))
	query := url.Values{"actor_email": {"../ada@example.com"}, "end_time": {"2026-09-13T23:59:59Z"}, "path": {"a//b"}}
	if _, err := tr.PlatformRequest(t.Context(), PlatformCall{
		Account: "account-A",
		Method:  http.MethodGet,
		Path:    "/v1/accounts/account-A/audit-logs/export",
		Query:   query.Encode(),
	}); err != nil {
		t.Fatalf("PlatformRequest(a query carrying two dots) errored: %v", err)
	}
	if seen.URL.Path != "/v1/accounts/account-A/audit-logs/export" {
		t.Errorf("PlatformRequest() sent path %q, want the path unchanged by the join", seen.URL.Path)
	}
	for key, want := range map[string]string{"actor_email": "../ada@example.com", "end_time": "2026-09-13T23:59:59Z", "path": "a//b"} {
		if got := seen.URL.Query().Get(key); got != want {
			t.Errorf("PlatformRequest() delivered %s=%q, want %q (raw %q)", key, got, want, seen.URL.RawQuery)
		}
	}
	// THE QUERY IS BOUNDED TOO, by the one property an encoder guarantees: its
	// output carries no unescaped delimiter. A query naming a second query or a
	// fragment was built by hand rather than encoded, and this is the second
	// wall under the proxy's own encoder.
	for _, malformed := range []string{"a=b?c=d", "a=b#frag"} {
		if _, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: "/auth/me", Query: malformed}); err == nil {
			t.Errorf("PlatformRequest(query %q) accepted it", malformed)
		}
	}
}

// TestPlatformRequest_ReturnsTheContentType pins the result the Desktop proxy's
// response arm is selected from. This method used to throw the *http.Response
// away, so the content type had no source and every non-JSON answer became a
// failure code at the proxy.
func TestPlatformRequest_ReturnsTheContentType(t *testing.T) {
	declared := "application/json"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if declared != "" {
			w.Header().Set("Content-Type", declared)
		} else {
			// The nil entry suppresses net/http's sniffing, so the absent-header
			// row observes an absent header rather than the sniffer's guess.
			w.Header()["Content-Type"] = nil
		}
		_, _ = w.Write([]byte("a,b\n1,2\n"))
	}))
	defer server.Close()
	tr := NewSyncTransport(server.URL, StaticTokenSource{AccessToken: "platform-test"},
		WithAccountSelection(NewAccountSelection(seedSelection(t, t.TempDir(), "account-A"), time.Second)))
	for _, want := range []string{"application/json", "text/csv", "text/csv; charset=utf-8", "application/pdf", ""} {
		declared = want
		got, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: "/v1/accounts/account-A/audit-logs/export"})
		if err != nil {
			t.Errorf("PlatformRequest(%q) errored: %v", want, err)
			continue
		}
		if got.ContentType != want {
			t.Errorf("PlatformRequest().ContentType = %q, want %q", got.ContentType, want)
		}
	}
}

// TestPlatformRequest_OversizeIsASentinel pins that an oversize response is
// distinguishable from a transport failure. The Desktop proxy has to report it
// as its own named outcome: reported as an unreachable platform, a too-large
// answer would tell the user the service is down.
func TestPlatformRequest_OversizeIsASentinel(t *testing.T) {
	size := PlatformResponseLimit + 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("c"), size))
	}))
	defer server.Close()
	tr := NewSyncTransport(server.URL, StaticTokenSource{AccessToken: "platform-test"},
		WithAccountSelection(NewAccountSelection(seedSelection(t, t.TempDir(), "account-A"), time.Second)))
	_, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: "/v1/accounts/account-A/audit-logs/export"})
	if !errors.Is(err, ErrPlatformResponseTooLarge) {
		t.Errorf("PlatformRequest(a body over the limit) = %v, want ErrPlatformResponseTooLarge", err)
	}
	// The same-run known positive: a body AT the limit is not refused, so the
	// sentinel above is the bound's answer and not every answer.
	size = PlatformResponseLimit
	got, err := tr.PlatformRequest(t.Context(), PlatformCall{Account: "account-A", Method: http.MethodGet, Path: "/v1/accounts/account-A/audit-logs/export"})
	if err != nil {
		t.Fatalf("PlatformRequest(a body at the limit) errored: %v", err)
	}
	if len(got.Body) != PlatformResponseLimit {
		t.Errorf("PlatformRequest(a body at the limit) read %d bytes, want %d", len(got.Body), PlatformResponseLimit)
	}
}
