// SPDX-License-Identifier: Apache-2.0

//go:build !devendpoint

package cli

import "testing"

// TestCloudEndpoint_Release asserts the production cloud host. Pinned
// by build tag — there is no runtime override (memory:
// feedback_no_endpoint_override). The devendpoint counterpart asserts
// the dev URL.
func TestCloudEndpoint_Release(t *testing.T) {
	if CloudEndpoint != "https://fulminate.io" {
		t.Errorf("release CloudEndpoint = %q, want https://fulminate.io", CloudEndpoint)
	}
}

// TestAllowedAuthHosts_Release asserts the production AuthKit allowlist.
// Exactly the two Fulminate-controlled WorkOS custom domains:
// signin.fulminate.io (the authorization server prod publishes — its
// absence made every v0.6.3 binary refuse prod login) and
// auth.fulminate.io (the WorkOS custom API domain). No staging, no
// localhost, no arbitrary entries — discovery refuses every other host.
func TestAllowedAuthHosts_Release(t *testing.T) {
	got := AllowedAuthHosts()
	if len(got) != 2 {
		t.Fatalf("release AllowedAuthHosts size = %d, want 2: %v", len(got), got)
	}
	for _, host := range []string{"signin.fulminate.io", "auth.fulminate.io"} {
		if _, ok := got[host]; !ok {
			t.Errorf("release AllowedAuthHosts missing %s: %v", host, got)
		}
	}
}
