// SPDX-License-Identifier: Apache-2.0

//go:build devendpoint

package cli

// CloudEndpoint is the dev/staging Fulminate API base URL. Devendpoint
// builds are internal-only — release binaries (no build tag) cannot
// reach dev.fulminate.io. There is no runtime override (memory:
// feedback_no_endpoint_override); the build tag is the entire switch.
const CloudEndpoint = "https://dev.fulminate.io"

// allowedAuthHosts (devendpoint build): the staging WorkOS AuthKit
// host the CLI will trust as an authorization server. Discovered via
// RFC 9728 protected-resource metadata at CloudEndpoint; refused if
// the discovered host is not in this map.
var allowedAuthHosts = map[string]struct{}{
	"bright-secret-07-staging.authkit.app": {},
}

// AllowedAuthHosts returns a copy of the AuthKit allowlist so callers
// outside this package can pass it to auth.NewOAuthTokenSource without
// reaching into a private map.
func AllowedAuthHosts() map[string]struct{} {
	out := make(map[string]struct{}, len(allowedAuthHosts))
	for k := range allowedAuthHosts {
		out[k] = struct{}{}
	}
	return out
}
