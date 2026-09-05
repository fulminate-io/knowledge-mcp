// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestVersionOutput_RendersMinimumVersionRefusal drives the `knowledge version`
// surface across the same four states the manage(status) gate drives.
//
// It is a SEPARATE test against a SEPARATE renderer on purpose: renderVersionOutput
// lives in this package and shares only the skew line with the manage surface, so
// a manage-side fix cannot reach it. This is the command a user runs after being
// told to upgrade, which makes its bare output the most expensive omission.
func TestVersionOutput_RendersMinimumVersionRefusal(t *testing.T) {
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		refusal   *clientver.Refusal
		proof     *clientver.ProofState
		wantEmpty bool
	}{
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
		{
			// THE REFUSAL THIS CLIENT COULD NOT READ, and the only case where the
			// minimum and the remedy are genuinely unknown — every other field on
			// this line renders as "(unknown)", which is exactly when the
			// diagnostic is the only thing on screen a reader can act on. An
			// EMPTY Minimum is what makes it that case rather than a fifth
			// ordinary refusal.
			name: "refused with a body this client could not read",
			refusal: &clientver.Refusal{
				ClientVersion: "1.0.0", Platform: "linux-amd64",
				Reason:     clientver.ReasonRefusalBodyUnparseable,
				Diagnostic: "on the connect transport the body arrived under content-encoding gzip, 129 bytes, and it is not JSON; it begins \"\\x1f\\x8b\"",
				At:         at,
			},
		},
	}

	// The skew line is the U2-owned neighbor on this surface. Driven with
	// DIFFERING stamps so it actually renders — an invariance claim about a line
	// that never appears would be vacuous.
	skewLine, skewed := graphclient.VersionSkewLine("1.0.0", "1.0.1")
	if !skewed {
		t.Fatalf("the neighbor control did not render a skew line, so its invariance proves nothing")
	}

	// The baseline the empty state must reproduce byte for byte. Both records
	// are cleared first so this is order-independent: another test in this
	// package may already have recorded a proof.
	clientver.ClearRefusal()
	clientver.ClearProof()
	baseline := renderVersionOutput("1.0.0", "1.0.1", true, "", false)

	bodies := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientver.ClearRefusal()
			clientver.ClearProof()
			if tc.refusal != nil {
				clientver.LatchRefusal(*tc.refusal)
			}
			if tc.proof != nil {
				clientver.RecordProof(*tc.proof)
			}
			t.Cleanup(func() {
				clientver.ClearRefusal()
				clientver.ClearProof()
			})

			out := renderVersionOutput("1.0.0", "1.0.1", true, "", false)
			bodies[tc.name] = out

			if !strings.Contains(out, skewLine) {
				t.Errorf("the U2-owned skew line is missing or altered; this block must be additive beside it:\n%s", out)
			}
			if !strings.Contains(out, "knowledge 1.0.0\n") || !strings.Contains(out, "server 1.0.1\n") {
				t.Errorf("the existing version lines were disturbed:\n%s", out)
			}

			if tc.wantEmpty {
				if out != baseline {
					t.Errorf("a client with neither a refusal nor a proof must render byte-identically to before:\ngot:  %q\nwant: %q", out, baseline)
				}
				return
			}

			if tc.refusal != nil {
				for _, want := range []string{tc.refusal.Minimum, tc.refusal.ClientVersion, tc.refusal.Reason, tc.refusal.UpgradeCommand} {
					if !strings.Contains(out, want) {
						t.Errorf("the refusal render omits %q, so the user has no remedy:\n%s", want, out)
					}
				}
				// THE DIAGNOSTIC IS THE DELIVERABLE ON THE UNREADABLE-BODY CASE.
				// Deleting the render's `if refusal.Diagnostic != ""` block left
				// this whole package green before this assertion existed, because
				// no case set the field.
				if tc.refusal.Diagnostic != "" && !strings.Contains(out, tc.refusal.Diagnostic) {
					t.Errorf("the render drops the diagnostic, which on this case is the only line a user can act on — every other field is (unknown):\n%s", out)
				}
				if tc.refusal.Diagnostic == "" && strings.Contains(out, "could not read the refusal") {
					t.Errorf("a refusal the gateway explained must not claim this client could not read it:\n%s", out)
				}
			} else if strings.Contains(out, "refused by the Fulminate gateway") {
				t.Errorf("a client that was never refused must not render a refusal:\n%s", out)
			}

			if tc.proof != nil {
				if !strings.Contains(out, "version proof:") {
					t.Errorf("the proof state must render its outcome:\n%s", out)
				}
				if !strings.Contains(out, at.Format(time.RFC3339)) {
					t.Errorf("the proof state must render when it ran:\n%s", out)
				}
			}
		})
	}

	// THE DISCRIMINATING PAIR: the two refused states send a user to different
	// remedies, so they must not collapse into one render.
	if bodies["refused with a good proof"] == bodies["refused with a FAILED proof"] {
		t.Errorf("a refusal beside a good proof and a refusal beside a failed proof render identically; they have different remedies")
	}
	if bodies["neither set"] == bodies["refused with a good proof"] {
		t.Errorf("the control: a refused client's output must differ from a healthy one's")
	}
	// The second discriminating pair: a refusal the gateway explained and one this
	// client could not read send a user to different places — upgrade, versus
	// report a client that cannot read its own refusals — so they must not render
	// the same. Without this, an implementation that dropped the diagnostic line
	// would still satisfy the containment check above on every OTHER case.
	if bodies["refused with a body this client could not read"] == bodies["refused with a good proof"] {
		t.Errorf("an unreadable-body refusal and an ordinary one render identically; the first has no minimum and no remedy to act on")
	}
}
