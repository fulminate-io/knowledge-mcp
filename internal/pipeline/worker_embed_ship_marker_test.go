// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// shipMarkerStamps flattens every recorded update_batch item that carries the
// segment-ship failure marker into id → reason. The vector writeback rides the
// same seam, so the marker items have to be selected by key rather than by batch
// position — a positional read would silently follow any reordering of the two
// writes.
func shipMarkerStamps(f *fakeWireClient) map[string]string {
	out := map[string]string{}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, batch := range f.recordedWrites {
		for _, it := range batch {
			if r, ok := it.Metadata[kgtypes.MetaKeySegmentShipFailureReason]; ok {
				out[it.ID] = r
			}
		}
	}
	return out
}

// embedMarkerStamps is the inverse selector: items carrying a NON-EMPTY embed
// failure reason. A ship drop must never write one — both rebuild scans exclude
// embed-marked nodes, so stamping it would hide the dropped node from the repair
// that exists to re-ship it. The empty value is excluded deliberately and is not
// a loosened assertion: writeEmbedResults writes the key with "" on every
// successful vector writeback to CLEAR a prior marker, so a presence-only test
// would fire on the clear.
func embedMarkerStamps(f *fakeWireClient) map[string]string {
	out := map[string]string{}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, batch := range f.recordedWrites {
		for _, it := range batch {
			if r, ok := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; ok && r != "" {
				out[it.ID] = r
			}
		}
	}
	return out
}

// TestShipEmbedStampsShipFailureMarker drives a real embed batch through the
// writeback seam against a segment manager programmed to fail, and asserts the
// dropped ids carry a durable marker naming the format that dropped them. Without
// the marker an embedded-but-unshipped node is indistinguishable from a healthy one
// and the coverage hole is untraceable.
//
// THE BM25 SUBTEST IS GONE, NOT WEAKENED. It asserted that a FIELDS ship failure on
// the embed axis stamped a bm25-named marker — behaviour this ticket removed, since
// the embed axis no longer ships BM25 at all. A test kept alive against a deleted
// path would assert nothing while looking like coverage. The BM25 ship's failure
// disposition now lives with its producer and is a HELD CURSOR rather than a marker:
// see TestBM25Arm_CursorHeldOnShipFailure, which is a strictly stronger property —
// it re-drives the work instead of recording that work was lost.
//
// The HNSW arm below is UNCHANGED and still the reason this test exists.
func TestShipEmbedStampsShipFailureMarker(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1), "n2": vec32(2)}}
	p := New(Config{}, be, nil, fe.call)
	p.AttachSegmentManager(&fakeShipManager{err: errors.New("hnsw seal boom")})

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
	})

	stamps := shipMarkerStamps(be)
	require.Len(t, stamps, 2, "both dropped ids are stamped")
	for _, id := range []string{"n1", "n2"} {
		require.Contains(t, stamps, id)
		require.Contains(t, strings.ToLower(stamps[id]), "hnsw",
			"the reason names the format that dropped the ship")
		require.Contains(t, stamps[id], "hnsw seal boom",
			"the reason carries the underlying error")
	}
	require.Empty(t, embedMarkerStamps(be),
		"a ship drop must not write the embed-failure key the rebuild scans exclude")
}

// TestShipEmbedStampsNothingOnSuccess is the known-positive control for the two
// subtests above: their pass condition is the PRESENCE of stamps, but the
// emptiness assertions on the embed key, and the whole claim that the stamp is an
// error-path cost only, need a run where the same selector reads zero. A healthy
// ship writes the vectors and nothing else.
func TestShipEmbedStampsNothingOnSuccess(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1)}}
	p := New(Config{}, be, nil, fe.call)
	p.AttachSegmentManager(&fakeShipManager{})

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
	})

	require.Empty(t, shipMarkerStamps(be), "a successful ship stamps nothing")
	require.Equal(t, 1, be.mutateCallCount(), "only the vector writeback rides the wire")
}
