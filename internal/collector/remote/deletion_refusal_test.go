// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// deletion_refusal_test.go — the client half of the refusal report.
//
// WHAT WAS BROKEN. deletion_refused_reason has ridden FinalizeResponse since the
// guard stack landed, and NO client code read it: the collect logged "finalize
// accepted" at Debug, reported success, and the rows the client believed it had
// deleted stayed in the graph. A refused deletion and a graph with nothing to
// delete were indistinguishable from the operator's seat, which is the exact
// indistinguishability the field exists to end.
//
// THE TWO OBSERVABLES ARE ASSERTED SEPARATELY because they serve different
// readers: the Error log line is for the operator tailing the daemon, and the
// recorded refusal is what reaches the calling LLM in the collect's result text.
// A change that wires one and not the other leaves the other reader with plain
// success.

// manifestNamingOneDeletedFile is manifestMatching plus ONE entry the result does
// not carry, so the client's own diff names exactly one deletion. The refusal
// report's counts are then a real measurement rather than zero.
func manifestNamingOneDeletedFile(result *collectorwire.CollectResult, gonePath string) *knowledgev1.CollectManifestResponse {
	resp := manifestMatching(result)
	resp.Entries = append(resp.Entries, newManifestEntry(diffKeyFile, gonePath, make([]byte, 32)))
	return resp
}

// TestFinalizeRefusal_IsLoggedAndRecorded drives a real diff collect whose
// Finalize comes back refused.
func TestFinalizeRefusal_IsLoggedAndRecorded(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	seedCollectBaselines(result)
	rec.manifest = manifestNamingOneDeletedFile(result, "pkg/gone.go")
	rec.deletionRefusedReason = "walk_incomplete"

	h := installCapturingSlog(t)
	ctx, refusal := collector.WithDeletionRefusal(context.Background())
	require.NoError(t, NewUploadSink(client).WriteResult(ctx, "", result),
		"a refused DELETION is not a failed collect — the rows this collect uploaded landed")

	// CONTROL ON THE FIXTURE: the collect really did name a deletion, so the counts
	// the report carries are measured rather than vacuous.
	require.Equal(t, []string{"pkg/gone.go"}, rec.finalizeRequest(t).GetDeletedFiles(),
		"fixture control: this collect must have named a deletion for the server to refuse")

	// (1) THE OPERATOR'S OBSERVABLE: one Error line naming the reason and the counts.
	require.Equal(t, 1, h.countAt(slog.LevelError, "deletion"),
		"a refused deletion must be logged exactly once at Error — Debug-level success is what hid it")
	require.Equal(t, 1, h.attrCountAt(slog.LevelError, "reason", "walk_incomplete"),
		"the line must name the GUARD that refused: a line that says a deletion was refused without saying why is not actionable")
	require.Equal(t, 1, h.attrCountAt(slog.LevelError, "named", "1"),
		"the line must carry how many entries were named")
	require.Equal(t, 1, h.attrCountAt(slog.LevelError, "graph", result.GraphName),
		"the line must name the graph — an operator with several repos cannot act on an unattributed refusal")

	// (2) THE CALLING LLM'S OBSERVABLE: the refusal is recorded for the result text.
	notice := refusal.Notice()
	require.NotEmpty(t, notice, "the refusal must reach the collect's result text, not only the daemon log")
	require.Contains(t, notice, "walk_incomplete", "the notice must name the reason")
}

// TestFinalizeRefusal_CleanFinalizeReportsNothing is the CONTROL, and it is the
// leg that catches a reporter wired to fire unconditionally. Everything about the
// fixture is identical except the server's answer.
func TestFinalizeRefusal_CleanFinalizeReportsNothing(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	seedCollectBaselines(result)
	rec.manifest = manifestNamingOneDeletedFile(result, "pkg/gone.go")

	h := installCapturingSlog(t)
	ctx, refusal := collector.WithDeletionRefusal(context.Background())
	require.NoError(t, NewUploadSink(client).WriteResult(ctx, "", result))

	require.Equal(t, []string{"pkg/gone.go"}, rec.finalizeRequest(t).GetDeletedFiles(),
		"fixture control: the same named deletion as the refused case — only the server's answer differs")
	require.Zero(t, h.countAt(slog.LevelError, "deletion"),
		"an ADMITTED deletion must log no refusal at all")
	require.Empty(t, refusal.Notice(), "and it must leave the result text byte-identical to before")
}

// TestFinalizeRefusal_WithoutACarrierIsStillSafe pins the collect paths that never
// installed a recorder — every direct sink caller, including every test above this
// one and the cascade collects that construct their own context. The report must
// still log, and must not panic on the absent carrier.
func TestFinalizeRefusal_WithoutACarrierIsStillSafe(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	seedCollectBaselines(result)
	rec.manifest = manifestNamingOneDeletedFile(result, "pkg/gone.go")
	rec.deletionRefusedReason = "manifest_identity_mismatch"

	h := installCapturingSlog(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	require.Equal(t, 1, h.attrCountAt(slog.LevelError, "reason", "manifest_identity_mismatch"),
		"the log half of the report does not depend on a recorder being installed")
}
