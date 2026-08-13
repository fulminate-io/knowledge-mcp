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

// OAuthError is an error response from the authorization server that
// does not map to a typed sentinel. It carries the HTTP status alongside
// the RFC 6749 §5.2 error code so callers can tell a rejected credential
// (401) from a server-side fault (5xx) without matching on message text.
type OAuthError struct {
	// Code is the server's `error` field, or the HTTP status text when
	// the response body carried no error code.
	Code string
	// StatusCode is the HTTP status of the error response.
	StatusCode int
	// Description is the server's `error_description`, falling back to
	// the raw body when the response was not a well-formed OAuth error.
	Description string
}

func (e *OAuthError) Error() string {
	return fmt.Sprintf("auth: oauth error %q (status %d): %s",
		e.Code, e.StatusCode, e.Description)
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
// `error` code to a typed sentinel where one exists. Every other code
// becomes an [OAuthError] carrying the status and the server's
// error_description, so the user sees something actionable on stderr and
// callers can branch on the status.
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
	return &OAuthError{Code: code, StatusCode: resp.StatusCode, Description: desc}
}
