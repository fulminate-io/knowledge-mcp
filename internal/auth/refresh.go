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

// RevokeRefreshToken logs and returns the provider's revocation outcome.
// An unadvertised endpoint is an expected no-op logged at Debug. Other
// failures are logged at WARN and returned; the logout caller still performs
// local cleanup. Callers needing the explicit provider outcome, including a
// missing endpoint, use RevokeRefreshTokenResult.
func RevokeRefreshToken(
	ctx context.Context,
	revocationEndpoint, refreshToken string,
) error {
	if revocationEndpoint == "" {
		slog.Debug("auth: no revocation endpoint; continuing local cleanup")
		return nil
	}
	if err := RevokeRefreshTokenResult(ctx, revocationEndpoint, refreshToken); err != nil {
		slog.Warn("auth: revoke unconfirmed; continuing local cleanup", "error", err)
		return err
	}
	return nil
}

// RevokeRefreshTokenResult reports whether the provider confirmed revocation.
// Local credential cleanup remains the caller's responsibility on every result.
func RevokeRefreshTokenResult(ctx context.Context, revocationEndpoint, refreshToken string) error {
	if revocationEndpoint == "" {
		return fmt.Errorf("auth: no revocation endpoint")
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}}
	req, err := buildFormPOST(ctx, revocationEndpoint, form)
	if err != nil {
		return fmt.Errorf("auth: invalid revocation endpoint: %w", err)
	}
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth: revocation unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: revocation not confirmed: HTTP %d", resp.StatusCode)
	}
	return nil
}
