// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

// grantTypeRefreshToken is the RFC 6749 §6 grant_type for rotating an
// access token using the long-lived refresh token.
const grantTypeRefreshToken = "refresh_token"

// RefreshAccessToken exchanges the given refresh_token for a fresh
// access token by POSTing to AuthKit's token endpoint (resolved via
// the RFC 8414 authorization-server metadata document). AuthKit rotates
// the refresh token on every successful call — the returned
// TokenResponse.RefreshToken must be persisted immediately, replacing
// the previous value.
//
// resource is the RFC 8707 Resource Indicator value the agent expects
// in the JWT's `aud` claim (e.g. "https://fulminate.io/mcp"). When non-
// empty it's forwarded as a `resource` form parameter so AuthKit mints
// the new access token with the correct audience.
//
// Server errors:
//   - invalid_grant → returns [ErrInvalidGrant]. The refresh token has
//     been revoked, expired, or otherwise invalidated; the caller must
//     force the user through `knowledge login` again.
//   - any other OAuth error → wrapped error with the server's code and
//     description.
func RefreshAccessToken(
	ctx context.Context,
	tokenEndpoint, clientID, refreshToken, resource string,
) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", grantTypeRefreshToken)
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	if resource != "" {
		form.Set("resource", resource)
	}

	req, err := buildFormPOST(ctx, tokenEndpoint, form)
	if err != nil {
		return nil, err
	}

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var tr TokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
			return nil, fmt.Errorf("auth: decode refresh response: %w", err)
		}
		return &tr, nil
	}
	return nil, classifyOAuthError(resp)
}

// RevokeRefreshToken revokes the refresh token on AuthKit's revocation
// endpoint per RFC 7009. Best-effort: network or server errors are
// logged at WARN level but never returned to the caller. The local-only
// half of logout (deleting the token from the keychain) is performed by
// the caller regardless — a failed revoke must not block a logout.
//
// revocationEndpoint may be "" when the AuthKit deployment doesn't
// expose RFC 7009 (per the authorization-server metadata document); in
// that case the call is a no-op and local cleanup is the only signal.
//
// Per RFC 7009 §2.2 the server returns 200 for both successful
// revocation and for unknown tokens, so we only warn on transport
// errors.
func RevokeRefreshToken(
	ctx context.Context,
	revocationEndpoint, refreshToken string,
) error {
	if revocationEndpoint == "" {
		slog.Debug("auth: revoke: no revocation_endpoint advertised; skipping server revoke")
		return nil
	}

	form := url.Values{}
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")

	req, err := buildFormPOST(ctx, revocationEndpoint, form)
	if err != nil {
		slog.Warn("auth: revoke: failed to build request", "error", err)
		return nil
	}

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		slog.Warn("auth: revoke: network error (continuing with local logout)", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("auth: revoke: unexpected status (continuing with local logout)",
			"status", resp.StatusCode)
	}
	return nil
}
