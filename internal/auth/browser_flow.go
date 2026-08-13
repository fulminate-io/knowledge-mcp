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
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// oauthScopes is the scope set requested on the AuthKit authorize URL.
// It is the standard OIDC set plus offline_access — NOT the application's
// custom permission slugs (mcp:knowledge:*). WorkOS does not accept
// arbitrary application slugs as OAuth scopes; permissions are delivered
// in the access token's `permissions` claim from the user's assigned
// WorkOS Role, independent of the scope request (read later via
// ParsePermissionsFromJWT). offline_access is the scope that makes
// AuthKit mint the refresh_token this flow persists; openid/profile/email
// are the conventional AuthKit OIDC scopes.
const oauthScopes = "openid profile email offline_access"

// browserFlowTimeout is the maximum time RunBrowserPKCEFlow waits for
// the user to complete the AuthKit hosted-login flow and the browser to
// redirect back to the loopback listener. Five minutes is generous
// enough to handle a paused tab, a fresh signup-with-email step, and a
// 2FA challenge.
const browserFlowTimeout = 5 * time.Minute

// authCallbackPort is the fixed loopback port the callback listener binds in
// container mode. The container has no way to publish a randomly-chosen port
// after the fact, so the port has to be known ahead of time for the frontend
// route and the image to agree on it. 15025 sits just past the ports this
// project already claims: 15021 pprof, 15022 graph server, 15023 the frontend
// external listener, 15024 the daemon's loopback MCP.
const authCallbackPort = 15025

// defaultRedirectHostPort is the HOST-published port the browser dials when
// KNOWLEDGE_LOGIN_REDIRECT_PORT is unset — the frontend's external listener,
// which forwards /auth/callback on to authCallbackPort inside the container.
const defaultRedirectHostPort = 15023

// envLoginViaFrontend selects container mode when non-empty. The image sets
// it, so a login container inherits it with no user action, and a host shell
// can never carry it accidentally. It is named for the mechanism rather than
// for "container" so nothing else grows a dependency on it.
const envLoginViaFrontend = "KNOWLEDGE_LOGIN_VIA_FRONTEND"

// envLoginRedirectPort optionally overrides the host-published port the
// browser dials. Honored only in container mode.
const envLoginRedirectPort = "KNOWLEDGE_LOGIN_REDIRECT_PORT"

// hostCallbackPath and frontendCallbackPath are the two callback paths. The
// browser dials the path the redirect URI advertises, the frontend forwards
// that same path, and the listener answers it — no path is ever rewritten.
const (
	hostCallbackPath     = "/callback"
	frontendCallbackPath = "/auth/callback"
)

// promptOut is where the flow writes the authorize URL. Replaced in tests so
// a unit test can read what the flow printed.
var promptOut io.Writer = os.Stdout

// RunBrowserPKCEFlow drives the OAuth 2.0 Authorization Code + PKCE
// flow with a local-loopback callback against the discovered AuthKit
// authorization server. On success it returns the dynamically-registered
// client_id (which the caller persists so the refresh path can reuse it)
// and the token response.
//
// Steps:
//  1. Generate a PKCE code_verifier + S256 code_challenge.
//  2. Bind a loopback TCP listener and build the matching redirect_uri —
//     a random port and /callback on a host, the fixed authCallbackPort
//     and the frontend's published port and /auth/callback in a container
//     (see bindCallbackListener).
//  3. Dynamically register a fresh public client (RFC 7591) whose
//     redirect_uri matches this loopback callback. WorkOS honors RFC 8707
//     resource indicators only for DCR/CIMD clients — a static OAuth
//     Application would get invalid_target at the token endpoint.
//  4. Open the user's browser to the AuthKit authorize URL with PKCE +
//     RFC 8707 `resource` parameter.
//  5. Wait on the listener for the callback path with ?code=…&state=…
//     and validate state.
//  6. Shut the listener down and POST grant_type=authorization_code to
//     AuthKit's token endpoint with the code, code_verifier, redirect_uri,
//     client_id, and resource parameter.
//
// The authorize request asks for the standard OIDC + offline_access
// scope set (oauthScopes). The application's custom permission slugs
// (mcp:knowledge:*) are NOT requested as scopes — WorkOS delivers them
// in the access token's `permissions` claim from the user's assigned
// Role, which the client reads via ParsePermissionsFromJWT.
func RunBrowserPKCEFlow(
	ctx context.Context,
	endpoints *DiscoveredEndpoints,
) (clientID string, tr *TokenResponse, err error) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		return "", nil, fmt.Errorf("auth: pkce: %w", err)
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return "", nil, fmt.Errorf("auth: state: %w", err)
	}

	listener, redirectURI, err := bindCallbackListener()
	if err != nil {
		return "", nil, err
	}
	defer listener.Close()

	clientID, err = RegisterPublicClient(ctx, endpoints.RegistrationEndpoint, redirectURI)
	if err != nil {
		return "", nil, err
	}

	authorizeURL := buildAuthorizeURL(endpoints, clientID, redirectURI, challenge, state)

	flowCtx, cancel := context.WithTimeout(ctx, browserFlowTimeout)
	defer cancel()

	resultCh := make(chan callbackResult, 1)
	srv := startLoopbackServer(listener, state, resultCh)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// The printed URL is the contract; the launcher is best-effort
	// convenience. Printing first means the URL is there whether the launcher
	// succeeds, fails, or succeeds without opening anything — the last of
	// which is what a headless host with xdg-open installed but no DISPLAY
	// does, since cmd.Start() returns nil as soon as the binary exists.
	fmt.Fprintf(promptOut, "\nTo authenticate, open this URL:\n\n  %s\n\n", authorizeURL)
	if err := openBrowser(authorizeURL); err != nil {
		fmt.Fprintf(promptOut, "Could not open a browser automatically (%v). Open the URL above.\n", err)
	}

	var cb callbackResult
	select {
	case cb = <-resultCh:
	case <-flowCtx.Done():
		return "", nil, fmt.Errorf("auth: timed out waiting for browser callback: %w", flowCtx.Err())
	}
	if cb.err != nil {
		return "", nil, cb.err
	}

	tr, err = exchangeCode(flowCtx, endpoints.TokenEndpoint, clientID, cb.code, verifier, redirectURI, endpoints.Resource)
	if err != nil {
		return "", nil, err
	}
	return clientID, tr, nil
}

// callbackResult is the typed message the loopback handler sends back
// to RunBrowserPKCEFlow once the browser hits /callback. Exactly one of
// (code, err) is set.
type callbackResult struct {
	code string
	err  error
}

// bindCallbackListener binds the loopback callback listener and returns it
// with the redirect_uri the browser should be sent to.
//
// On a host (the default) this is byte-for-byte the historical behavior: a
// random port on 127.0.0.1, advertised as itself.
//
// In container mode the two ports come apart. The listener binds the fixed
// authCallbackPort inside the container, while the advertised URI names the
// HOST-published frontend port, because that is the address the user's
// browser can actually reach; the frontend forwards /auth/callback inward.
//
// The loopback fence is untouched either way — the listener is 127.0.0.1 in
// both modes, and the only thing container mode changes is a port number
// written into a URL.
func bindCallbackListener() (net.Listener, string, error) {
	if os.Getenv(envLoginViaFrontend) == "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", fmt.Errorf("auth: bind loopback listener: %w", err)
		}
		tcpAddr, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			_ = listener.Close()
			return nil, "", fmt.Errorf("auth: loopback listener returned non-TCP addr %T", listener.Addr())
		}
		return listener, fmt.Sprintf("http://127.0.0.1:%d%s", tcpAddr.Port, hostCallbackPath), nil
	}

	// Resolve the advertised port before binding, so a bad value fails
	// without leaving a listener behind.
	hostPort, err := redirectHostPort()
	if err != nil {
		return nil, "", err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", authCallbackPort))
	if err != nil {
		return nil, "", fmt.Errorf(
			"auth: bind loopback listener on port %d — another `knowledge login` may already be in progress: %w",
			authCallbackPort, err)
	}
	return listener, fmt.Sprintf("http://127.0.0.1:%d%s", hostPort, frontendCallbackPath), nil
}

// redirectHostPort resolves the host-published port the browser dials. An
// unparseable or out-of-range override is a loud error naming the variable,
// never a silent fall back to the default: a user who set the variable wants
// that port, and defaulting quietly sends the browser somewhere the callback
// never arrives.
func redirectHostPort() (int, error) {
	raw := os.Getenv(envLoginRedirectPort)
	if raw == "" {
		return defaultRedirectHostPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("auth: %s=%q is not a valid TCP port (1-65535)", envLoginRedirectPort, raw)
	}
	return port, nil
}

// startLoopbackServer wires a single-shot HTTP server on the bound listener
// whose callback handler validates state, extracts the code, pushes a
// callbackResult to resultCh, and renders a small "you can close this tab"
// page. The same handler answers both callback paths, since which one the
// browser dials depends on the mode bindCallbackListener chose. Any other
// path returns 404.
func startLoopbackServer(listener net.Listener, expectedState string, resultCh chan<- callbackResult) *http.Server {
	mux := http.NewServeMux()
	var once sync.Once
	send := func(r callbackResult) {
		once.Do(func() { resultCh <- r })
	}
	handleCallback := func(w http.ResponseWriter, r *http.Request) {
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
	}
	mux.HandleFunc(hostCallbackPath, handleCallback)
	mux.HandleFunc(frontendCallbackPath, handleCallback)

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
func buildAuthorizeURL(endpoints *DiscoveredEndpoints, clientID, redirectURI, challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("scope", oauthScopes)
	if endpoints.Resource != "" {
		q.Set("resource", endpoints.Resource)
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
// Linux, and Windows. Returns an error if the platform launcher cannot be
// started — best-effort only, since the caller has already printed the URL.
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
