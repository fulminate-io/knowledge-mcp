// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// platformFixture stands the proxy up against a stub platform: a memory
// credential store holding a token-shaped value, a selected account, and an
// httptest server recording what each request carried.
type platformFixture struct {
	store *fakeAuthStore
	path  string
	calls []*http.Request
	// queries records each request's RAW query string, read off the *http.Request
	// before the handler returns: the recorded request's URL is what the
	// platform saw, and this is the field every query row asserts on.
	queries []string
	status  int
	body    string
	// contentType is the Content-Type the stub platform declares. It is a field
	// rather than a sniffed default because the response ARM is selected from
	// it: left to net/http's sniffer, a CSV fixture would be served as
	// text/plain and a non-JSON fixture body would decide its own arm.
	contentType string
	bearers     []string
	selection   *auth.AccountSelection
	builtWith   []string
}

func newPlatformFixture(t *testing.T, account string) *platformFixture {
	t.Helper()
	f := &platformFixture{store: withMemoryStore(t), status: http.StatusOK, body: `{"name":"Ada"}`, contentType: "application/json"}
	ctx := t.Context()
	// A token-shaped value, asserted only by set-or-unset and by refusal: no
	// assertion in this file compares or prints a credential value.
	if err := f.store.Set(ctx, auth.KeyRefreshToken, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(t.TempDir(), "config")
	if account != "" {
		if err := config.WriteSelectedAccountID(f.path, account); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r)
		f.queries = append(f.queries, r.URL.RawQuery)
		f.bearers = append(f.bearers, r.Header.Get("Authorization"))
		// An EMPTY contentType declares no header at all, which is what a 204
		// answer looks like. The nil map entry is what suppresses net/http's
		// own sniffing: left to it, "no content type" would arrive as the
		// sniffer's guess (text/plain for most bodies) and no fixture here
		// could observe an absent one.
		if f.contentType != "" {
			w.Header().Set("Content-Type", f.contentType)
		} else {
			w.Header()["Content-Type"] = nil
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(server.Close)
	original := desktopPlatformTransport
	t.Cleanup(func() { desktopPlatformTransport = original })
	// The selection comes from desktopPlatformSelection, the production
	// binding, applied to the configuration path the command was given — so no
	// assertion here can read the operator's own account selection, and the
	// double does not invent a selection the production wiring does not have.
	// It is built ONCE and reused across requests, because a selection built
	// per request would lose a rejection the gateway recorded on the last one,
	// which is a state one test here drives deliberately.
	f.selection = desktopPlatformSelection(f.path)
	// The double RECORDS the configuration path it was built against; the
	// assertion on it lives in the Test function, because a helper that fails
	// the test reports a correctness failure away from the test that gives it
	// meaning.
	desktopPlatformTransport = func(_ auth.Store, configPath string) *auth.Transport {
		f.builtWith = append(f.builtWith, configPath)
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "test-native"},
			auth.WithAccountSelection(f.selection))
	}
	return f
}

func (f *platformFixture) run(t *testing.T, request string) desktopPlatformResult {
	t.Helper()
	return runDesktopPlatform(t.Context(), f.path, strings.NewReader(request))
}

// TestDesktopPlatformAdmittedShape pins the structural wall this side
// enforces. Owner decision 2026-09-13: the proxy admits every platform path the
// website's own API modules declare, and nothing else; the closed derived set
// is enforced in the Desktop, over a module generated from the schema that
// defines it, and this side admits the SHAPE such a path can take.
//
// So the admitted rows below are templates the website's schema does declare,
// and the refused rows are shapes no schema path can be.
func TestDesktopPlatformAdmittedShape(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	admitted := []struct {
		method  string
		request string
		path    string
	}{
		{method: "GET", request: `{"method":"GET","path":"/auth/me","params":{}}`, path: "/auth/me"},
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"}}`, path: "/v1/accounts/account-A/profiles"},
		{method: "POST", request: `{"method":"POST","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"},"body":{"name":"p"}}`, path: "/v1/accounts/account-A/profiles"},
		{method: "PUT", request: `{"method":"PUT","path":"/v1/accounts/{account_id}/profiles/{profile_id}","params":{"account_id":"account-A","profile_id":"prof-1"},"body":{"name":"p"}}`, path: "/v1/accounts/account-A/profiles/prof-1"},
		{method: "DELETE", request: `{"method":"DELETE","path":"/v1/accounts/{account_id}/profiles/{profile_id}","params":{"account_id":"account-A","profile_id":"prof-1"}}`, path: "/v1/accounts/account-A/profiles/prof-1"},
		{method: "POST", request: `{"method":"POST","path":"/v1/accounts/{account_id}/profiles/{profile_id}/clone","params":{"account_id":"account-A","profile_id":"prof-1"}}`, path: "/v1/accounts/account-A/profiles/prof-1/clone"},
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/transcript-consent","params":{"account_id":"account-A"}}`, path: "/v1/accounts/account-A/transcript-consent"},
		{method: "PUT", request: `{"method":"PUT","path":"/v1/accounts/{account_id}/transcript-mandate","params":{"account_id":"account-A"},"body":{"required":true}}`, path: "/v1/accounts/account-A/transcript-mandate"},
		{method: "GET", request: `{"method":"GET","path":"/v1/system/config","params":{}}`, path: "/v1/system/config"},
		// The areas the seven-template allowlist refused and the parity
		// program needs: usage, providers, dev environments, volumes, audit,
		// billing, team roles, credits and the account list itself.
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/usage","params":{"account_id":"account-A"}}`, path: "/v1/accounts/account-A/usage"},
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/dev-vm","params":{"account_id":"account-A"}}`, path: "/v1/accounts/account-A/dev-vm"},
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"}}`, path: "/v1/accounts/account-A/audit-logs/export"},
		{method: "PATCH", request: `{"method":"PATCH","path":"/v1/accounts/{account_id}/roles/{role_id}","params":{"account_id":"account-A","role_id":"role-1"},"body":{"name":"r"}}`, path: "/v1/accounts/account-A/roles/role-1"},
		{method: "POST", request: `{"method":"POST","path":"/v1/accounts","params":{},"body":{"name":"New"}}`, path: "/v1/accounts"},
		// A segment carrying a dot or an at sign, which platform identifiers do.
		{method: "GET", request: `{"method":"GET","path":"/v1/accounts/{account_id}/members/{email}","params":{"account_id":"account-A","email":"ada@example.com"}}`, path: "/v1/accounts/account-A/members/ada@example.com"},
	}
	for _, a := range admitted {
		before := len(f.calls)
		got := f.run(t, a.request)
		if got.Code != "" || got.Status != http.StatusOK {
			t.Errorf("admitted %s %s refused: %+v", a.method, a.path, got)
			continue
		}
		if len(f.calls) != before+1 {
			t.Errorf("admitted %s %s issued %d requests", a.method, a.path, len(f.calls)-before)
			continue
		}
		issued := f.calls[len(f.calls)-1]
		if issued.URL.Path != a.path || issued.Method != a.method {
			t.Errorf("admitted %s %s issued %s %s", a.method, a.path, issued.Method, issued.URL.Path)
		}
	}
	// REFUSED: shapes no path the website's schema declares can have. The
	// navigations are the load-bearing ones — the website reaches /auth/logout
	// and /auth/login by navigating, this window cancels navigations, and the
	// Desktop's own adapter performs the native logout instead.
	refused := []string{
		`{"method":"GET","path":"/auth/logout","params":{}}`,
		`{"method":"GET","path":"/auth/login","params":{}}`,
		`{"method":"GET","path":"/auth/me/","params":{}}`,
		`{"method":"GET","path":"/auth","params":{}}`,
		`{"method":"GET","path":"/internal/metrics","params":{}}`,
		`{"method":"GET","path":"v1/accounts","params":{}}`,
		`{"method":"GET","path":"/v2/accounts","params":{}}`,
		`{"method":"GET","path":"/v1//accounts","params":{}}`,
		`{"method":"GET","path":"/v1/../v1/accounts","params":{}}`,
		`{"method":"GET","path":"/v1/accounts?download=true","params":{}}`,
		`{"method":"GET","path":"/v1/accounts#fragment","params":{}}`,
		`{"method":"GET","path":"/v1/accounts/{account_id}/profiles/../../secrets","params":{"account_id":"account-A"}}`,
		`{"method":"GET","path":"","params":{}}`,
	}
	for _, request := range refused {
		before := len(f.calls)
		got := f.run(t, request)
		if got.Code != "path_not_allowed" {
			t.Errorf("off-shape request %s not refused: %+v", request, got)
		}
		if len(f.calls) != before {
			t.Errorf("off-shape request %s reached the platform", request)
		}
	}
}

// TestDesktopPlatformRequestBoundary pins the request allowlist: the fields
// desktopPlatformRequest declares and nothing else, no credential-carrying
// field on any spelling, and a bound on size.
func TestDesktopPlatformRequestBoundary(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	rejected := []string{
		`{"method":"GET","path":"/auth/me","params":{},"url":"https://evil.example"}`,
		`{"method":"GET","path":"/auth/me","params":{},"host":"evil.example"}`,
		`{"method":"GET","path":"/auth/me","params":{},"headers":{"Authorization":"Bearer x"}}`,
		`{"method":"GET","path":"/auth/me","params":{},"token":"x"}`,
		`{"method":"GET","path":"/auth/me","params":{},"Authorization":"Bearer x"}`,
		`{"method":"GET","path":"/auth/me","params":{},"authorization":"Bearer x"}`,
		`{"method":"GET","path":"/auth/me","params":{},"unknown":1}`,
		// The METHOD VOCABULARY. PATCH is admitted since the website's api
		// modules call it, so the refusals here are the spellings outside the
		// enum; whether a given path accepts a given method is the DERIVED
		// set's business, enforced Desktop-side and by the platform's own 405.
		`{"method":"get","path":"/auth/me","params":{}}`,
		`{"method":"HEAD","path":"/auth/me","params":{}}`,
		`{"method":"OPTIONS","path":"/auth/me","params":{}}`,
		`{"method":"GET","path":"/auth/me","params":{},"body":{"x":1}}`,
		`{"method":"DELETE","path":"/v1/accounts/{account_id}/profiles/{profile_id}","params":{"account_id":"account-A","profile_id":"../account-B/profiles"}}`,
		`{"method":"DELETE","path":"/v1/accounts/{account_id}/profiles/{profile_id}","params":{"account_id":"account-A","profile_id":""}}`,
		`{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A","profile_id":"prof-1"}}`,
		`{"method":"GET","path":"/auth/me","params":{"account_id":"account-A"}}`,
		`{"method":"GET","path":"/auth/me"}{"method":"GET","path":"/auth/me"}`,
		`{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"` + strings.Repeat("a", 70000) + `"}}`,
		``,
	}
	// Exactly one byte over the bound, WELL-FORMED at that length, and
	// admissible on every other axis — an allowlisted path, an admitted
	// method, an exact param set, a body a write accepts — so the BOUND is the
	// only thing that can refuse it. A fixture that also fails the param
	// vocabulary would leave the bound unobserved. The padding is computed
	// from the envelope, never a literal.
	envelope := `{"method":"POST","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"},"body":{"name":""}}`
	oversize := strings.Replace(envelope, `"name":""`, `"name":"`+strings.Repeat("a", 65537-len(envelope))+`"`, 1)
	if len(oversize) != 65537 {
		t.Fatalf("oversize fixture is %d bytes, not the 65537 the bound refuses", len(oversize))
	}
	rejected = append(rejected, oversize)
	for _, request := range rejected {
		before := len(f.calls)
		got := f.run(t, request)
		if got.Code != "invalid_request" {
			t.Errorf("request %.80q not refused as invalid_request: %+v", request, got)
		}
		if len(f.calls) != before {
			t.Errorf("request %.80q reached the platform", request)
		}
	}
	// The same-run known positive: the instrument that recorded zero calls
	// above records a call for an admitted request.
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" || len(f.calls) == 0 {
		t.Fatalf("known positive did not reach the platform: %+v (calls %d)", got, len(f.calls))
	}
}

// TestDesktopPlatformAccountScoping pins that account scoping is ASSERTED
// against the native selection rather than trusted from the request.
func TestDesktopPlatformAccountScoping(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	for _, request := range []string{
		`{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-B"}}`,
		`{"method":"PUT","path":"/v1/accounts/{account_id}/transcript-consent","params":{"account_id":"account-B"},"body":{"granted":true}}`,
	} {
		before := len(f.calls)
		got := f.run(t, request)
		if got.Code != "invalid_request" {
			t.Errorf("cross-account request %s not refused: %+v", request, got)
		}
		if len(f.calls) != before {
			t.Error("cross-account request reached the platform")
		}
	}
	got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"}}`)
	if got.Code != "" || len(f.calls) != 1 {
		t.Fatalf("selected-account request refused: %+v (calls %d)", got, len(f.calls))
	}
	if f.calls[0].Header.Get(auth.AccountHeaderName) != "account-A" {
		t.Errorf("account header not stamped: %q", f.calls[0].Header.Get(auth.AccountHeaderName))
	}
	// A CALLER-scoped route carries no {account_id}, so its header comes from
	// the selection — and that selection must be the one in the configuration
	// file the main process handed us, not the process-wide default. A literal
	// account id here is what reds if the proxy falls back to the default path.
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" {
		t.Fatalf("caller-scoped read refused: %+v", got)
	}
	if header := f.calls[1].Header.Get(auth.AccountHeaderName); header != "account-A" {
		t.Errorf("caller-scoped read stamped %q, not the Desktop's own selection", header)
	}
	// Every transport this command built was built against the configuration
	// path it was handed, which is what makes the two header assertions above
	// statements about that file rather than about this process.
	if len(f.builtWith) == 0 {
		t.Fatal("no transport was built, so the path assertion below would pass vacuously")
	}
	for _, built := range f.builtWith {
		if built != f.path {
			t.Errorf("the proxy built its transport against %q, not the configuration path it was given (%q)", built, f.path)
		}
	}
}

// TestDesktopPlatformNoSelectedAccount pins the arm where a signed-in user has
// no account selected: an account-scoped path has no scope to assert against
// and is refused, while the caller-scoped identity read still answers and
// stamps NO account header.
func TestDesktopPlatformNoSelectedAccount(t *testing.T) {
	f := newPlatformFixture(t, "")
	got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"}}`)
	if got.Code != "invalid_request" {
		t.Errorf("account-scoped path admitted with no selection: %+v", got)
	}
	if len(f.calls) != 0 {
		t.Error("account-scoped path with no selection reached the platform")
	}
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" || len(f.calls) != 1 {
		t.Fatalf("caller-scoped identity read refused with no selection: %+v (calls %d)", got, len(f.calls))
	}
	if header := f.calls[0].Header.Get(auth.AccountHeaderName); header != "" {
		t.Errorf("caller-scoped read stamped an account header: %q", header)
	}
}

// TestDesktopPlatformSelectionBinding pins the binding itself: the proxy's
// account selection reads the configuration path it was HANDED. The production
// transport factory is wiring a test double replaces wholesale, so the binding
// is pinned here, at the named function both of them call.
func TestDesktopPlatformSelectionBinding(t *testing.T) {
	handed := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(handed, "account-handed"); err != nil {
		t.Fatal(err)
	}
	if got := desktopPlatformSelection(handed).ID(t.Context()); got != "account-handed" {
		t.Errorf("desktopPlatformSelection(%q).ID() = %q, want the account that path selects", handed, got)
	}
	// A path selecting nothing yields nothing, which is what makes the
	// assertion above a statement about the path rather than about whatever
	// selection this process happens to hold.
	empty := filepath.Join(t.TempDir(), "config")
	if got := desktopPlatformSelection(empty).ID(t.Context()); got != "" {
		t.Errorf("desktopPlatformSelection(%q).ID() = %q, want empty for a path with no selection", empty, got)
	}
}

// TestDesktopPlatformRejectedSelection pins that a selection the gateway has
// already rejected is refused locally on an account-scoped route, and bypassed
// on the caller-scoped identity read — the one route a user must keep after
// their account selection stops working.
func TestDesktopPlatformRejectedSelection(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" {
		t.Fatalf("identity read refused before the rejection: %+v", got)
	}
	f.selection.MarkInvalid("account-A", "not a member")
	before := len(f.calls)
	got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/profiles","params":{"account_id":"account-A"}}`)
	if got.Code != "unauthenticated" {
		t.Errorf("account-scoped route on a rejected selection: %+v, want unauthenticated", got)
	}
	if len(f.calls) != before {
		t.Error("a rejected selection still round-tripped to the platform")
	}
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" || len(f.calls) != before+1 {
		t.Errorf("identity read refused on a rejected selection: %+v (calls %d)", got, len(f.calls))
	}
}

// TestDesktopPlatformStatusPassthrough pins that a platform refusal reaches the
// renderer AS a status, not as an error code: the shell's own signed-out state
// is what must render, and a collapsed 401 would leave it a failed call.
func TestDesktopPlatformStatusPassthrough(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		f.status = status
		f.body = `{"error":"refused"}`
		got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
		if got.Code != "" || got.State != "" || got.Status != status {
			t.Errorf("status %d not passed through: %+v", status, got)
		}
		if string(got.Body) != f.body {
			t.Errorf("status %d body not verbatim: %s", status, got.Body)
		}
	}
	// Two consecutive rejections both report, so nothing sticky swallows the
	// second — the shape the renderer's own sticky-guard defect would hide.
	f.status = http.StatusUnauthorized
	first := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
	second := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
	if first.Status != http.StatusUnauthorized || second.Status != http.StatusUnauthorized {
		t.Errorf("consecutive rejections not both reported: %+v %+v", first, second)
	}
}

// TestDesktopPlatformMalformedPlatformBody pins that a platform answer this
// command cannot vouch for is reported as a code rather than handed on: the
// renderer parses the body, so a non-JSON body would be its crash.
func TestDesktopPlatformMalformedPlatformBody(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	f.body = "<html>gateway</html>"
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "platform_error" {
		t.Errorf("non-JSON platform body not reported as platform_error: %+v", got)
	}
	// An empty body is a legitimate answer (a 204), and it reaches the
	// renderer as JSON null rather than as a failure.
	f.body = ""
	f.status = http.StatusNoContent
	got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
	if got.Code != "" || got.Status != http.StatusNoContent || string(got.Body) != "null" {
		t.Errorf("empty platform body not carried as null: %+v", got)
	}
}

// TestDesktopPlatformSignedOut pins the fail-closed arm: with no credential the
// proxy reports unauthenticated and spawns no request at all.
func TestDesktopPlatformSignedOut(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	if err := f.store.Delete(t.Context(), auth.KeyRefreshToken); err != nil {
		t.Fatal(err)
	}
	got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
	if got.Code != "unauthenticated" || got.State != "error" {
		t.Errorf("signed-out request not refused closed: %+v", got)
	}
	if len(f.calls) != 0 {
		t.Error("signed-out request reached the platform")
	}
	// The same-run known positive through the same instrument.
	if err := f.store.Set(t.Context(), auth.KeyRefreshToken, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`); got.Code != "" || len(f.calls) != 1 {
		t.Fatalf("known positive did not reach the platform: %+v (calls %d)", got, len(f.calls))
	}
}

// TestDesktopPlatformCmdArguments pins the argv contract: one action, an
// absolute configuration path, nothing else.
func TestDesktopPlatformCmdArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"perform", "--config-file", "/tmp/config"},
		{"request"},
		{"request", "--config-file", "relative/config"},
		{"request", "--config-file", "/tmp/config", "extra"},
		{"request", "--config-file", "/tmp/config", "--token", "x"},
	} {
		if err := DesktopPlatformCmd(args); err == nil {
			t.Errorf("DesktopPlatformCmd accepted %v", args)
		}
	}
}
