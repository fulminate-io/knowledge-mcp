// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// dcrRegistrationRequest is the RFC 7591 Dynamic Client Registration body
// for a public (PKCE, no client secret) MCP client.
//
// Why DCR instead of a static client_id: WorkOS AuthKit honors RFC 8707
// resource indicators (binding the access token's `aud` to the MCP
// resource URL the agent validates) ONLY for clients registered via DCR
// or a Client ID Metadata Document. A hand-created OAuth Application's
// tokens are minted with aud=client_id and reject the `resource`
// parameter at the token endpoint with `invalid_target`. So the knowledge
// CLI owns no static client_id — it registers a fresh public client at
// login time whose redirect_uri matches the loopback callback for that
// login.
type dcrRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// dcrRegistrationResponse captures the only field the CLI needs from the
// RFC 7591 registration response. A public client carries no secret.
type dcrRegistrationResponse struct {
	ClientID string `json:"client_id"`
}

// RegisterPublicClient performs an RFC 7591 Dynamic Client Registration
// against the authorization server's registration endpoint and returns
// the issued public client_id.
//
// redirectURI MUST be the exact loopback callback the subsequent
// authorize + token requests will use — the authorization server
// validates the redirect against the registered set. Because the loopback
// port is ephemeral, this registers a fresh client per login; the issued
// id is persisted (auth.KeyClientID) so the refresh path reuses it without
// re-registering.
func RegisterPublicClient(ctx context.Context, registrationEndpoint, redirectURI string) (string, error) {
	if registrationEndpoint == "" {
		return "", fmt.Errorf("auth: dcr: authorization server advertised no registration_endpoint")
	}
	body, err := json.Marshal(dcrRegistrationRequest{
		ClientName:              "knowledge CLI",
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   oauthScopes,
	})
	if err != nil {
		return "", fmt.Errorf("auth: dcr: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("auth: dcr: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: dcr: register request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("auth: dcr: registration HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out dcrRegistrationResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("auth: dcr: decode response: %w", err)
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("auth: dcr: registration response missing client_id")
	}
	return out.ClientID, nil
}
