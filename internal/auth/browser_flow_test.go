// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestRunBrowserPKCEFlow_HappyPath drives the full browser flow with a
// fake "browser" that immediately hits the loopback callback URL.
// Asserts the token exchange POSTs the expected PKCE parameters and the
// returned TokenResponse propagates back to the caller.
func TestRunBrowserPKCEFlow_HappyPath(t *testing.T) {
	// Token-exchange server: validates form params, returns a fake JWT.
	var seenForm url.Values
	tokenSrv := newTokenSrv(t, &seenForm, "access.jwt.token", "frt_new", 3600)
	defer tokenSrv.Close()

	endpoints := &DiscoveredEndpoints{
		Resource:              "https://fulminate.io/mcp",
		Issuer:                "https://auth.fulminate.io",
		AuthorizationEndpoint: "https://auth.fulminate.io/oauth2/authorize",
		TokenEndpoint:         tokenSrv.URL,
	}

	// Fake openBrowserFn — instead of forking `open`, immediately drive
	// the loopback listener by parsing the AuthKit authorize URL the
	// browser was asked to open and re-issuing the redirect ourselves.
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	openBrowserFn = func(authorizeURL string) error {
		u, err := url.Parse(authorizeURL)
		if err != nil {
			t.Errorf("authorize URL malformed: %v", err)
			return err
		}
		q := u.Query()
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			t.Errorf("redirect_uri malformed: %v", err)
			return err
		}
		rq := url.Values{}
		rq.Set("code", "auth_code_xyz")
		rq.Set("state", q.Get("state"))
		redirect.RawQuery = rq.Encode()
		go func() {
			// brief pause so the listener.Accept loop is ready
			time.Sleep(20 * time.Millisecond)
			resp, _ := http.Get(redirect.String())
			if resp != nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr, err := RunBrowserPKCEFlow(ctx, endpoints, "knowledge-cli",
		[]string{PermMCPKnowledgeRead, PermMCPKnowledgeWrite})
	if err != nil {
		t.Fatalf("RunBrowserPKCEFlow: %v", err)
	}
	if tr.AccessToken != "access.jwt.token" {
		t.Errorf("access_token mismatch: %q", tr.AccessToken)
	}
	if seenForm.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type: %q", seenForm.Get("grant_type"))
	}
	if seenForm.Get("code") != "auth_code_xyz" {
		t.Errorf("code: %q", seenForm.Get("code"))
	}
	if seenForm.Get("code_verifier") == "" {
		t.Error("code_verifier missing in token exchange")
	}
	if seenForm.Get("resource") != endpoints.Resource {
		t.Errorf("resource mismatch: %q", seenForm.Get("resource"))
	}
}

// TestRunBrowserPKCEFlow_StateMismatch asserts a callback whose state
// doesn't match the one the flow generated is rejected.
func TestRunBrowserPKCEFlow_StateMismatch(t *testing.T) {
	tokenSrv := newTokenSrv(t, nil, "x", "y", 1)
	defer tokenSrv.Close()
	endpoints := &DiscoveredEndpoints{
		AuthorizationEndpoint: "https://example.test/authorize",
		TokenEndpoint:         tokenSrv.URL,
	}
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	openBrowserFn = func(authorizeURL string) error {
		u, _ := url.Parse(authorizeURL)
		redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
		rq := url.Values{}
		rq.Set("code", "c")
		rq.Set("state", "WRONG-STATE-NOT-GENERATED-BY-FLOW")
		redirect.RawQuery = rq.Encode()
		go func() {
			time.Sleep(20 * time.Millisecond)
			resp, _ := http.Get(redirect.String())
			if resp != nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := RunBrowserPKCEFlow(ctx, endpoints, "cli", nil)
	if err == nil {
		t.Fatal("expected state-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunBrowserPKCEFlow_BrowserUnavailable asserts an openBrowser exec
// failure is surfaced as ErrBrowserUnavailable so the CLI prints the
// "browser required" message.
func TestRunBrowserPKCEFlow_BrowserUnavailable(t *testing.T) {
	endpoints := &DiscoveredEndpoints{
		AuthorizationEndpoint: "https://example.test/authorize",
		TokenEndpoint:         "https://example.test/token",
	}
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	openBrowserFn = func(_ string) error { return errors.New("no display") }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := RunBrowserPKCEFlow(ctx, endpoints, "cli", nil)
	if !errors.Is(err, ErrBrowserUnavailable) {
		t.Fatalf("expected ErrBrowserUnavailable, got %v", err)
	}
}

// newTokenSrv constructs an httptest.Server that validates the token-
// exchange form and returns a canned TokenResponse. seenForm (if
// non-nil) is populated with whatever the flow POSTed.
func newTokenSrv(t *testing.T, seenForm *url.Values, accessToken, refreshToken string, expiresIn int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil { //nolint:gosec // test fixture
			t.Errorf("parse form: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if seenForm != nil {
			*seenForm = r.PostForm
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{ //nolint:gosec // test fixture
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			TokenType:    "Bearer",
			ExpiresIn:    expiresIn,
		})
	}))
}
