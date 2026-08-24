// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// indexer_discover_fingerprint_test.go — DETERMINISM is the fingerprint's whole
// property, so it gets its own gate at the declaration as well as the one at its
// consumer.
//
// A fingerprint that varied run-to-run would differ on every collect, trip the
// discovery-change fallback every time, and leave the incremental diff
// permanently disarmed for every repository — with every other gate green. That
// is the realistic way this feature dies quietly.

// TestDiscoveryFingerprint_DeterministicAcrossIdenticalConfigs asserts equality
// for identically-configured inputs constructed independently, INCLUDING prefix
// slices given in different orders.
func TestDiscoveryFingerprint_DeterministicAcrossIdenticalConfigs(t *testing.T) {
	a := DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{
		PackagePrefixes: []string{"cmd", "internal", "docs"},
	})
	b := DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{
		PackagePrefixes: []string{"docs", "cmd", "internal"},
	})
	require.Equal(t, a, b, "prefix ORDER must not move the fingerprint — it is sorted before digesting")

	// Repeated calls with the same input must also agree: a digest over a map in
	// iteration order, or one seeded with a timestamp, fails here.
	require.Equal(t, a, DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{
		PackagePrefixes: []string{"cmd", "internal", "docs"},
	}), "the same configuration must digest identically on every call")
}

// TestDiscoveryFingerprint_DistinguishesEveryConfiguredAxis is the KNOWN-POSITIVE
// CONTROL for the test above: without it, a function returning a constant would
// satisfy every equality assertion while detecting no configuration change at
// all — the exact failure that would let a scoped collect name out-of-scope files
// as deletions.
func TestDiscoveryFingerprint_DistinguishesEveryConfiguredAxis(t *testing.T) {
	base := DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{PackagePrefixes: []string{"cmd"}})

	for _, tc := range []struct {
		name string
		got  string
	}{
		{"discovery path", DiscoveryFingerprint(DiscoveryPathWalk, DiscoveryOptions{PackagePrefixes: []string{"cmd"}})},
		{"lifted exclusions", DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{
			PackagePrefixes: []string{"cmd"}, LiftExclusions: true,
		})},
		{"different prefix", DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{PackagePrefixes: []string{"internal"}})},
		{"additional prefix", DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{
			PackagePrefixes: []string{"cmd", "internal"},
		})},
		{"no prefixes at all", DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{})},
	} {
		require.NotEqual(t, base, tc.got, "a change to %s MUST move the fingerprint", tc.name)
	}

	// The separator must not let one prefix impersonate two: "a\x1fb" as a single
	// prefix and {"a","b"} as two are different configurations and must digest
	// differently. A comma- or space-joined encoding fails this.
	single := DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{PackagePrefixes: []string{"a\x1fb"}})
	pair := DiscoveryFingerprint(DiscoveryPathGit, DiscoveryOptions{PackagePrefixes: []string{"a", "b"}})
	require.NotEqual(t, single, pair, "the prefix separator must be unambiguous")
}
