// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// clientVersionState is one of the four states both render surfaces are driven
// across. The last two are the pair that must render DIFFERENTLY: a refusal
// beside a good proof means the version really is too old, while a refusal
// beside a FAILED proof means the proof is broken and upgrading will not help.
type clientVersionState struct {
	name      string
	refusal   *clientver.Refusal
	proof     *clientver.ProofState
	wantEmpty bool
}

func clientVersionStates() []clientVersionState {
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	return []clientVersionState{
		{name: "neither set", wantEmpty: true},
		{
			name:  "proof succeeded, never refused",
			proof: &clientver.ProofState{At: at, OK: true, Version: "1.2.3", Platform: "linux-amd64"},
		},
		{
			name:    "refused with a good proof",
			refusal: &clientver.Refusal{Minimum: "2.0.0", ClientVersion: "1.0.0", Platform: "linux-amd64", UpgradeCommand: "knowledge install", Reason: clientver.ReasonBelowMinimum, At: at},
			proof:   &clientver.ProofState{At: at, OK: true, Version: "1.0.0", Platform: "linux-amd64"},
		},
		{
			name:    "refused with a FAILED proof",
			refusal: &clientver.Refusal{Minimum: "2.0.0", ClientVersion: "1.0.0", Platform: "linux-amd64", UpgradeCommand: "knowledge install", Reason: clientver.ReasonUnverified, At: at},
			proof:   &clientver.ProofState{At: at, OK: false, Version: "1.0.0", Platform: "linux-amd64", Err: "self-read handle not open"},
		},
	}
}

// applyClientVersionState installs a state process-wide and restores the empty
// refusal afterwards.
func applyClientVersionState(t *testing.T, s clientVersionState) {
	t.Helper()
	clientver.ClearRefusal()
	clientver.ClearProof()
	if s.refusal != nil {
		clientver.LatchRefusal(*s.refusal)
	}
	if s.proof != nil {
		clientver.RecordProof(*s.proof)
	}
	t.Cleanup(func() {
		clientver.ClearRefusal()
		clientver.ClearProof()
	})
}

// TestManageStatus_RendersMinimumVersionRefusal drives the manage(status)
// surface across all four states, in text and JSON, on BOTH assembly sites.
func TestManageStatus_RendersMinimumVersionRefusal(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 10, EdgeCount: 5}

	surfaces := []struct {
		name  string
		build func() ClientDeps
	}{
		{"handleServerStatus", func() ClientDeps {
			return &versionDeps{
				cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
				live:            fakeLiveness{status: runningStatusMap()},
				clientVer:       "1.0.0", daemonVer: "1.0.0", daemonKnown: true,
			}
		}},
		{"handleCloudStatus", func() ClientDeps {
			return &versionDeps{
				cloudStatusDeps: &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"},
				clientVer:       "1.0.0", daemonVer: "1.0.0", daemonKnown: true,
			}
		}},
	}

	// The U2-owned block's output must be byte-identical across all four states:
	// this block is additive beside renderVersionLines, and achieving the render
	// by editing that function is one of the implementations this gate rejects.
	baseline := renderVersionLines("1.0.0", "1.0.0", true, "", false)
	require.NotEmpty(t, baseline, "the invariance control must render something, or its constancy proves nothing")

	// THE STATE LOOP IS OUTERMOST, deliberately. The empty state is reachable
	// only before any proof has been recorded in this process: RecordProof has
	// no counterpart, because a proof state is a RECORD of what happened rather
	// than a verdict to retire, so nothing can put it back. Driving states
	// outermost is what lets BOTH surfaces be observed empty before the first
	// RecordProof, instead of leaving that to subtest ordering.
	bodies := map[string]string{}
	for _, s := range clientVersionStates() {
		for _, surface := range surfaces {
			t.Run(s.name+"/"+surface.name, func(t *testing.T) {
				applyClientVersionState(t, s)
				deps := surface.build()

				body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
				bodies[s.name+"/"+surface.name] = body

				var got map[string]any
				require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))

				assert.Equal(t, baseline, renderVersionLines("1.0.0", "1.0.0", true, "", false),
					"the version block this one sits beside must be byte-identical whatever this state is")

				if s.wantEmpty {
					assert.NotContains(t, body, "Client version state:",
						"a healthy client's status must be byte-identical to what it was before this block existed")
					assert.NotContains(t, got, "client_version_refusal",
						"the key must be ABSENT, not empty — an empty object reads as 'refused with no detail'")
					assert.NotContains(t, got, "client_version_proof")
					return
				}

				if s.refusal != nil {
					assert.Contains(t, body, s.refusal.Minimum)
					assert.Contains(t, body, s.refusal.ClientVersion)
					assert.Contains(t, body, s.refusal.Reason)
					assert.Contains(t, body, s.refusal.UpgradeCommand)
					raw, ok := got["client_version_refusal"].(map[string]any)
					require.True(t, ok, "client_version_refusal must be an object")
					assert.Equal(t, s.refusal.Reason, raw["reason"])
					assert.Equal(t, s.refusal.Minimum, raw["minimum"])
				} else {
					assert.NotContains(t, body, "Refused by the Fulminate gateway")
					assert.NotContains(t, got, "client_version_refusal")
				}

				if s.proof != nil {
					assert.Contains(t, body, "Version proof:")
					assert.Contains(t, body, s.proof.At.Format(time.RFC3339))
					raw, ok := got["client_version_proof"].(map[string]any)
					require.True(t, ok, "client_version_proof must be an object")
					assert.Equal(t, s.proof.OK, raw["ok"])
				}
			})
		}
	}

	// THE DISCRIMINATING PAIR, on BOTH surfaces. Collapsing these two into one
	// render is the wrong-but-compiling implementation that tells a user to
	// upgrade a client whose version was never the problem.
	for _, surface := range surfaces {
		assert.NotEqual(t, bodies["refused with a good proof/"+surface.name], bodies["refused with a FAILED proof/"+surface.name],
			"%s: a refusal beside a good proof and a refusal beside a failed proof must render differently — they have different remedies", surface.name)
		assert.NotEqual(t, bodies["neither set/"+surface.name], bodies["refused with a good proof/"+surface.name],
			"%s: the control — a refused client's render must differ from a healthy one's", surface.name)
	}
}
