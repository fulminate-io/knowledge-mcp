// SPDX-License-Identifier: Apache-2.0

//go:build !devendpoint

package cli

// CloudEndpoint is the production Fulminate API base URL. Hardcoded on
// purpose: the knowledge CLI exposes NO `--fulminate-endpoint` flag, NO
// `$FULMINATE_ENDPOINT` env var, NO config override. The build tag is
// the only switch (memory: feedback_no_endpoint_override). A release
// binary in a customer's hands can only reach this one host.
const CloudEndpoint = "https://fulminate.io"

// allowedAuthHosts (release build): the closed set of WorkOS AuthKit
// hosts the CLI will trust as authorization servers. Discovered via
// RFC 9728 protected-resource metadata at CloudEndpoint; refused if
// the discovered host is not in this map. Production tenant only.
var allowedAuthHosts = map[string]struct{}{
	"auth.fulminate.io": {},
}

// AllowedAuthHosts returns a copy of the AuthKit allowlist so callers
// outside this package (e.g. cmd/knowledge-server/tools.go) can pass it
// to auth.NewOAuthTokenSource without reaching into a private map.
func AllowedAuthHosts() map[string]struct{} {
	out := make(map[string]struct{}, len(allowedAuthHosts))
	for k := range allowedAuthHosts {
		out[k] = struct{}{}
	}
	return out
}
