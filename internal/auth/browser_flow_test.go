// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	regSrv := newRegistrationSrv(t, "client_dcr_test")
	defer regSrv.Close()

	endpoints := &DiscoveredEndpoints{
		Resource:              "https://fulminate.io/mcp",
		Issuer:                "https://auth.fulminate.io",
		AuthorizationEndpoint: "https://auth.fulminate.io/oauth2/authorize",
		TokenEndpoint:         tokenSrv.URL,
		RegistrationEndpoint:  regSrv.URL,
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
		if got := q.Get("scope"); got != oauthScopes {
			t.Errorf("authorize scope = %q, want %q", got, oauthScopes)
		}
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

	clientID, tr, err := RunBrowserPKCEFlow(ctx, endpoints)
	if err != nil {
		t.Fatalf("RunBrowserPKCEFlow: %v", err)
	}
	if clientID != "client_dcr_test" {
		t.Errorf("returned client_id = %q, want client_dcr_test", clientID)
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
	regSrv := newRegistrationSrv(t, "client_dcr_test")
	defer regSrv.Close()
	endpoints := &DiscoveredEndpoints{
		AuthorizationEndpoint: "https://example.test/authorize",
		TokenEndpoint:         tokenSrv.URL,
		RegistrationEndpoint:  regSrv.URL,
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

	_, _, err := RunBrowserPKCEFlow(ctx, endpoints)
	if err == nil {
		t.Fatal("expected state-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// capturePromptOut redirects the flow's prompt writer into a buffer.
func capturePromptOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := promptOut
	t.Cleanup(func() { promptOut = orig })
	promptOut = &buf
	return &buf
}

// TestLoginCallback_PrintsURLEvenWhenBrowserOpens is the catcher for the
// always-print contract: the launcher SUCCEEDS here, so a flow that printed
// the URL only on launcher failure would leave the buffer empty and fail this
// test alone.
func TestLoginCallback_PrintsURLEvenWhenBrowserOpens(t *testing.T) {
	t.Setenv(envLoginViaFrontend, "")
	endpoints := loginFlowEndpoints(t)
	buf := capturePromptOut(t)

	var handedURL string
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	drive := fakeBrowserDrivingListener(t, "", new(string))
	openBrowserFn = func(authorizeURL string) error {
		handedURL = authorizeURL
		return drive(authorizeURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := RunBrowserPKCEFlow(ctx, endpoints); err != nil {
		t.Fatalf("RunBrowserPKCEFlow: %v", err)
	}
	if handedURL == "" {
		t.Fatal("the launcher was never handed an authorize URL")
	}
	if !strings.Contains(buf.String(), handedURL) {
		t.Errorf("printed output does not contain the authorize URL.\nprinted: %q\nwant to contain: %q",
			buf.String(), handedURL)
	}
}

// TestLoginCallback_LauncherFailureIsNotTerminal asserts a launcher failure
// no longer aborts the flow: the URL is still printed and the callback still
// completes.
func TestLoginCallback_LauncherFailureIsNotTerminal(t *testing.T) {
	t.Setenv(envLoginViaFrontend, "")
	endpoints := loginFlowEndpoints(t)
	buf := capturePromptOut(t)

	var handedURL string
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	drive := fakeBrowserDrivingListener(t, "", new(string))
	openBrowserFn = func(authorizeURL string) error {
		handedURL = authorizeURL
		_ = drive(authorizeURL)
		return errors.New("no display")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientID, tr, err := RunBrowserPKCEFlow(ctx, endpoints)
	if err != nil {
		t.Fatalf("a launcher failure aborted the flow: %v", err)
	}
	if clientID != "client_dcr_test" || tr.AccessToken != "access.jwt.token" {
		t.Errorf("flow returned (%q, %q), want the exchanged credential", clientID, tr.AccessToken)
	}
	if !strings.Contains(buf.String(), handedURL) {
		t.Errorf("printed output does not contain the authorize URL.\nprinted: %q\nwant to contain: %q",
			buf.String(), handedURL)
	}
}

// fakeBrowserDrivingListener returns an openBrowserFn stand-in that records
// the advertised redirect_uri and then drives the LISTENER's own address with
// a valid code + state. Passing a listenerBase different from the advertised
// host is what lets the container-mode test prove the two ports really are
// separate: the browser is told one address and the flow is completed on
// another. An empty listenerBase drives the advertised address itself.
func fakeBrowserDrivingListener(t *testing.T, listenerBase string, gotRedirect *string) func(string) error {
	t.Helper()
	return func(authorizeURL string) error {
		u, err := url.Parse(authorizeURL)
		if err != nil {
			t.Errorf("authorize URL malformed: %v", err)
			return err
		}
		q := u.Query()
		*gotRedirect = q.Get("redirect_uri")
		redirect, err := url.Parse(*gotRedirect)
		if err != nil {
			t.Errorf("redirect_uri malformed: %v", err)
			return err
		}
		base := listenerBase
		if base == "" {
			base = redirect.Scheme + "://" + redirect.Host
		}
		drive, err := url.Parse(base + redirect.Path)
		if err != nil {
			t.Errorf("listener URL malformed: %v", err)
			return err
		}
		rq := url.Values{}
		rq.Set("code", "auth_code_xyz")
		rq.Set("state", q.Get("state"))
		drive.RawQuery = rq.Encode()
		go func() {
			// brief pause so the listener.Accept loop is ready
			time.Sleep(20 * time.Millisecond)
			resp, _ := http.Get(drive.String())
			if resp != nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

// loginFlowEndpoints wires the DCR + token stubs every callback test needs.
func loginFlowEndpoints(t *testing.T) *DiscoveredEndpoints {
	t.Helper()
	tokenSrv := newTokenSrv(t, nil, "access.jwt.token", "frt_new", 3600)
	t.Cleanup(tokenSrv.Close)
	regSrv := newRegistrationSrv(t, "client_dcr_test")
	t.Cleanup(regSrv.Close)
	return &DiscoveredEndpoints{
		AuthorizationEndpoint: "https://auth.fulminate.io/oauth2/authorize",
		TokenEndpoint:         tokenSrv.URL,
		RegistrationEndpoint:  regSrv.URL,
	}
}

// TestLoginCallback_HostModeRandomPort pins the host path: a random loopback
// port advertised as itself on /callback, which is the behavior that must not
// regress when container mode exists alongside it.
func TestLoginCallback_HostModeRandomPort(t *testing.T) {
	// Set the switch explicitly rather than relying on it being absent from
	// the ambient environment — an untested assumption is not a fixture.
	t.Setenv(envLoginViaFrontend, "")
	endpoints := loginFlowEndpoints(t)
	capturePromptOut(t)

	var redirectURI string
	origOpen := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origOpen })
	openBrowserFn = fakeBrowserDrivingListener(t, "", &redirectURI)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := RunBrowserPKCEFlow(ctx, endpoints); err != nil {
		t.Fatalf("RunBrowserPKCEFlow: %v", err)
	}

	u, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("redirect_uri %q: %v", redirectURI, err)
	}
	if u.Path != hostCallbackPath {
		t.Errorf("host-mode redirect path = %q, want %q", u.Path, hostCallbackPath)
	}
	if u.Hostname() != "127.0.0.1" {
		t.Errorf("host-mode redirect host = %q, want 127.0.0.1", u.Hostname())
	}
	port := u.Port()
	if port == strconv.Itoa(authCallbackPort) || port == strconv.Itoa(defaultRedirectHostPort) {
		t.Errorf("host-mode redirect port = %s, want a random port (neither %d nor %d)",
			port, authCallbackPort, defaultRedirectHostPort)
	}
}

// TestLoginCallback_ContainerModeFixedPort pins the split the whole design
// rests on: the listener binds authCallbackPort inside the container while
// the browser is told the host-published frontend port.
func TestLoginCallback_ContainerModeFixedPort(t *testing.T) {
	t.Setenv(envLoginViaFrontend, "1")
	// Pin the override empty too. redirectHostPort treats "" as unset, so this
	// establishes the default-port fixture instead of inheriting whatever the
	// ambient environment happens to carry — an ambient value reds the
	// assertion below for reasons that have nothing to do with the code.
	t.Setenv(envLoginRedirectPort, "")

	run := func(t *testing.T, wantRedirect string) {
		t.Helper()
		endpoints := loginFlowEndpoints(t)
		capturePromptOut(t)
		var redirectURI string
		origOpen := openBrowserFn
		t.Cleanup(func() { openBrowserFn = origOpen })
		// Drive the LISTENER port, not the advertised one. Completing the
		// flow this way is the proof that the listener really is on
		// authCallbackPort while the browser was pointed elsewhere.
		listenerBase := fmt.Sprintf("http://127.0.0.1:%d", authCallbackPort)
		openBrowserFn = fakeBrowserDrivingListener(t, listenerBase, &redirectURI)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, _, err := RunBrowserPKCEFlow(ctx, endpoints); err != nil {
			t.Fatalf("RunBrowserPKCEFlow: %v", err)
		}
		if redirectURI != wantRedirect {
			t.Errorf("advertised redirect_uri = %q, want %q", redirectURI, wantRedirect)
		}
	}

	run(t, fmt.Sprintf("http://127.0.0.1:%d%s", defaultRedirectHostPort, frontendCallbackPath))

	t.Run("redirect_port_override", func(t *testing.T) {
		t.Setenv(envLoginRedirectPort, "18080")
		run(t, "http://127.0.0.1:18080"+frontendCallbackPath)
	})

	t.Run("redirect_port_invalid_is_loud", func(t *testing.T) {
		for _, bad := range []string{"notaport", "0", "70000", "-1"} {
			t.Setenv(envLoginRedirectPort, bad)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _, err := RunBrowserPKCEFlow(ctx, &DiscoveredEndpoints{})
			cancel()
			if err == nil {
				t.Fatalf("%s=%q was accepted, want a loud error", envLoginRedirectPort, bad)
			}
			if !strings.Contains(err.Error(), envLoginRedirectPort) {
				t.Errorf("%s=%q error %v does not name the variable", envLoginRedirectPort, bad, err)
			}
		}
	})
}

// newRegistrationSrv constructs an httptest.Server that answers the RFC
// 7591 DCR registration POST with the given client_id. RunBrowserPKCEFlow
// registers a fresh public client before opening the browser, so every
// flow test needs this stub at endpoints.RegistrationEndpoint.
func newRegistrationSrv(t *testing.T, clientID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": clientID}) //nolint:gosec // test fixture
	}))
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
