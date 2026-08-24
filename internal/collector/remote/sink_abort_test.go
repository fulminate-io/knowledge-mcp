// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sink_abort_test.go — the four conditions a full collect cannot repair, each
// asserted where it now lives: a loud error from WriteResult, raised before the
// first chunk.
//
// THE ZERO-CHUNK LEG IS THE DISCRIMINATING ONE in every case. An implementation
// that logged loudly and then uploaded anyway satisfies an error-message
// assertion while leaving the lane exactly as it was; only the absence of any
// CollectChunk separates an abort from a noisy degrade.

// chunkCount returns how many CollectChunk requests the stub received.
func chunkCount(rec *recordingIngest) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.chunks)
}

// manifestCallCount returns how many CollectManifest RPCs the stub received.
func manifestCallCount(rec *recordingIngest) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.manifestCalls
}

// abortFixture stands up an armed code collect whose manifest the caller shapes.
func abortFixture(t *testing.T) (*UploadSink, *recordingIngest, *collectorwire.CollectResult) {
	t.Helper()
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")
	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	seedCollectBaselines(result)
	return NewUploadSink(client), rec, result
}

// TestWriteResult_ManifestFetchErrorAbortsCollect: the manifest RPC failing is
// the SAME failure class WriteResult already hard-errors on for the picker —
// fetchManifest resolves through that same picker — so it errors rather than
// degrading.
func TestWriteResult_ManifestFetchErrorAbortsCollect(t *testing.T) {
	sink, rec, result := abortFixture(t)
	rec.manifestErr = errFetchFailedForTest

	err := sink.WriteResult(context.Background(), "", result)
	require.Error(t, err, "a failed manifest fetch must ABORT, not degrade to a full upload")
	require.Contains(t, err.Error(), "collect manifest:", "the error names the operation")
	require.Contains(t, err.Error(), errFetchFailedForTest.Error(), "and carries the cause")
	require.Zero(t, chunkCount(rec), "the abort precedes the FIRST chunk, so nothing was uploaded")
}

// TestWriteResult_InconsistentManifestAbortsCollect: a manifest that disagrees
// with its own contract came from the server's render logic, and the next render
// runs the same logic — the condition re-fires forever instead of converging.
func TestWriteResult_InconsistentManifestAbortsCollect(t *testing.T) {
	sink, rec, result := abortFixture(t)
	bad := manifestMatching(result)
	// A DUPLICATE file_path. Many symbols legitimately share a file, so a repeated
	// path in a PER-FILE manifest can only mean broken aggregation.
	bad.Entries = append(bad.Entries, &knowledgev1.ManifestEntry{
		FilePath:         bad.Entries[0].GetFilePath(),
		ContributionHash: append([]byte(nil), bad.Entries[0].GetContributionHash()...),
	})
	rec.manifest = bad

	err := sink.WriteResult(context.Background(), "", result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "violates its own contract")
	require.Contains(t, err.Error(), bad.Entries[0].GetFilePath(),
		"the error must name the OFFENDING ENTRY — 'inconsistent' alone is not actionable")
	require.Zero(t, chunkCount(rec), "the abort precedes the FIRST chunk")
}

// TestWriteResult_MissingManifestIDAbortsCollect: the identity is minted and
// persisted by the RENDER, never by an upload, so no collect this client runs
// can supply one that is missing.
func TestWriteResult_MissingManifestIDAbortsCollect(t *testing.T) {
	t.Run("empty_identity", func(t *testing.T) {
		sink, rec, result := abortFixture(t)
		bad := manifestMatching(result)
		bad.ManifestId = ""
		rec.manifest = bad

		err := sink.WriteResult(context.Background(), "", result)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing_manifest_id")
		require.Zero(t, chunkCount(rec), "the abort precedes the FIRST chunk")
	})

	// THE NIL BODY IS REACHABLE, not defensive: fetchManifestWith returns
	// resp.Msg, so a response carrying no message yields (nil, nil) and the
	// identity check is the arm that catches it.
	t.Run("nil_response_body", func(t *testing.T) {
		var nilResp *knowledgev1.CollectManifestResponse
		require.Empty(t, nilResp.GetManifestId(),
			"a nil manifest presents the same empty identity the abort keys on")
	})
}

// TestWriteResult_EmptyDiscoveryFingerprintAbortsCollect: an empty fingerprint
// names OUR OWN producer regressing, which no full collect repairs.
func TestWriteResult_EmptyDiscoveryFingerprintAbortsCollect(t *testing.T) {
	sink, rec, result := abortFixture(t)
	result.DiscoveryFingerprint = ""

	err := sink.WriteResult(context.Background(), "", result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty discovery fingerprint")
	require.Zero(t, chunkCount(rec), "the abort precedes the FIRST chunk")
	// THE NO-RPC LEG PROVES THE ORDERING the step requires: the check runs before
	// the fetch, so a regressed producer costs no round trip. Without it, an abort
	// placed after the fetch would satisfy every other assertion here.
	require.Zero(t, manifestCallCount(rec), "and precedes the manifest fetch entirely")
}

// TestFilterToChangedFiles_IndexAddressedEdgeErrors is the other half of the
// no-dropping guard (the producer half is
// parser.TestToBatchEdges_AlwaysIDAddressed). The armed diff places an edge by
// resolving its FROM NODE BY ID, so an index-addressed edge cannot be placed —
// and the answer to "cannot place" is a loud error naming the edge, never a
// silent drop.
func TestFilterToChangedFiles_IndexAddressedEdgeErrors(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "pkg/a.go:Alpha", FilePath: "pkg/a.go"},
		{Id: "pkg/b.go:Beta", FilePath: "pkg/b.go"},
	}

	t.Run("id_addressed_edges_pass_through", func(t *testing.T) {
		// THE KNOWN POSITIVE. Without it, an implementation that errored on every
		// edge — or a filter that dropped everything — would satisfy the refusal
		// case below while breaking every real collect.
		edges := []kgwire.BatchEdge{{
			FromIdx: -1, ToIdx: -1,
			FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CALLS"),
		}}
		keptNodes, keptHashes, keptEdges, err := filterToChangedFiles(
			nodes, nil, edges, []string{"pkg/a.go", "pkg/b.go"}, true)
		require.NoError(t, err)
		require.Len(t, keptNodes, 2)
		require.Len(t, keptEdges, 1, "an ID-addressed edge whose FROM node survives rides with it")
		require.Nil(t, keptHashes, "a caller carrying no per-row digests gets none back")
	})

	t.Run("node_digests_are_narrowed_by_the_same_predicate", func(t *testing.T) {
		// The wire contract for node_contribution_hashes is INDEX ALIGNMENT with the
		// chunked node slice, so the digest array has to survive this filter in
		// lockstep. Distinct digest values are what make a mis-narrowing visible: an
		// implementation that kept the first N would return alpha's digest for beta.
		alpha := [32]byte{0xAA}
		beta := [32]byte{0xBB}
		keptNodes, keptHashes, _, err := filterToChangedFiles(
			nodes, [][32]byte{alpha, beta}, nil, []string{"pkg/b.go"}, true)
		require.NoError(t, err)
		require.Len(t, keptNodes, 1, "only pkg/b.go was named as changed")
		require.Equal(t, [][32]byte{beta}, keptHashes,
			"the surviving node's OWN digest rides with it, not its predecessor's")
	})

	t.Run("misaligned_digest_array_is_refused", func(t *testing.T) {
		// A length that does not match is the two-passes bug, and narrowing to the
		// shorter array would hand the chunker digests belonging to other nodes.
		_, _, _, err := filterToChangedFiles(
			nodes, [][32]byte{{0xAA}}, nil, []string{"pkg/a.go", "pkg/b.go"}, true)
		require.Error(t, err, "a misaligned digest array must ERROR rather than truncate")
		require.Contains(t, err.Error(), "index-aligned", "the error names the contract it broke")
		require.Contains(t, err.Error(), "1 per-row node digests for 2 nodes", "and both lengths")
	})

	t.Run("index_addressed_edge_is_refused_not_dropped", func(t *testing.T) {
		edges := []kgwire.BatchEdge{{
			FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeType("CONTAINS"), ToID: "pkg/b.go:Beta",
		}}
		_, _, _, err := filterToChangedFiles(nodes, nil, edges, []string{"pkg/a.go", "pkg/b.go"}, true)
		require.Error(t, err, "an unplaceable edge must ERROR — dropping information is not an available response")
		require.Contains(t, err.Error(), "INDEX-ADDRESSED", "the error names the condition")
		require.Contains(t, err.Error(), "CONTAINS", "and the offending edge's type")
		require.Contains(t, err.Error(), "pkg/b.go:Beta", "and what it pointed at")
		require.Contains(t, err.Error(), "ID-ADDRESSED", "and the valid shape")
	})
}
