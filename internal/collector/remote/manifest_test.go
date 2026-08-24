// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// manifest_test.go — the fail-closed trigger table's own gate.

// healthyManifestState is the state in which NO trigger fires: diff mode on, a
// well-formed manifest carrying an identity and this client's scheme version, an
// unchanged discovery surface and a complete walk. Every subtest below perturbs
// exactly ONE field of it, so a subtest that passes is evidence about its own
// trigger rather than about a state that was broken several ways at once.
func healthyManifestState() manifestState {
	return manifestState{
		mode: diffModeOn,
		resp: &knowledgev1.CollectManifestResponse{
			ManifestId:        "mid-1",
			HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
			Entries: []*knowledgev1.ManifestEntry{
				{FilePath: "pkg/a.go", ContributionHash: bytes.Repeat([]byte{1}, contribHashBytes)},
			},
		},
	}
}

// TestManifestFallback_EveryTriggerFires drives the trigger table directly.
//
// THE NEGATIVE CONTROLS ARE PART OF THE TABLE, NOT EXTRAS. A test that only
// counts FIRING triggers is satisfied by a table that fires on everything, and
// the optimization would be silently dead for every repository with every
// positive subtest still green. first_collect_empty_graph_does_not_fall_back
// carries the load-bearing half: an empty entries list must leave the diff
// ENGAGED, which is exactly what a retired trigger used to override.
func TestManifestFallback_EveryTriggerFires(t *testing.T) {
	// Control: the unperturbed state must NOT fall back, else every "this
	// perturbation fires" assertion below would be satisfied by a table that
	// always fires.
	_, fell := evaluateManifestFallback(healthyManifestState())
	require.False(t, fell, "control: the healthy state must not fall back")

	cases := []struct {
		name    string
		mutate  func(*manifestState)
		want    fallbackReason
		wantAny bool
	}{
		{
			name: "kill_switch",
			// KEYED ON THE LEVER, not the mode: under ship-armed, off resolves to
			// diffModeShadow like a deliberate shadow request and a typo do, so a
			// mode-keyed mutation can no longer express "a human pulled the switch".
			mutate:  func(s *manifestState) { s.lever = leverOff },
			want:    fallbackKillSwitch,
			wantAny: true,
		},
		{
			name:    "scheme_mismatch",
			mutate:  func(s *manifestState) { s.resp.HashSchemeVersion = contribhash.ContributionHashSchemeVersion + 1 },
			want:    fallbackSchemeMismatch,
			wantAny: true,
		},
		{
			name:    "discovery_mode_change",
			mutate:  func(s *manifestState) { s.discoveryChanged = true },
			want:    fallbackDiscoveryModeChange,
			wantAny: true,
		},
		{
			// THE DECISIVE NEGATIVE. An empty entries list — from ANY server state,
			// since the wire no longer distinguishes a genuinely empty graph from one
			// holding only fileless rows — leaves the diff ENGAGED. Everything is new
			// and there is nothing to fall back from.
			name: "first_collect_empty_graph_does_not_fall_back",
			mutate: func(s *manifestState) {
				s.resp.Entries = nil
			},
			wantAny: false,
		},
		{
			// THE SECOND DECISIVE NEGATIVE, and the only catcher for the realistic
			// way this feature dies quietly.
			//
			// BOTH fingerprints are computed THROUGH THE PRODUCTION FUNCTION from
			// two INDEPENDENTLY CONSTRUCTED but identically configured inputs.
			// Reusing one string on both sides would make this vacuous: it would
			// pass against a fingerprint embedding a timestamp, an absolute path or
			// an unsorted map iteration — and such a fingerprint differs on every
			// collect, trips this trigger every time, and leaves diff mode
			// PERMANENTLY OFF for every repository with every gate still green.
			name: "same_discovery_fingerprint_does_not_fall_back",
			mutate: func(s *manifestState) {
				optsA := parser.DiscoveryOptions{
					PackagePrefixes: []string{"cmd", "internal"}, LiftExclusions: false,
				}
				// Deliberately built in a DIFFERENT ORDER from optsA: an
				// implementation that digested the slice as given rather than
				// sorted would differ here, which is exactly the defect this
				// subtest exists to catch.
				optsB := parser.DiscoveryOptions{
					PackagePrefixes: []string{"internal", "cmd"}, LiftExclusions: false,
				}
				fpA := parser.DiscoveryFingerprint(parser.DiscoveryPathGit, optsA)
				fpB := parser.DiscoveryFingerprint(parser.DiscoveryPathGit, optsB)
				if fpA != fpB {
					panic("discovery fingerprint is NOT deterministic across identical configurations: " +
						fpA + " != " + fpB)
				}
				// The comparison the production path makes, driven by the two
				// independently derived values rather than asserted.
				s.discoveryChanged = fpA != fpB
			},
			wantAny: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyManifestState()
			tc.mutate(&s)
			got, fell := evaluateManifestFallback(s)
			require.Equal(t, tc.wantAny, fell, "fallback firing for %s", tc.name)
			if tc.wantAny {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

// errFetchFailedForTest stands in for a transport failure.
var errFetchFailedForTest = errTestSentinel("manifest fetch failed")

type errTestSentinel string

func (e errTestSentinel) Error() string { return string(e) }

// TestDiffEligibleGraph_OnlyCodeIsEligible pins the graph-family gate. The web
// case is the one that matters: a budget-bounded re-crawl legitimately
// re-materializes only a subset of its previous paths, and every deletion guard
// would admit that subset as deliberate.
func TestDiffEligibleGraph_OnlyCodeIsEligible(t *testing.T) {
	require.True(t, diffEligibleGraph(kgtypes.GraphCode))
	for _, gt := range []kgtypes.GraphType{
		kgtypes.GraphWebRaw, kgtypes.GraphKnowledge, kgtypes.GraphCloud, kgtypes.GraphCICD,
	} {
		require.False(t, diffEligibleGraph(gt), "%s must never take the diff path", gt)
	}
}

// TestCollectDiffMode_UnsetIsArmed pins the SHIPPED default: with no lever set
// at all, a collect resolves ARMED. The diff is the collect, not an opt-in.
//
// EVERY ROW ASSERTS THE LEVER AS WELL AS THE MODE, because four of the five
// valid rows share a mode and the lever is the only thing that separates them —
// the mode is lossy exactly where the kill switch needs to be visible.
func TestCollectDiffMode_UnsetIsArmed(t *testing.T) {
	t.Run("unset_is_armed", func(t *testing.T) {
		// t.Setenv("") sets it EMPTY, which is a DIFFERENT row; unset it for real
		// and let t.Setenv's cleanup restore whatever the environment had.
		t.Setenv(collectDiffEnv, "")
		require.NoError(t, os.Unsetenv(collectDiffEnv))
		mode, lever, err := collectDiffMode()
		require.NoError(t, err, "absence of input is not bad input")
		require.Equal(t, diffModeOn, mode, "unset is ARMED — this is the shipped default")
		require.Equal(t, leverUnset, lever)
	})
	t.Run("set_but_empty_is_armed", func(t *testing.T) {
		t.Setenv(collectDiffEnv, "")
		mode, lever, err := collectDiffMode()
		require.NoError(t, err, "a cleared override is the operator saying 'no override', deliberately")
		require.Equal(t, diffModeOn, mode, "a cleared override is not a typo")
		require.Equal(t, leverEmpty, lever, "empty keeps its own lever value")
	})
	t.Run("on_is_case_insensitive", func(t *testing.T) {
		t.Setenv(collectDiffEnv, "ON")
		mode, lever, err := collectDiffMode()
		require.NoError(t, err)
		require.Equal(t, diffModeOn, mode)
		require.Equal(t, leverOn, lever)
	})
	t.Run("shadow_is_trimmed", func(t *testing.T) {
		t.Setenv(collectDiffEnv, " shadow ")
		mode, lever, err := collectDiffMode()
		require.NoError(t, err)
		require.Equal(t, diffModeShadow, mode)
		require.Equal(t, leverShadow, lever, "a deliberate shadow request is not the kill switch")
	})
	t.Run("off_is_the_kill_switch", func(t *testing.T) {
		t.Setenv(collectDiffEnv, "off")
		mode, lever, err := collectDiffMode()
		require.NoError(t, err, "the kill switch is a VALID deliberate value, never bad input")
		require.Equal(t, diffModeShadow, mode, "the kill switch degrades to SHADOW, not off")
		require.Equal(t, leverOff, lever, "and it is the LEVER that identifies it")
	})
	// THE ONLY ROW THAT REFUSES. A present, meaningless value errors the collect
	// before anything else runs — it is not a sixth resolved state.
	t.Run("unrecognized_value_errors", func(t *testing.T) {
		t.Setenv(collectDiffEnv, "enabled")
		mode, lever, err := collectDiffMode()
		require.Error(t, err, "a present, meaningless value ERRORS — it never degrades to a lane")
		require.Contains(t, err.Error(), "enabled",
			"the error must carry the RAW VALUE — 'it errored' alone leaves the operator hunting their typo exactly as stuck as the silent degrade did")
		require.Contains(t, err.Error(), collectDiffEnv, "and name the variable that carried it")
		for _, valid := range []diffMode{diffModeOn, diffModeShadow, diffModeOff} {
			require.Contains(t, err.Error(), string(valid),
				"and name the valid vocabulary, so the message is actionable rather than merely loud")
		}
		require.Equal(t, diffMode(""), mode,
			"the error path returns ZERO VALUES — a caller that drops the error must not get a plausible-looking pair")
		require.Equal(t, diffLever(""), lever)
	})
}
