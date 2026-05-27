// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"context"
)

// TokenResponse is the JSON body returned by AuthKit's token endpoint on
// success — both for the authorization_code exchange after the browser
// PKCE flow and for the refresh_token rotation. Access tokens are WorkOS
// JWTs (RS256, signed by AuthKit's JWKS).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"` // "Bearer"
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"` // space-separated, RFC 6749 — retained for upstream parity but unused; permissions live in the JWT claim
}

// ErrInvalidGrant indicates the server rejected the grant — for the
// refresh-token flow this means the stored refresh token is revoked,
// expired, or otherwise no longer accepted. Caller must re-authenticate
// via `knowledge login`. Wrapped by classifyOAuthError so callers can
// errors.Is against this sentinel.
var ErrInvalidGrant = errors.New("auth: invalid_grant")

// oauthHTTPClient is the HTTP client used for every OAuth call this
// package makes (browser-flow token exchange, refresh, revoke). 30s
// timeout covers the slowest realistic AuthKit response; the long
// "wait for user to authorize in browser" delay happens on the loopback
// listener, NOT inside an HTTP request.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// oauthErrorBody mirrors the JSON error response defined by RFC 6749 §5.2.
// Fields beyond error/error_description are ignored.
type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// buildFormPOST constructs a POST request with the standard OAuth headers
// and a form-urlencoded body. Used by the token-exchange, refresh, and
// revoke paths.
func buildFormPOST(ctx context.Context, target string, form url.Values) (*http.Request, error) {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// classifyOAuthError reads an OAuth error response body and maps the
// `error` code to a typed sentinel where one exists. Unknown codes
// become a wrapped generic error carrying the server's error_description
// so the user sees something actionable on stderr.
func classifyOAuthError(resp *http.Response) error {
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("auth: oauth error status %d: read body: %w", resp.StatusCode, readErr)
	}

	var body oauthErrorBody
	_ = json.Unmarshal(raw, &body) // best-effort

	if body.Error == "invalid_grant" {
		return ErrInvalidGrant
	}

	desc := body.ErrorDescription
	if desc == "" {
		desc = strings.TrimSpace(string(raw))
	}
	code := body.Error
	if code == "" {
		code = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("auth: oauth error %q (status %d): %s", code, resp.StatusCode, desc)
}
