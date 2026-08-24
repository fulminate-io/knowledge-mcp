// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_echo_test.go — the manifest-identity echo, which is what makes the
// collector-version trigger a REAL re-land rather than a full upload the server
// declines file by file.
//
// WHY BLANKING THE IDENTITY IS THE WHOLE MECHANISM. The server's decline is not
// keyed on diff mode: store.DeclinedFilesForChunk declines any file whose echoed
// manifest identity and per-file hash match what it holds. The collector-version
// trigger fires precisely when the rows DIFFER in ways no per-file hash can see,
// so a trigger that only forced uploadAll would upload everything and have every
// file declined — a mechanism that executes exclusively in the state where it
// accomplishes nothing. Withholding the identity turns the decline off for that
// one collect, and only then does the server's file-scoped reclaim clear the
// superseded rows.

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

	// SCOPE. A discovery-mode change is a different trigger with a different
	// remedy: its files genuinely match what the server holds, so declining them is
	// CORRECT and suppressing the decline would re-land the whole graph for a
	// re-scope. The identity must still be echoed here.
	t.Run("discovery_trigger_still_echoes", func(t *testing.T) {
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
			require.Equal(t, "manifest-matching", id,
				"chunk %d must still echo the identity: the suppression is scoped to the collector-version trigger", i)
		}
		require.Equal(t, "manifest-matching", finalizeID, "and the Finalize must still echo it")
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
