// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProtectedResourceMetadata mirrors the RFC 9728 OAuth Protected Resource
// Metadata document published at <fulminate>/.well-known/oauth-protected-resource.
// Only the fields the CLI actually consumes are surfaced; unknown fields
// are ignored.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// AuthorizationServerMetadata mirrors the RFC 8414 OAuth Authorization
// Server Metadata document published at <authkit>/.well-known/oauth-authorization-server.
// Only the fields the CLI actually consumes are surfaced.
type AuthorizationServerMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// DiscoveredEndpoints is the resolved set of URLs the CLI needs to run a
// browser-PKCE login (authorize + token) and to refresh / revoke later.
// Resource is echoed back to the token endpoint as the RFC 8707 `resource`
// parameter and ends up in the issued JWT's `aud` claim.
type DiscoveredEndpoints struct {
	Resource              string
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RevocationEndpoint    string // may be "" if AuthKit doesn't expose one
	RegistrationEndpoint  string // RFC 7591 DCR endpoint; "" if not advertised
}

// Discover walks the two-step RFC 9728 + RFC 8414 chain starting from a
// Fulminate API base URL (e.g. https://fulminate.io). One GET to the
// agent for protected-resource metadata, one GET to AuthKit for the
// authorization-server metadata. Returns a fully-populated
// DiscoveredEndpoints or an error explaining which step failed.
//
// allowedAuthHosts is the closed set of AuthKit hosts the CLI will trust
// as authorization servers. A discovered host outside this set is a hard
// error — protects against a compromised or misconfigured agent pointing
// the CLI at an attacker-controlled OAuth server.
func Discover(
	ctx context.Context,
	fulminateEndpoint string,
	allowedAuthHosts map[string]struct{},
) (*DiscoveredEndpoints, error) {
	prm, err := fetchProtectedResourceMetadata(ctx, fulminateEndpoint)
	if err != nil {
		return nil, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("auth: discover: %s metadata listed no authorization_servers", fulminateEndpoint)
	}
	authServerURL := prm.AuthorizationServers[0]
	if err := validateAuthServerHost(authServerURL, allowedAuthHosts); err != nil {
		return nil, err
	}

	asm, err := fetchAuthorizationServerMetadata(ctx, authServerURL)
	if err != nil {
		return nil, err
	}
	if asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return nil, fmt.Errorf("auth: discover: %s metadata missing authorization_endpoint or token_endpoint", authServerURL)
	}

	return &DiscoveredEndpoints{
		Resource:              prm.Resource,
		Issuer:                asm.Issuer,
		AuthorizationEndpoint: asm.AuthorizationEndpoint,
		TokenEndpoint:         asm.TokenEndpoint,
		RevocationEndpoint:    asm.RevocationEndpoint,
		RegistrationEndpoint:  asm.RegistrationEndpoint,
	}, nil
}

// fetchProtectedResourceMetadata GETs the RFC 9728 metadata document
// from the Fulminate endpoint. The path is fixed.
func fetchProtectedResourceMetadata(ctx context.Context, fulminateEndpoint string) (*ProtectedResourceMetadata, error) {
	u := strings.TrimRight(fulminateEndpoint, "/") + "/.well-known/oauth-protected-resource"
	body, err := fetchWellKnown(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch protected-resource metadata: %w", err)
	}
	var out ProtectedResourceMetadata
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("auth: parse protected-resource metadata: %w", err)
	}
	return &out, nil
}

// fetchAuthorizationServerMetadata GETs the RFC 8414 metadata document
// from the authorization-server URL. The path is appended to whatever
// the protected-resource metadata pointed at.
func fetchAuthorizationServerMetadata(ctx context.Context, authServerURL string) (*AuthorizationServerMetadata, error) {
	u := strings.TrimRight(authServerURL, "/") + "/.well-known/oauth-authorization-server"
	body, err := fetchWellKnown(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch authorization-server metadata: %w", err)
	}
	var out AuthorizationServerMetadata
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("auth: parse authorization-server metadata: %w", err)
	}
	return &out, nil
}

// fetchWellKnown is the shared GET + read + status-check helper for the
// two .well-known endpoints. Limits body size to 64 KB so a misconfigured
// server can't OOM the CLI.
func fetchWellKnown(ctx context.Context, fullURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	const max = 64 * 1024
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// validateAuthServerHost enforces that the discovered authorization-server
// host is in the CLI's allowlist. The agent's metadata is signed-by-TLS
// but the CLI still pins which AuthKit tenants it will follow — an agent
// that's been pointed at someone else's OAuth server cannot trick this
// binary into surrendering credentials to an unknown host.
func validateAuthServerHost(authServerURL string, allowed map[string]struct{}) error {
	u, err := url.Parse(authServerURL)
	if err != nil {
		return fmt.Errorf("auth: discover: parse authorization_servers[0]=%q: %w", authServerURL, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("auth: discover: authorization_servers[0]=%q is not https", authServerURL)
	}
	if _, ok := allowed[u.Hostname()]; !ok {
		return fmt.Errorf("auth: discover: authorization-server host %q is not in the allowed AuthKit hosts list", u.Hostname())
	}
	return nil
}
