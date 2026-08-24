// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_record_test.go — the COMMIT POINT of the collect's baselines.
//
// WHAT THESE TESTS EXIST TO CATCH, and it is one defect with two different early
// returns. The compare used to record too, during upload PLANNING — so a collect
// whose upload then failed had already advanced the baseline, the next collect
// saw no change, took the diff lane, and whatever condition fired the trigger
// never propagated. The trigger fired exactly once and accomplished nothing.
//
// EACH TEST DRIVES THE SAME MEASUREMENT IN BOTH DIRECTIONS. An "unchanged store"
// assertion is worthless alone: this package's fake returns no finalize id by
// default, so NOTHING advances the baseline in most tests and a broken record()
// that never wrote would satisfy the failure arm perfectly. Every test below
// therefore ends with a SUCCESS arm proving the identical fixture DOES advance
// the same bytes.

// errUploadFailedForTest is the CollectChunk failure the upload-failure arm drives.
var errUploadFailedForTest = errors.New("collect chunk rejected by the test fake")

// staleBaselineVersion is the collector version the store is seeded with — one
// BELOW what the fixture stamps.
//
// IT MUST DIFFER FROM THE FIXTURE'S VERSION, and that is the difference between a
// real assertion and a vacuous one. Seeded with the SAME value, a collect that
// wrongly committed would write back byte-identical contents and the
// before/after comparison would pass while the defect was present.
const staleBaselineVersion = testCollectorOutputVersion - 1

// seedStaleCollectorBaseline records a discovery signature that MATCHES the
// fixture and a collector version that does NOT, so exactly one trigger is armed
// and any wrongful commit is visible as a changed byte.
func seedStaleCollectorBaseline(t *testing.T, result *collectorwire.CollectResult) {
	t.Helper()
	require.NoError(t, defaultDiscoveryStore.record(
		baselineCommit{key: discoveryKey(result), sig: discoverySignature(result)},
		baselineCommit{key: collectorVersionKey(result), sig: strconv.Itoa(staleBaselineVersion)}))
}

// readBaselineStore returns the store's raw on-disk bytes. The assertion is made
// on CONTENTS rather than on a re-read through changed(), so a commit that wrote
// the right answer through a broken read path cannot hide.
func readBaselineStore(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(defaultDiscoveryStore.path)
	require.NoError(t, err, "the fixture must have seeded a readable store")
	return raw
}

// wantCommittedVersion asserts the store now carries the fixture's version — the
// positive direction, proving the commit path works at all.
func wantCommittedVersion(t *testing.T, result *collectorwire.CollectResult) {
	t.Helper()
	changed, err := defaultDiscoveryStore.changed(
		collectorVersionKey(result), strconv.Itoa(testCollectorOutputVersion))
	require.NoError(t, err)
	require.False(t, changed, "a DONE tail must advance the collector-version baseline")
}

// TestDiscoveryRecord_NotAdvancedOnUploadFailure is the ORDERING catcher: the
// commit must sit after the upload, so a collect that never uploaded leaves the
// baseline exactly as it found it.
func TestDiscoveryRecord_NotAdvancedOnUploadFailure(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	seedStaleCollectorBaseline(t, result)

	before := readBaselineStore(t)
	rec.chunkErr = errUploadFailedForTest

	err := NewUploadSink(client).WriteResult(context.Background(), "", result)
	require.Error(t, err, "a failed upload must fail the collect")

	require.Equal(t, string(before), string(readBaselineStore(t)),
		"a collect whose upload failed must leave the recorded signature UNCHANGED")

	// KNOWN POSITIVE, same fixture and same store: with the upload succeeding and
	// the tail reporting DONE, these exact bytes DO move. Without this arm the
	// assertion above is satisfied by a record() that never writes at all.
	client2, rec2 := startRecordingIngest(t)
	rec2.manifest = manifestMatching(result)
	rec2.finalizeID = "fin-upload-control"
	rec2.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_DONE
	require.NoError(t, NewUploadSink(client2).WriteResult(context.Background(), "", twoFileResult()))
	require.NotEqual(t, string(before), string(readBaselineStore(t)),
		"control: a successful collect MUST advance the baseline, or the assertion above proves nothing")
	wantCommittedVersion(t, result)
}

// TestDiscoveryRecord_NotAdvancedOnFailedTail is the COMMIT-CONDITION catcher,
// and it is a DIFFERENT early return from the one above: here the upload
// SUCCEEDS and Finalize is accepted, so the collect reaches the tail — which the
// upload-failure test never does.
//
// IT IS THE TEST THAT FAILS AGAINST A COMMIT KEYED ON A NIL ERROR.
// awaitFinalizeTail returns nil on every branch by design (a failed tail must not
// fail the collect, because the durable half already committed), so committing on
// "the tail returned no error" advances the baseline after a FAILED tail and the
// trigger can never re-fire.
func TestDiscoveryRecord_NotAdvancedOnFailedTail(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	rec.finalizeID = "fin-failed-tail"
	rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_FAILED
	seedStaleCollectorBaseline(t, result)

	before := readBaselineStore(t)

	// THE COLLECT ITSELF SUCCEEDS. That is the point: a failed tail is not a
	// collect failure, which is exactly why the tail's error cannot be the signal.
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result),
		"a FAILED tail must not fail the collect")
	require.NotEmpty(t, rec.chunks, "control: the upload must have genuinely happened")
	require.NotNil(t, rec.finalizeRequest(t), "control: Finalize must have been accepted")

	require.Equal(t, string(before), string(readBaselineStore(t)),
		"a non-DONE terminal state must leave the baseline UNADVANCED so the next collect re-fires the trigger")

	// KNOWN POSITIVE: the only thing changed is the tail's terminal state, and the
	// same bytes now move. This is what separates "gated on DONE" from "never
	// commits".
	client2, rec2 := startRecordingIngest(t)
	rec2.manifest = manifestMatching(result)
	rec2.finalizeID = "fin-done-control"
	rec2.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_DONE
	require.NoError(t, NewUploadSink(client2).WriteResult(context.Background(), "", twoFileResult()))
	require.NotEqual(t, string(before), string(readBaselineStore(t)),
		"control: DONE MUST advance the baseline, or the assertion above proves nothing")
	wantCommittedVersion(t, result)
}

// TestDiscoveryRecord_UnconfirmedTailWarnsAboutWithheldBaselines pins the SIGNAL,
// not just the behaviour.
//
// WHY A LOG LINE IS WORTH A TEST HERE. An UNKNOWN tail is ordinary multi-replica
// routing — a FinalizeStatus poll that reached a replica which never served the
// Finalize — and before this changeset it cost nothing durable. It now withholds
// the baseline advance, so the trigger re-fires and the NEXT collect of the graph
// pays another decline-suppressed full re-land of everything. A repeating full
// re-land whose only trace was Debug would be an operator mystery. The signal has
// to match the cost the changeset gave the state.
func TestDiscoveryRecord_UnconfirmedTailWarnsAboutWithheldBaselines(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	rec.finalizeID = "fin-unknown-warn"
	rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN
	seedStaleCollectorBaseline(t, result)

	h := installCapturingSlog(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	require.Positive(t, h.countAt(slog.LevelWarn, "collect baselines WITHHELD"),
		"an unconfirmed tail that withheld a baseline must say so at WARN — the next collect pays a full re-land")

	// THE LINE MUST NAME WHAT WAS WITHHELD, or an operator cannot tell which graph
	// is about to re-land.
	require.Positive(t, h.attrCountAt(slog.LevelWarn, "withheld", collectorVersionKey(result)),
		"the warning must name the withheld collector-version baseline")

	// KNOWN-NEGATIVE CONTROL: the same unconfirmed tail with NOTHING pending must
	// stay quiet, or the warning becomes noise on every non-code collect and gets
	// trained away exactly where it matters.
	require.Zero(t, quietCommitWarnCount(t),
		"a collect that owes no baseline must not warn about withholding one")
}

// quietCommitWarnCount drives commitCollectBaselines directly with an empty
// pending set under a non-DONE state and reports how many warnings it emitted.
func quietCommitWarnCount(t *testing.T) int {
	t.Helper()
	h := installCapturingSlog(t)
	require.NoError(t, commitCollectBaselines(knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN, nil))
	return h.countAt(slog.LevelWarn, "collect baselines WITHHELD")
}

// TestDiscoveryRecord_NotAdvancedOnUnknownTail covers the remaining non-DONE
// terminal states through their representative: a poll routed to a replica that
// never served the Finalize. It is a legitimate, common outcome rather than a
// failure, and it is still not a completion signal.
func TestDiscoveryRecord_NotAdvancedOnUnknownTail(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)
	rec.finalizeID = "fin-unknown-tail"
	rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN
	seedStaleCollectorBaseline(t, result)

	before := readBaselineStore(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
	require.Equal(t, string(before), string(readBaselineStore(t)),
		"an UNKNOWN tail is an unobserved outcome, and a baseline must not advance on one")

	client2, rec2 := startRecordingIngest(t)
	rec2.manifest = manifestMatching(result)
	rec2.finalizeID = "fin-done-control"
	rec2.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_DONE
	require.NoError(t, NewUploadSink(client2).WriteResult(context.Background(), "", twoFileResult()))
	require.NotEqual(t, string(before), string(readBaselineStore(t)),
		"control: DONE MUST advance the baseline")
}
