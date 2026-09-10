// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// identity_test.go — the TWO PRODUCER STAMPS a registered collect owes the
// incremental diff, driven through the REAL MCP path against the stub provider.
//
// THE STAMPS ARE NOT COSMETIC. Without them the sink refuses a registered custom
// collect outright: an empty DiscoveryFingerprint aborts before the manifest
// fetch and a zero CollectorOutputVersion aborts just after, so the family could
// not reach the diff at all. These tests drive RunMCP end to end so the property
// is asserted about the result a real provider call produces, not about a
// hand-built struct.

// TestRunMCP_StampsBothProducerIdentities is R6 through the real provider: a
// result returned by the MCP path carries both stamps, non-empty and non-zero.
func TestRunMCP_StampsBothProducerIdentities(t *testing.T) {
	res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeConforming), nil, "board", nil)
	require.NoError(t, err)

	assert.NotEmpty(t, res.DiscoveryFingerprint,
		"an empty fingerprint aborts the collect before the manifest fetch — the family could not diff at all")
	assert.NotZero(t, res.CollectorOutputVersion,
		"a zero version aborts just after the fetch: zero means UNSTAMPED, never 'unchanged'")
	assert.True(t, res.WalkComplete,
		"the provider's completeness assertion rides through, and it is what arms the deletion phase")
}

// TestRunMCP_StampsAreStableAcrossCollects is the property the DIFF depends on,
// and it is a different assertion from "the stamps are set".
//
// THE BASELINE COMPARISON IS AN EQUALITY. Both stamps are compared against what
// the previous collect of this graph recorded, so a value that moved between two
// identical collects fires a fail-closed trigger and forces a full upload —
// every time, forever. A stamp derived from a clock, a map iteration or a
// per-process nonce would satisfy the test above and defeat the whole feature.
func TestRunMCP_StampsAreStableAcrossCollects(t *testing.T) {
	def := stdioDef(t, stubModeConforming)
	first, _, err := RunMCP(context.Background(), def, nil, "board", nil)
	require.NoError(t, err)
	second, _, err := RunMCP(context.Background(), def, nil, "board", nil)
	require.NoError(t, err)

	assert.Equal(t, first.DiscoveryFingerprint, second.DiscoveryFingerprint,
		"two identical collects must present the SAME fingerprint, or the discovery trigger fires on every collect")
	assert.Equal(t, first.CollectorOutputVersion, second.CollectorOutputVersion,
		"and the SAME collector identity, or every collect re-lands the graph in full with the echo suppressed")
}

// TestProviderIdentity_MovesWithTheProvider pins what the collector-version
// stamp COVERS. Each row changes one thing about the registration and asserts
// the identity moves; the last asserts it does NOT move for a change outside the
// provider's identity.
//
// WHY THIS MATTERS: the identity is the custom family's analog of the code
// collector's hand-bumped output version, and it is the ONLY signal for a class
// of change no contribution hash can see — node ids, summaries and metadata all
// move without moving any hashed field.
func TestProviderIdentity_MovesWithTheProvider(t *testing.T) {
	base := func() *knowledgev1.GraphTypeDef {
		return &knowledgev1.GraphTypeDef{
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool: "collect_graph",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/usr/bin/provider", Args: []string{"--serve"}, Env: []string{"TOKEN=secret-v1"},
				}},
			},
		}
	}
	baseline, err := providerIdentity(base())
	require.NoError(t, err)
	require.NotZero(t, baseline)

	for _, tc := range []struct {
		name   string
		mutate func(d *knowledgev1.GraphTypeDef)
	}{
		{"a different command", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.GetStdio().Command = "/usr/bin/other-provider"
		}},
		{"different args", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.GetStdio().Args = []string{"--serve", "--v2"}
		}},
		{"a different env key", func(d *knowledgev1.GraphTypeDef) {
			// The block's keys decide what the provider can SEE, so a different
			// credential or endpoint variable legitimately produces a different graph
			// — and that difference moves no row's hashed bytes.
			d.Collector.GetStdio().Env = []string{"OTHER_TOKEN=t"}
		}},
		{"a different tool on the same provider", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.Tool = "collect_other_graph"
		}},
		{"a different registration name", func(d *knowledgev1.GraphTypeDef) {
			d.Name = "notion-workspace"
		}},
		{"an http transport instead of stdio", func(d *knowledgev1.GraphTypeDef) {
			d.Collector.Provider = &knowledgev1.CollectorSpec_Http{
				Http: &knowledgev1.HttpProvider{Url: "https://example.invalid/mcp"},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.mutate(d)
			got, err := providerIdentity(d)
			require.NoError(t, err)
			assert.NotEqual(t, baseline, got,
				"the identity must move, or a re-registration is invisible and the graph keeps rows the new provider would not emit")
		})
	}

	t.Run("an identical record yields an identical identity", func(t *testing.T) {
		again, err := providerIdentity(base())
		require.NoError(t, err)
		assert.Equal(t, baseline, again,
			"the KNOWN-NEGATIVE: a stamp that moved for an unchanged record would force a full re-land on every collect")
	})

	t.Run("a record with no provider transport is an error, not a shared identity", func(t *testing.T) {
		d := base()
		d.Collector.Provider = nil
		_, err := providerIdentity(d)
		require.Error(t, err,
			"digesting the empty case would give every provider-less record ONE shared identity")
	})
}

// TestProviderIdentity_RotatingAValueDoesNotMoveIt is the row the config-file
// contract adds, and it has no pre-change counterpart: the retired record's env
// carried NAMES only, so there was no value to rotate.
//
// WHY IT MATTERS: the identity is compared against the previous collect's, and a
// value that moved forces a FULL re-land of the graph with the manifest echo
// suppressed. A credential rotation changes no row's bytes, so re-landing for
// one is pure cost.
//
// THE HTTP ARM IS THE CONTROL, in the same table. It rotates a header value and
// its identity is likewise unchanged — but for a different reason (that arm
// folds only the URL, and headers never reach the record at all), so the two
// halves together show the stdio arm has been brought into line with an arm that
// already had the property rather than merely edited.
func TestProviderIdentity_RotatingAValueDoesNotMoveIt(t *testing.T) {
	stdio := func(value string) *knowledgev1.GraphTypeDef {
		return &knowledgev1.GraphTypeDef{
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool: "collect_graph",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/usr/bin/provider", Env: []string{"TOKEN=" + value},
				}},
			},
		}
	}
	before, err := providerIdentity(stdio("secret-v1"))
	require.NoError(t, err)
	after, err := providerIdentity(stdio("secret-v2-rotated"))
	require.NoError(t, err)
	assert.Equal(t, before, after,
		"rotating a credential must NOT move the collector identity: it re-lands the whole graph for a change that alters no row's bytes")

	t.Run("CONTROL: the http arm has the same property", func(t *testing.T) {
		httpDef := &knowledgev1.GraphTypeDef{
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool:     "collect_graph",
				Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: "https://p.example/mcp"}},
			},
		}
		first, err := providerIdentity(httpDef)
		require.NoError(t, err)
		// The entry's headers ride BESIDE the record and never enter it, so there is
		// nothing on this record a header rotation could change. That is the
		// asymmetry stated as an observation rather than left implicit.
		second, err := providerIdentity(httpDef)
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})
}

// TestProviderIdentity_AddingOrRemovingAKeyMovesIt is the half a lazy fix would
// lose. Dropping the env from the fold entirely would satisfy the rotation row
// above and blind the stamp to a real change: what the provider can SEE has
// changed, and no contribution hash over the collected rows can detect it.
func TestProviderIdentity_AddingOrRemovingAKeyMovesIt(t *testing.T) {
	withEnv := func(pairs ...string) *knowledgev1.GraphTypeDef {
		return &knowledgev1.GraphTypeDef{
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool: "collect_graph",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/usr/bin/provider", Env: pairs,
				}},
			},
		}
	}
	baseline, err := providerIdentity(withEnv("TOKEN=t"))
	require.NoError(t, err)

	added, err := providerIdentity(withEnv("TOKEN=t", "REGION=us-east-1"))
	require.NoError(t, err)
	assert.NotEqual(t, baseline, added, "adding a variable the provider can read must move the identity")

	removed, err := providerIdentity(withEnv())
	require.NoError(t, err)
	assert.NotEqual(t, baseline, removed, "and removing one must move it too")
	assert.NotEqual(t, added, removed)
}

// TestProviderIdentity_IsStableAcrossRepeatedComputation is the SORTING row.
//
// THE BLOCK IS A GO MAP UPSTREAM and Go map iteration order is randomized, so an
// unsorted fold gives one unchanged entry a different identity on every collect
// and re-lands its graph every time. This row drives the fold with the pairs in
// two different orders, which is what a randomized map would produce across two
// collects, and asserts one value.
func TestProviderIdentity_IsStableAcrossRepeatedComputation(t *testing.T) {
	withEnv := func(pairs ...string) *knowledgev1.GraphTypeDef {
		return &knowledgev1.GraphTypeDef{
			Name: "jira",
			Collector: &knowledgev1.CollectorSpec{
				Tool: "collect_graph",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/usr/bin/provider", Env: pairs,
				}},
			},
		}
	}
	first, err := providerIdentity(withEnv("ALPHA=1", "BETA=2", "GAMMA=3"))
	require.NoError(t, err)
	shuffled, err := providerIdentity(withEnv("GAMMA=3", "ALPHA=1", "BETA=2"))
	require.NoError(t, err)
	assert.Equal(t, first, shuffled,
		"the identity must be a function of the SET of keys, not of the order a map iteration happened to yield")

	again, err := providerIdentity(withEnv("ALPHA=1", "BETA=2", "GAMMA=3"))
	require.NoError(t, err)
	assert.Equal(t, first, again)
}

// TestDiscoveryFingerprint_MovesWithTheParams pins what the fingerprint covers:
// the configuration this collect ASKED its provider for.
//
// IT GUARDS A DESTRUCTIVE PATH. A collect scoped by params emits nothing for the
// records outside that scope, and every deletion guard would admit naming them —
// the walk was complete, the ratio is ordinary, each named id has a live row.
// Comparing this value against the previous collect's is what refuses that.
func TestDiscoveryFingerprint_MovesWithTheParams(t *testing.T) {
	none, err := discoveryFingerprint(nil)
	require.NoError(t, err)
	require.NotEmpty(t, none,
		"NO params is itself a discovery configuration, and an empty return would trip the sink's unstamped abort")

	scoped, err := discoveryFingerprint(map[string]any{"project": "ACME"})
	require.NoError(t, err)
	assert.NotEqual(t, none, scoped, "scoping the collect must move the fingerprint")

	rescoped, err := discoveryFingerprint(map[string]any{"project": "OTHER"})
	require.NoError(t, err)
	assert.NotEqual(t, scoped, rescoped, "and so must re-scoping it")

	again, err := discoveryFingerprint(map[string]any{"project": "ACME"})
	require.NoError(t, err)
	assert.Equal(t, scoped, again,
		"the KNOWN-NEGATIVE: identical params must give an identical value, or the trigger fires forever")

	t.Run("key order does not move it", func(t *testing.T) {
		a, err := discoveryFingerprint(map[string]any{"project": "ACME", "since": "2026-01-01"})
		require.NoError(t, err)
		b, err := discoveryFingerprint(map[string]any{"since": "2026-01-01", "project": "ACME"})
		require.NoError(t, err)
		assert.Equal(t, a, b,
			"the digest is a function of the PARAMS, not of Go's randomized map iteration order")
	})
}
