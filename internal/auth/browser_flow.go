// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// browserFlowTimeout is the maximum time RunBrowserPKCEFlow waits for
// the user to complete the AuthKit hosted-login flow and the browser to
// redirect back to the loopback listener. Five minutes is generous
// enough to handle a paused tab, a fresh signup-with-email step, and a
// 2FA challenge.
const browserFlowTimeout = 5 * time.Minute

// ErrBrowserUnavailable is returned by RunBrowserPKCEFlow when the
// platform's "open URL in browser" command exits non-zero — typically
// because the environment has no DISPLAY (remote SSH), no default
// browser registered, or the user is in a container. `knowledge login`
// is browser-only by design (no device-flow fallback) so this is a
// terminal, user-facing error.
var ErrBrowserUnavailable = errors.New("auth: knowledge login requires a browser; this command is not supported in headless environments")

// RunBrowserPKCEFlow drives the OAuth 2.0 Authorization Code + PKCE
// flow with a local-loopback callback against the discovered AuthKit
// authorization server. Returns the token response on success.
//
// Steps:
//  1. Generate a PKCE code_verifier + S256 code_challenge.
//  2. Bind a TCP listener on 127.0.0.1:<random port> and build the
//     redirect_uri http://127.0.0.1:<port>/callback.
//  3. Open the user's browser to the AuthKit authorize URL with PKCE +
//     RFC 8707 `resource` parameter.
//  4. Wait on the listener for /callback?code=…&state=… and validate
//     state.
//  5. Shut the listener down and POST grant_type=authorization_code to
//     AuthKit's token endpoint with the code, code_verifier, redirect_uri,
//     client_id, and resource parameter.
//
// permissions is included as a space-separated `scope` parameter on the
// authorize URL even though AuthKit reads permissions from the user's
// role catalog — the explicit scope request is what tells AuthKit which
// resource indicator's permission catalog to mint claims against.
func RunBrowserPKCEFlow(
	ctx context.Context,
	endpoints *DiscoveredEndpoints,
	clientID string,
	permissions []string,
) (*TokenResponse, error) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		return nil, fmt.Errorf("auth: pkce: %w", err)
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("auth: state: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("auth: bind loopback listener: %w", err)
	}
	defer listener.Close()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("auth: loopback listener returned non-TCP addr %T", listener.Addr())
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", tcpAddr.Port)

	authorizeURL := buildAuthorizeURL(endpoints, clientID, redirectURI, challenge, state, permissions)

	flowCtx, cancel := context.WithTimeout(ctx, browserFlowTimeout)
	defer cancel()

	resultCh := make(chan callbackResult, 1)
	srv := startLoopbackServer(listener, state, resultCh)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	if err := openBrowser(authorizeURL); err != nil {
		return nil, fmt.Errorf("%w (underlying: %v)", ErrBrowserUnavailable, err)
	}

	var cb callbackResult
	select {
	case cb = <-resultCh:
	case <-flowCtx.Done():
		return nil, fmt.Errorf("auth: timed out waiting for browser callback: %w", flowCtx.Err())
	}
	if cb.err != nil {
		return nil, cb.err
	}

	return exchangeCode(flowCtx, endpoints.TokenEndpoint, clientID, cb.code, verifier, redirectURI, endpoints.Resource)
}

// callbackResult is the typed message the loopback handler sends back
// to RunBrowserPKCEFlow once the browser hits /callback. Exactly one of
// (code, err) is set.
type callbackResult struct {
	code string
	err  error
}

// startLoopbackServer wires a single-shot HTTP server on the bound
// listener whose /callback handler validates state, extracts the code,
// pushes a callbackResult to resultCh, and renders a small "you can
// close this tab" page. Any path other than /callback returns 404.
func startLoopbackServer(listener net.Listener, expectedState string, resultCh chan<- callbackResult) *http.Server {
	mux := http.NewServeMux()
	var once sync.Once
	send := func(r callbackResult) {
		once.Do(func() { resultCh <- r })
	}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errCode := q.Get("error"); errCode != "" {
			desc := q.Get("error_description")
			http.Error(w, fmt.Sprintf("Authorization failed: %s — %s", errCode, desc), http.StatusBadRequest)
			send(callbackResult{err: fmt.Errorf("auth: authorize endpoint returned %s: %s", errCode, desc)})
			return
		}
		gotState := q.Get("state")
		if gotState != expectedState {
			http.Error(w, "Authorization failed: state mismatch", http.StatusBadRequest)
			send(callbackResult{err: errors.New("auth: callback state mismatch")})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "Authorization failed: missing code", http.StatusBadRequest)
			send(callbackResult{err: errors.New("auth: callback missing code parameter")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(callbackHTML))
		send(callbackResult{code: code})
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = srv.Serve(listener)
	}()
	return srv
}

// callbackHTML is the page rendered after a successful authorization.
// Kept minimal — apathetic-voice memory: dev tool, no flourish.
const callbackHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>knowledge login</title></head>
<body><p>knowledge login complete. You can close this tab.</p></body></html>`

// buildAuthorizeURL composes the AuthKit authorize endpoint with the
// standard OAuth 2.0 + PKCE + RFC 8707 query parameters. The resource
// parameter ends up in the issued JWT's `aud` claim so the agent's
// Bearer middleware accepts it.
func buildAuthorizeURL(endpoints *DiscoveredEndpoints, clientID, redirectURI, challenge, state string, permissions []string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if endpoints.Resource != "" {
		q.Set("resource", endpoints.Resource)
	}
	if len(permissions) > 0 {
		q.Set("scope", strings.Join(permissions, " "))
	}
	return endpoints.AuthorizationEndpoint + "?" + q.Encode()
}

// exchangeCode POSTs the authorization_code grant to AuthKit's token
// endpoint. Returns the parsed TokenResponse on 200, a typed
// ErrInvalidGrant on grant rejection, or a wrapped error otherwise.
func exchangeCode(
	ctx context.Context,
	tokenEndpoint, clientID, code, verifier, redirectURI, resource string,
) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	if resource != "" {
		form.Set("resource", resource)
	}

	req, err := buildFormPOST(ctx, tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: token exchange request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyOAuthError(resp)
	}
	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("auth: decode token response: %w", err)
	}
	return &tr, nil
}

// newPKCE generates a 32-byte random code_verifier and its S256
// code_challenge per RFC 7636. The verifier is base64url(no padding) of
// the raw 32 bytes; the challenge is base64url(no padding) of the
// SHA-256 of the ASCII verifier string.
func newPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// randomURLSafe returns n random bytes encoded as base64url(no padding).
// Used for the `state` parameter.
func randomURLSafe(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// openBrowser opens the user's default browser to target on macOS,
// Linux, and Windows. Returns an error if the platform launcher exits
// non-zero — caller maps this to ErrBrowserUnavailable.
//
// Replaced in tests via openBrowserFn so unit tests don't fork `open`.
var openBrowserFn = openBrowserDefault

func openBrowser(target string) error { return openBrowserFn(target) }

func openBrowserDefault(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
