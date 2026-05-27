// SPDX-License-Identifier: Apache-2.0

//go:build devendpoint

package cli

import "testing"

// TestCloudEndpoint_Dev asserts the devendpoint-build cloud host.
// Pinned by build tag — there is no runtime override (memory:
// feedback_no_endpoint_override).
func TestCloudEndpoint_Dev(t *testing.T) {
	if CloudEndpoint != "https://dev.fulminate.io" {
		t.Errorf("devendpoint CloudEndpoint = %q, want https://dev.fulminate.io", CloudEndpoint)
	}
}

// TestAllowedAuthHosts_Dev asserts the staging AuthKit allowlist.
// Exactly one host: bright-secret-07-staging.authkit.app. Discovery
// refuses every other host.
func TestAllowedAuthHosts_Dev(t *testing.T) {
	got := AllowedAuthHosts()
	if len(got) != 1 {
		t.Fatalf("dev AllowedAuthHosts size = %d, want 1: %v", len(got), got)
	}
	if _, ok := got["bright-secret-07-staging.authkit.app"]; !ok {
		t.Errorf("dev AllowedAuthHosts missing bright-secret-07-staging.authkit.app: %v", got)
	}
}
