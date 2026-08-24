// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sink_edge_merge_test.go — the end-to-end reproduction of the file that never
// converges, driven through the REAL WriteResult against the recording ingest
// fake so every assertion reads the bytes that went on the wire.
//
// THE DEFECT IT REPRODUCES: the emitters produce SEVERAL rows per stored edge
// identity, the client hashes and ships that multiset, and the server can only
// hold the set. The two hashes can never agree, so the file uploads on every
// collect forever.
//
// THE FIXTURE CARRIES BOTH DUPLICATE CLASSES ON PURPOSE, because they have
// DIFFERENT correct answers: an IMPLEMENTS identity's copies differ only in
// Method and the later one wins, while a CALLS identity's copies each hold a
// share of one call count and must SUM. A fixture carrying only one class is
// satisfied by an implementation that gets the other wrong. Interleaving them
// also exercises the merge's position bookkeeping rather than two adjacent pairs.

// dupSurvivingMethod is the LAST IMPLEMENTS copy's Method, and therefore the one
// the store's last-op-wins conflict resolution keeps.
const dupSurvivingMethod = "method-set:2"

// dupSummedWeight is 2 + 3: the call count the source makes, which is what the
// merged CALLS row must publish. Keeping a single copy would publish 3.
const dupSummedWeight = 5.0

// dupEdgeResult returns a two-file code CollectResult whose pkg/a.go emits TWO
// edge identities TWICE each in interleaved order, and — as its second value —
// the row set the SERVER therefore holds for those emissions.
//
// IT STATES BOTH ROW SETS BY CONSTRUCTION AND CALLS NO MERGE FUNCTION. That is
// what lets this test compile and run RED against an unfixed tree, need no
// rewrite once the fix lands, and introduce no second spelling of the identity
// or of the summing rule anywhere.
//
// NO FILELESS NODES, for twoFileResult's reason: the fileless set always
// uploads, so a fixture carrying one could never produce a genuinely empty diff.
func dupEdgeResult() (*collectorwire.CollectResult, []kgwire.BatchEdge) {
	nodes := []*knowledgev1.Node{
		{Id: "pkg/a.go:Alpha", Type: "function", SymbolName: "Alpha", FilePath: "pkg/a.go", Language: "go"},
		{Id: "pkg/b.go:Beta", Type: "function", SymbolName: "Beta", FilePath: "pkg/b.go", Language: "go"},
	}
	implements := func(method string) kgwire.BatchEdge {
		return kgwire.BatchEdge{
			FromIdx: -1, ToIdx: -1,
			FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta",
			Type: kgtypes.EdgeImplements, Method: method,
		}
	}
	calls := func(weight float64) kgwire.BatchEdge {
		return kgwire.BatchEdge{
			FromIdx: -1, ToIdx: -1,
			FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta",
			Type: kgtypes.EdgeCalls, Method: "typed-qualifier", Weight: weight,
		}
	}
	emitted := []kgwire.BatchEdge{
		implements("method-set:1"),
		calls(2),
		implements(dupSurvivingMethod),
		calls(3),
	}
	stored := []kgwire.BatchEdge{
		implements(dupSurvivingMethod),
		calls(dupSummedWeight),
	}
	return &collectorwire.CollectResult{
		GraphType:              kgtypes.GraphCode,
		GraphName:              "dup-edge-repo",
		CurrentBranch:          "main",
		Nodes:                  nodes,
		Edges:                  emitted,
		WalkComplete:           true,
		DiscoveryFingerprint:   "fingerprint-dup-v1",
		CollectorOutputVersion: testCollectorOutputVersion,
	}, stored
}

// manifestAtStoredRowSet renders the manifest the SERVER would render: the
// per-file hashes taken over the STORED row set rather than over result.Edges.
//
// THIS IS THE ONE PLACE manifestMatching CANNOT BE REUSED, AND THE REASON IS THE
// DEFECT. manifestMatching (sink_diff_upload_test.go) hashes result.Edges — the
// MULTISET — which is precisely the value the server can never hold.
func manifestAtStoredRowSet(
	result *collectorwire.CollectResult, stored []kgwire.BatchEdge,
) *knowledgev1.CollectManifestResponse {
	hashes := contribhash.FileContributionHashes(result.Nodes, stored)
	resp := &knowledgev1.CollectManifestResponse{
		ManifestId:        "manifest-stored-row-set",
		HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	}
	for path, h := range hashes {
		resp.Entries = append(resp.Entries, &knowledgev1.ManifestEntry{
			FilePath: path, ContributionHash: append([]byte(nil), h[:]...),
		})
	}
	return resp
}

// uploadedEdgesOfType filters the captured chunks' edges by type — the wire
// bytes, not an intermediate value.
func uploadedEdgesOfType(
	chunks []*knowledgev1.CollectChunkRequest, edgeType kgtypes.EdgeType,
) []*knowledgev1.BatchEdge {
	var out []*knowledgev1.BatchEdge
	for _, c := range chunks {
		for _, e := range c.GetEdges() {
			if e.GetType() == string(edgeType) {
				out = append(out, e)
			}
		}
	}
	return out
}

// runDupCollect drives one armed collect of the duplicate-carrying fixture and
// returns the captured chunks. An EMPTY omit serves the stored-row-set manifest
// (the convergence case); a non-empty one omits that file's entry, so the file
// legitimately reads changed and its rows go on the wire.
func runDupCollect(t *testing.T, omit string) []*knowledgev1.CollectChunkRequest {
	t.Helper()
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result, stored := dupEdgeResult()
	seedCollectBaselines(result)
	if omit == "" {
		rec.manifest = manifestAtStoredRowSet(result, stored)
	} else {
		rec.manifest = manifestOmitting(result, omit)
	}

	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.chunks
}

// TestDiffUpload_DuplicateEdgeIdentity is both the reproduction and the standing
// regression: it lives in the package suite and runs on every go test of this
// package.
//
// LEGS 2 AND 3 ARE WHAT DISCRIMINATE A FIX THAT MERGES THE HASH BUT NOT THE
// UPLOAD. Leg 1 alone would be satisfied by a client that merged only the value
// it hashed and still shipped the raw multiset.
func TestDiffUpload_DuplicateEdgeIdentity(t *testing.T) {
	t.Run("declines_against_stored_row_set", func(t *testing.T) {
		// THE CONVERGENCE PROPERTY. The manifest carries the hash of the rows the
		// server actually holds — including the SUMMED CALLS weight — so a client
		// whose hash basis is the same row set uploads nothing. Before the merge
		// the client hashes four rows against the server's two and the file
		// re-uploads on every collect forever.
		captured := runDupCollect(t, "")

		require.Empty(t, uploadedNodeIDs(captured),
			"a file whose stored row set already matches must upload nothing")
	})

	t.Run("uploads_one_row_last_copy", func(t *testing.T) {
		// pkg/a.go is omitted from the manifest, so it legitimately reads changed
		// and its rows go on the wire — where there must be ONE IMPLEMENTS row,
		// carrying the LAST copy's Method, matching the winner the store picks.
		captured := runDupCollect(t, "pkg/a.go")

		got := uploadedEdgesOfType(captured, kgtypes.EdgeImplements)
		require.Len(t, got, 1,
			"the two emitted IMPLEMENTS copies are one stored row and must ship as one")
		require.Equal(t, dupSurvivingMethod, got[0].GetMethod(),
			"the surviving row must carry the LAST copy's Method — the store's own last-op-wins winner")
	})

	t.Run("calls_weight_sums_on_the_wire", func(t *testing.T) {
		// THE CATCHER FOR KEEP-LAST APPLIED TO CALLS — the disposition a rule that
		// only said "keep the last copy" would have produced, which publishes 3
		// calls where the source makes 5. Asserted on the BYTES THAT WENT ON THE
		// WIRE, not on an intermediate value.
		captured := runDupCollect(t, "pkg/a.go")

		got := uploadedEdgesOfType(captured, kgtypes.EdgeCalls)
		require.Len(t, got, 1,
			"the two emitted CALLS copies are one stored row and must ship as one")
		require.InDelta(t, dupSummedWeight, got[0].GetWeight(), 0,
			"the surviving CALLS row must carry the SUMMED call count, not one copy's share")
	})
}
