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
// Exactly one host: auth.fulminate.io. No staging, no localhost, no
// arbitrary entries — discovery refuses every other host.
func TestAllowedAuthHosts_Release(t *testing.T) {
	got := AllowedAuthHosts()
	if len(got) != 1 {
		t.Fatalf("release AllowedAuthHosts size = %d, want 1: %v", len(got), got)
	}
	if _, ok := got["auth.fulminate.io"]; !ok {
		t.Errorf("release AllowedAuthHosts missing auth.fulminate.io: %v", got)
	}
}
