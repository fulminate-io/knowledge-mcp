// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_triggers_test.go — the fail-closed table driven through the UPLOAD
// path, one perturbation at a time off a shared healthy fixture.
//
// THIS IS NOT THE SAME TEST AS TestManifestFallback_EveryTriggerFires. That one
// drives evaluateManifestFallback directly and carries FIVE subtests. This one
// drives what a collect actually SENDS and carries SIX — the same THREE triggers and two negative controls,
// plus the trigger-record subtest that is the only observable separating a wired
// kill-switch predicate from an unwired one.

// capturingSlog captures every emitted record so a test can assert on a log line
// the production code emits. Same idiom as installCapturingSlog in the auth
// package, which is unexported there.
type capturingSlog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingSlog) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingSlog) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec.Clone())
	return nil
}

func (h *capturingSlog) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingSlog) WithGroup(string) slog.Handler { return h }

// countAt returns how many captured records at level carry substr in their
// message.
func (h *capturingSlog) countAt(level slog.Level, substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, rec := range h.records {
		if rec.Level == level && strings.Contains(rec.Message, substr) {
			n++
		}
	}
	return n
}

// attrCountAt returns how many captured records at level carry an attribute
// named key whose rendered value contains substr.
//
// IT INSPECTS THE ATTRIBUTE RATHER THAN THE MESSAGE because the message is the
// same for every graph: what makes a withheld-baseline warning actionable is the
// identity it names, and only an attribute-level assertion can catch a line that
// says the right thing about nothing in particular.
func (h *capturingSlog) attrCountAt(level slog.Level, key, substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, rec := range h.records {
		if rec.Level != level {
			continue
		}
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == key && strings.Contains(a.Value.String(), substr) {
				n++
				return false
			}
			return true
		})
	}
	return n
}

// recordedFallback returns the reason attribute of the first fallback line
// logManifestFallback emitted, and whether any was emitted at all.
//
// THE ABSENCE ANSWER IS THE POINT. The two negative controls assert nothing was
// recorded, and the kill-switch record subtest asserts a specific name WAS — an
// unwired predicate is silent, so silence has to be distinguishable from a name.
func (h *capturingSlog) recordedFallback() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if !strings.Contains(rec.Message, "falling back to a full collect") {
			continue
		}
		reason := ""
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "reason" {
				reason = a.Value.String()
				return false
			}
			return true
		})
		return reason, true
	}
	return "", false
}

func installCapturingSlog(t *testing.T) *capturingSlog {
	t.Helper()
	h := &capturingSlog{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// resolveModeFor runs the client half exactly as WriteResult does — same fetch,
// same present set — and returns the mode the collect RESOLVED TO.
//
// IT IS CALLED BEFORE WriteResult, never after: an armed collect filters
// result.Nodes in place, so a second read of the same result would be measuring
// the leftovers of the first.
func resolveModeFor(
	t *testing.T, client knowledgev1connect.IngestServiceClient, result *collectorwire.CollectResult,
) diffMode {
	t.Helper()
	// A fetch failure now ABORTS in WriteResult rather than reaching the trigger
	// table, so this helper — which models the surviving degrade path — requires
	// the fetch to have succeeded.
	resp, err := fetchManifestWith(context.Background(), client, result)
	require.NoError(t, err)
	present := contribhash.FileContributionHashes(result.Nodes, result.Edges)
	// The lever is resolved by the CALLER exactly as WriteResult does it, so this
	// helper mirrors the production path rather than re-reading the environment
	// inside applyCollectDiff.
	lvMode, lever, lvErr := collectDiffMode()
	require.NoError(t, lvErr)
	var outcome collectDiffOutcome
	mode, _, dErr := NewUploadSink(client).applyCollectDiff(result, lvMode, lever, present, resp, &outcome)
	require.NoError(t, dErr, "the discovery store must be healthy for a trigger row to be attributable")
	return mode
}

// uploadedNodeCount sums the nodes across every captured chunk.
func uploadedNodeCount(rec *recordingIngest) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, c := range rec.chunks {
		n += len(c.GetNodes())
	}
	return n
}

// triggerCase is one perturbation of the shared healthy fixture.
type triggerCase struct {
	name string
	// perturb changes EXACTLY ONE thing, so a fired trigger can only be this one.
	perturb func(t *testing.T, result *collectorwire.CollectResult, rec *recordingIngest)
	// wantReason is the trigger expected to be recorded; empty means none may be.
	wantReason fallbackReason
	wantMode   diffMode
	// wantFullUpload asserts the degraded lane put the WHOLE node set on the wire.
	// The negative controls leave it false deliberately: against a matching
	// manifest an armed collect and a degraded one over an empty graph can upload
	// identical payloads, so a count assertion there would pass in both worlds.
	wantFullUpload bool
}

func triggerCases() []triggerCase {
	return []triggerCase{{
		name: string(fallbackSchemeMismatch),
		perturb: func(_ *testing.T, _ *collectorwire.CollectResult, rec *recordingIngest) {
			rec.manifest.HashSchemeVersion = contribhash.ContributionHashSchemeVersion + 1
		},
		wantReason: fallbackSchemeMismatch, wantMode: diffModeOff, wantFullUpload: true,
	}, {
		// THE SCOPE-CHANGE BRANCH, which is the whole trigger now: an EMPTY
		// fingerprint names our own producer regressing and aborts in WriteResult
		// instead. Perturbing the recorded signature is what a re-scoped collect
		// does to this predicate.
		name: string(fallbackDiscoveryModeChange),
		perturb: func(_ *testing.T, result *collectorwire.CollectResult, _ *recordingIngest) {
			result.DiscoveryFingerprint = "fp-a-different-discovery-configuration"
		},
		wantReason: fallbackDiscoveryModeChange, wantMode: diffModeOff, wantFullUpload: true,
	}, {
		// THE ONLY TRIGGER A HUMAN FIRES, and the only one targeting shadow rather
		// than plain full. Both upload everything, so the mode is what separates
		// them and an uploaded-count assertion alone could not.
		name: string(fallbackKillSwitch),
		perturb: func(t *testing.T, _ *collectorwire.CollectResult, _ *recordingIngest) {
			t.Setenv(collectDiffEnv, "off")
		},
		wantReason: fallbackKillSwitch, wantMode: diffModeShadow, wantFullUpload: true,
	}, {
		// THE ONLY TRIGGER THAT ALSO SUPPRESSES THE SERVER'S DECLINE. It fires for a
		// collector whose EMITTED OUTPUT moved in ways no per-file hash can see, so
		// the forced upload has to actually re-land rather than be declined file by
		// file. Perturbing the carried version is what an upgraded binary does to
		// this predicate.
		name: string(fallbackCollectorVersionChange),
		perturb: func(_ *testing.T, result *collectorwire.CollectResult, _ *recordingIngest) {
			result.CollectorOutputVersion = testCollectorOutputVersion + 1
		},
		wantReason: fallbackCollectorVersionChange, wantMode: diffModeOff, wantFullUpload: true,
	}, {
		// NEGATIVE CONTROL. An empty entries list — from ANY server state, since the
		// wire no longer distinguishes a genuinely empty graph from one holding only
		// fileless rows — leaves the diff ENGAGED. Asserting the uploaded count here
		// would prove nothing: against an empty manifest the changed set is every
		// present file either way.
		name: "first_collect_empty_graph_does_not_fall_back",
		perturb: func(_ *testing.T, _ *collectorwire.CollectResult, rec *recordingIngest) {
			rec.manifest.Entries = nil
		},
		wantMode: diffModeOn,
	}, {
		// NEGATIVE CONTROL. Handled entirely by the harness, which seeds the store
		// from an INDEPENDENTLY CONSTRUCTED but identically configured result
		// through the production signature function — see runTriggerCase.
		name:     "same_discovery_fingerprint_does_not_fall_back",
		perturb:  func(*testing.T, *collectorwire.CollectResult, *recordingIngest) {},
		wantMode: diffModeOn,
	}}
}

// runTriggerCase stands up the shared healthy fixture, applies one perturbation,
// and drives a whole collect through WriteResult.
//
// THE STORE IS SEEDED FROM A SECOND, INDEPENDENTLY BUILT RESULT rather than from
// the one under test. Reusing one signature string would pass even against a
// NONDETERMINISTIC signature — which would trip the discovery trigger on every
// collect and leave every collect permanently degraded, the exact defect the
// same_discovery_fingerprint control exists to catch.
func runTriggerCase(t *testing.T, tc triggerCase) {
	t.Helper()
	isolateDiscoveryStore(t)
	// ARMED unless the case's own perturbation says otherwise, so a degraded lane
	// is always attributable to the perturbation.
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	tc.perturb(t, result, rec)

	// THE TWIN IS INDEPENDENTLY CONSTRUCTED AND NOT COPIED FROM THE PERTURBED
	// RESULT. It stands for the PREVIOUS collect of an unchanged configuration, so
	// a perturbation of the result's discovery fingerprint is exactly what the
	// discovery-change trigger must see; copying the perturbed value across would
	// seed the store with the new configuration and make that row unfireable.
	twin := twoFileResult()
	seedCollectBaselines(twin)

	require.Equal(t, tc.wantMode, resolveModeFor(t, client, result),
		"the collect must resolve to the lane this trigger targets")

	// RE-SEED BEFORE THE SECOND OBSERVATION, and it is now BELT-AND-BRACES rather
	// than load-bearing. It was load-bearing when the compare also RECORDED: the
	// resolve above moved the store to this collect's configuration, and the
	// discovery-change row would fire once and then be unfireable for the
	// WriteResult below. The compare no longer writes — changed() reads only, and
	// record() runs after a DONE finalize tail, which this fake never reports
	// because it returns no finalize id. Re-seeding is kept because it states the
	// premise both observations share ("the previous collect was the twin") in the
	// test rather than in a fact about the fake.
	seedCollectBaselines(twin)

	h := installCapturingSlog(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	got, any := h.recordedFallback()
	if tc.wantReason == "" {
		require.False(t, any, "a negative control must record NO fallback, got %q", got)
	} else {
		require.True(t, any, "the trigger must be RECORDED, not merely acted on")
		require.Equal(t, string(tc.wantReason), got)
	}
	if tc.wantFullUpload {
		require.Equal(t, 2, uploadedNodeCount(rec),
			"a degraded lane uploads the WHOLE node set, never the diff")
	}
	require.Empty(t, rec.finalizeRequest(t).GetDeletedFiles(),
		"a degraded lane names no deletions — this is what separates 'degraded to full' from 'sent the diff with deletions off'")
}

// TestFallbackTriggers_EachForcesFullUpload drives all FOUR triggers plus the
// two negative controls through a real collect, and adds the trigger-record
// subtest that catches a kill-switch predicate left keyed on the resolved mode.
func TestFallbackTriggers_EachForcesFullUpload(t *testing.T) {
	for _, tc := range triggerCases() {
		t.Run(tc.name, func(t *testing.T) { runTriggerCase(t, tc) })
	}

	// THE ONE ASSERTION A HALF-DONE IMPLEMENTATION CANNOT SATISFY. With the
	// predicate still keyed on s.mode, no trigger fires for the kill switch: the
	// collect still resolves to shadow and still uploads everything, so every mode
	// and payload assertion above passes while logManifestFallback is never
	// reached. Only the RECORD tells the two apart.
	t.Run("kill_switch_records_its_trigger_name", func(t *testing.T) {
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "off")
		client, rec := startRecordingIngest(t)
		result := twoFileResult()
		rec.manifest = manifestMatching(result)
		seedCollectBaselines(result)

		h := installCapturingSlog(t)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

		got, any := h.recordedFallback()
		require.True(t, any, "the kill switch must be RECORDED through logManifestFallback")
		require.Equal(t, string(fallbackKillSwitch), got,
			"and recorded under its own name, so armed-mode classification can attribute it to a human")
	})
}

// divergingManifest renders a manifest that disagrees with the result on exactly
// one file's hash, so shadowDivergences returns a NON-EMPTY hash_mismatch class.
//
// A NON-DIVERGING FIXTURE WOULD MAKE THE TEST VACUOUS: logShadowDivergence
// returns early on an empty path list, so no line would be emitted and an
// "at least one line" assertion would be the only thing failing — or worse,
// an "emitted nothing" test would pass while proving nothing.
func divergingManifest(result *collectorwire.CollectResult) *knowledgev1.CollectManifestResponse {
	resp := manifestMatching(result)
	resp.Entries[0].ContributionHash[0] ^= 0xFF
	return resp
}

// TestKillSwitch_DegradesToShadowAndLogsDivergence is the behavioral gate on the
// retarget itself. The kill switch was moved from plain-full to shadow on one
// justification — the operator who pulls the break-glass lever gets the
// diagnostic of what the diff WOULD have done rather than silence — and that
// justification is worth nothing unless a divergence line is actually emitted on
// that path. Both halves are asserted: the resolved mode, and the line.
func TestKillSwitch_DegradesToShadowAndLogsDivergence(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "off")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = divergingManifest(result)
	seedCollectBaselines(result)

	h := installCapturingSlog(t)
	resp, err := fetchManifestWith(context.Background(), client, result)
	require.NoError(t, err)
	present := contribhash.FileContributionHashes(result.Nodes, result.Edges)
	lvMode, lever, lvErr := collectDiffMode()
	require.NoError(t, lvErr, "off is a VALID lever value")
	var outcome collectDiffOutcome
	mode, decision, dErr := NewUploadSink(client).applyCollectDiff(result, lvMode, lever, present, resp, &outcome)
	require.NoError(t, dErr)
	require.False(t, outcome.suppressManifestEcho,
		"the kill switch must NOT suppress the decline — only the collector-version trigger does")

	require.Equal(t, diffModeShadow, mode,
		"the kill switch degrades to SHADOW, not off — off would upload the same bytes and say nothing")
	require.True(t, decision.uploadAll, "shadow still uploads everything")
	require.Empty(t, decision.deletions, "and still sends no deletions")
	require.Positive(t,
		h.countAt(slog.LevelError, "contribution hash DISAGREES with the server"),
		"a diverging fixture under the kill switch must emit the divergence line — that diagnostic IS the reason the kill switch targets shadow")
}
