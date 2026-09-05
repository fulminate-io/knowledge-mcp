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

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
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
	// OURS: stamped, per the call-path census disposition for this leg.
	body, err := fetchWellKnown(ctx, u, true)
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
	// A THIRD PARTY: deliberately UNSTAMPED. A client-version header must never
	// reach an authorization server we do not operate.
	body, err := fetchWellKnown(ctx, u, false)
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
//
// ONE HELPER, TWO TARGETS, AND ONLY ONE OF THEM IS OURS. The RFC 9728
// protected-resource document is served by the Fulminate API and carries the
// client-identity headers like every other call to it; the RFC 8414
// authorization-server document is served by a THIRD-PARTY authorization
// server and must carry NEITHER. Sending a third party a client-version header
// tells it something about our users for no benefit we can name, and the
// call-path census dispositions that leg as out of scope — a stamp there would
// put the code and the manifest in disagreement, which is the exact thing that
// census exists to prevent.
//
// THE DECISION IS THE CALLER'S, passed in explicitly, rather than a host
// comparison inside this helper. A host-sniffing branch would re-derive the
// census's classification in a second place, where it can drift from the
// manifest silently.
func fetchWellKnown(ctx context.Context, fullURL string, stampClientIdentity bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if stampClientIdentity {
		clientver.Stamp(req.Header)
	}

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A version refusal on our own leg is routed through the shared
		// classifier so it names the minimum, this client's version and the
		// upgrade command rather than surfacing as a bare status.
		if stampClientIdentity {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBodyBytes))
			if refusal, ok := LatchVersionRefusal(RefusalObservation{
				Status:    resp.StatusCode,
				Header:    resp.Header,
				Body:      raw,
				ReadErr:   readErr,
				Transport: "oauth-discovery",
				Path:      fullURL,
			}); ok {
				return nil, refusal
			}
		}
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
