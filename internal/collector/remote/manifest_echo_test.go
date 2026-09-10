// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_echo_test.go — the manifest-identity echo, which is what makes a
// FULL-UPLOAD collect a REAL re-land rather than a full upload the server
// declines key by key.
//
// WHY BLANKING THE IDENTITY IS THE WHOLE MECHANISM. The server's decline is not
// keyed on diff mode: store.DeclinedFilesForChunk declines any key whose echoed
// manifest identity and per-key hash match what it holds. A collect that forces
// uploadAll while still echoing the identity therefore uploads everything and
// has every unchanged key declined — a mechanism that executes exclusively in
// the state where it accomplishes nothing. Withholding the identity turns the
// decline off for that one collect, and only then does the server's reclaim
// clear the superseded rows.
//
// AND THE ECHO ON A FULL UPLOAD IS NOT MERELY INERT — IT DESTROYS. A full upload
// carries no deletion set, so the server derives one from its presence rails,
// and a declined key is accounted for by the declined rail alone. That shipped
// as a P0: a registered custom collect whose params moved fired the
// discovery-mode trigger, echoed the identity, had every stable-id row declined
// and lost them, while reporting a complete successful emission. So the
// suppression is keyed on decision.uploadAll and the legs below cover EVERY
// route to a full upload, not just the one trigger that had it first.

// echoedManifestIDs returns the identity every captured chunk carried plus the
// one the Finalize carried, so an assertion covers BOTH consumers. Blanking one
// and not the other would leave the collect half-declined.
func echoedManifestIDs(t *testing.T, rec *recordingIngest) (chunkIDs []string, finalizeID string) {
	t.Helper()
	rec.mu.Lock()
	for _, c := range rec.chunks {
		chunkIDs = append(chunkIDs, c.GetManifestId())
	}
	rec.mu.Unlock()
	require.NotEmpty(t, chunkIDs, "control: the collect must have sent at least one chunk")
	return chunkIDs, rec.finalizeRequest(t).GetManifestId()
}

// runEchoCollect seeds the store as the caller describes, drives one whole
// collect, and returns what went on the wire.
func runEchoCollect(
	t *testing.T, seed func(*collectorwire.CollectResult),
) (*recordingIngest, *capturingSlog) {
	t.Helper()
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	seed(result)

	h := installCapturingSlog(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
	return rec, h
}

// TestCollectorVersionChange_SuppressesManifestEcho pins that the suppression
// exists, that it is SCOPED to the collector-version trigger, and that it does
// not fire when nothing changed.
func TestCollectorVersionChange_SuppressesManifestEcho(t *testing.T) {
	// The trigger's own case: the store holds an older collector version, so this
	// collect must withhold the identity from both carriers and let every file
	// land.
	t.Run("changed_version_blanks_echo", func(t *testing.T) {
		rec, h := runEchoCollect(t, func(result *collectorwire.CollectResult) {
			require.NoError(t, defaultDiscoveryStore.record(
				baselineCommit{key: discoveryKey(result), sig: discoverySignature(result)},
				baselineCommit{key: collectorVersionKey(result), sig: strconv.Itoa(staleBaselineVersion)}))
		})

		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id,
				"chunk %d must carry an EMPTY manifest identity — the server's first decline conjunct "+
					"then fails and every row lands", i)
		}
		require.Empty(t, finalizeID, "the Finalize must withhold the identity too, or the collect is half-declined")

		got, any := h.recordedFallback()
		require.True(t, any, "the trigger must be RECORDED, not merely acted on")
		require.Equal(t, string(fallbackCollectorVersionChange), got)
	})

	// THE LEG THAT CATCHES A BLANKET BLANKING. Without it, code that always blanks
	// satisfies the first subtest while silently disabling the server's decline on
	// every collect forever — the incremental path's entire value, gone, with every
	// other gate green.
	t.Run("unchanged_version_echoes", func(t *testing.T) {
		rec, h := runEchoCollect(t, seedCollectBaselines)

		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Equal(t, "manifest-matching", id,
				"chunk %d must echo the served identity when nothing changed", i)
		}
		require.Equal(t, "manifest-matching", finalizeID, "and so must the Finalize")

		got, any := h.recordedFallback()
		require.False(t, any, "an unchanged collect must record NO fallback, got %q", got)
	})

	// THE P0's OWN LEG. A discovery-mode change forces a full upload exactly as
	// the collector-version trigger does, and while it echoed the identity that
	// upload was declined key by key and the server's derived deletion set
	// removed every declined row. The rows matching what the server holds is not
	// a reason to echo — it is what makes the decline fire.
	t.Run("discovery_trigger_suppresses_echo", func(t *testing.T) {
		rec, h := runEchoCollect(t, func(result *collectorwire.CollectResult) {
			require.NoError(t, defaultDiscoveryStore.record(
				baselineCommit{key: discoveryKey(result), sig: "a-different-discovery-configuration"},
				baselineCommit{
					key: collectorVersionKey(result),
					sig: strconv.FormatUint(uint64(result.CollectorOutputVersion), 10),
				}))
		})

		got, any := h.recordedFallback()
		require.True(t, any, "control: the discovery trigger must actually have fired")
		require.Equal(t, string(fallbackDiscoveryModeChange), got,
			"control: this subtest is only meaningful if the DISCOVERY trigger is the one that fired")

		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id,
				"chunk %d must withhold the identity: this collect uploads in full and names no deletions, "+
					"so an echoed identity has the server decline every unchanged key and then derive it as deleted", i)
		}
		require.Empty(t, finalizeID, "the Finalize must withhold it too, or the collect is half-declined")
	})

	// THE KILL SWITCH IS A FULL UPLOAD TOO. An operator reaching for the
	// break-glass lever gets a real re-land rather than an upload the server
	// declines wholesale — and, on a node-keyed graph, rather than the same
	// deletion the discovery trigger produced.
	t.Run("kill_switch_suppresses_echo", func(t *testing.T) {
		isolateDiscoveryStore(t)
		client, rec := startRecordingIngest(t)
		result := twoFileResult()
		rec.manifest = manifestMatching(result)
		seedCollectBaselines(result)
		t.Setenv(collectDiffEnv, "off")

		h := installCapturingSlog(t)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

		got, any := h.recordedFallback()
		require.True(t, any, "control: the kill switch must actually have fired")
		require.Equal(t, string(fallbackKillSwitch), got, "control: the KILL SWITCH must be the trigger under test")

		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id, "chunk %d of a kill-switched collect must withhold the identity", i)
		}
		require.Empty(t, finalizeID)
	})

	// THE LEG WITH NO TRIGGER AT ALL, which is why the suppression is keyed on
	// the upload plan and not on the trigger table. A deliberate shadow request
	// fires nothing — evaluateManifestFallback returns false for it — and still
	// uploads in full, so a reason-keyed suppression would leave exactly this
	// lane echoing an identity for a full upload.
	t.Run("deliberate_shadow_suppresses_echo", func(t *testing.T) {
		isolateDiscoveryStore(t)
		client, rec := startRecordingIngest(t)
		result := twoFileResult()
		rec.manifest = manifestMatching(result)
		seedCollectBaselines(result)
		t.Setenv(collectDiffEnv, "shadow")

		h := installCapturingSlog(t)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

		got, any := h.recordedFallback()
		require.False(t, any, "control: a deliberate shadow request must fire NO trigger, got %q", got)

		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id,
				"chunk %d: shadow uploads in full, so it must withhold the identity even though no trigger fired", i)
		}
		require.Empty(t, finalizeID)
	})
}

// TestCollectorVersionChange_UnstampedVersionAborts is the producer-regression
// refusal. A zero version is OUR OWN collector failing to stamp itself, which no
// full collect repairs — so it is a loud error rather than a value read as
// "unchanged", which would silently disable this mechanism for that collector.
func TestCollectorVersionChange_UnstampedVersionAborts(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	result.CollectorOutputVersion = 0
	rec.manifest = manifestMatching(result)

	err := NewUploadSink(client).WriteResult(context.Background(), "", result)
	require.Error(t, err, "an unstamped producer must abort the collect")
	require.Contains(t, err.Error(), "CollectorOutputVersion",
		"and the error must name the field that was not stamped")
	require.Zero(t, chunkCount(rec), "the abort must precede the first chunk")
}

// TestCollectorVersionChange_FirstCollectReLands pins the bootstrap case, which
// is DELIBERATELY the repair: a graph with no recorded collector version — every
// graph, on the first collect after this ships — reads as changed and takes one
// decline-suppressed full re-land, exactly as the discovery store's absent-record
// state keeps the rebuild lane.
func TestCollectorVersionChange_FirstCollectReLands(t *testing.T) {
	rec, h := runEchoCollect(t, func(*collectorwire.CollectResult) {})

	chunkIDs, finalizeID := echoedManifestIDs(t, rec)
	for i, id := range chunkIDs {
		require.Empty(t, id, "chunk %d of a first collect must withhold the identity", i)
	}
	require.Empty(t, finalizeID)

	got, any := h.recordedFallback()
	require.True(t, any)
	require.Equal(t, string(fallbackCollectorVersionChange), got,
		"an absent record is a collector-version change, not a discovery change")
}
