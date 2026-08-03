// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// fakeShipManager captures the docs AddAndMarkDirty was asked to build+seal, and
// (to prove the production round-trip) actually builds an HNSW segment from them,
// encodes it, decodes it, and records the decoded segment's ids. It can be set to
// return an error to exercise the best-effort failure-isolation path. The
// AddAndMarkDirtyFields counterpart does the same for the BM25 format + captures
// the field-bearing Documents.
type fakeShipManager struct {
	err       error
	calls     int
	gotIDs    []string
	decodedID []string
	// shipKeys records the (gt, name) every AddAndMarkDirty (HNSW) was keyed on, so the
	// capstone can assert a custom graph's segments were shipped under its own key.
	shipKeys []graphKey

	fieldsErr     error
	fieldsCalls   int
	fieldDocs     []searchengine.Document
	bm25DecodedID []string
	// fieldsShipKeys records the (gt, name) every AddAndMarkDirtyFields (BM25) was keyed
	// on — the BM25 counterpart of shipKeys.
	fieldsShipKeys []graphKey

	flushErr   error
	flushCalls int
	flushKeys  []graphKey
}

func (f *fakeShipManager) AddAndMarkDirty(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	f.calls++
	f.shipKeys = append(f.shipKeys, graphKey{GraphType: gt, GraphName: name})
	for _, d := range docs {
		f.gotIDs = append(f.gotIDs, d.ID)
	}
	// Mirror the production build→encode→decode→ship round-trip so the test
	// asserts on a DECODED segment (the shippable form), not just the input docs.
	seg, err := hnsw.New().Build(docs)
	if err != nil {
		return err
	}
	blob, err := seg.Encode()
	if err != nil {
		return err
	}
	decoded, err := hnsw.New().Decode(blob)
	if err != nil {
		return err
	}
	f.decodedID = decoded.IDs()
	return f.err
}

// AddAndMarkDirtyFields captures the field-bearing Documents and builds a real
// BM25 segment from them (build→encode→decode) so the test asserts on the DECODED
// (shippable) form. Returns fieldsErr to exercise the best-effort path.
func (f *fakeShipManager) AddAndMarkDirtyFields(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	f.fieldsCalls++
	f.fieldsShipKeys = append(f.fieldsShipKeys, graphKey{GraphType: gt, GraphName: name})
	f.fieldDocs = append(f.fieldDocs, docs...)
	seg, err := bm25.New().Build(docs)
	if err != nil {
		return err
	}
	blob, err := seg.Encode()
	if err != nil {
		return err
	}
	decoded, err := bm25.New().Decode(blob)
	if err != nil {
		return err
	}
	f.bm25DecodedID = decoded.IDs()
	return f.fieldsErr
}

// Flush captures the per-graph quiescence flush. Mirrors the AddAndMarkDirty
// capture shape: records the call count and the (gt, name) keys flushed, and
// returns flushErr to exercise the failure path.
func (f *fakeShipManager) Flush(_ context.Context, gt kgtypes.GraphType, name string) error {
	f.flushCalls++
	f.flushKeys = append(f.flushKeys, graphKey{GraphType: gt, GraphName: name})
	return f.flushErr
}

// vec32 returns a non-empty 32-byte vector seeded by b.
func vec32(b byte) []byte {
	v := make([]byte, 32)
	for i := range v {
		v[i] = b + byte(i)
	}
	return v
}

// TestEmbedWriteback_BuildsAndShipsHNSW drives an embed batch through
// runEmbedWorkerBatch with a fake embedder returning known vectors and a fake
// ship manager; after writeback the manager observed a build+ship whose DECODED
// segment indexes exactly the embedded ids. Criterion: Phase 3 Step 2.
func TestEmbedWriteback_BuildsAndShipsHNSW(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n1": vec32(1),
		"n2": vec32(2),
		"n3": vec32(3),
	}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{}
	p.AttachSegmentManager(fsm)

	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n3", EmbedText: "c", Backend: be},
	}
	runEmbedWorkerBatch(ctx, p, batch)

	require.Equal(t, 1, fsm.calls, "AddAndMarkDirty fires once for the embed group")
	require.ElementsMatch(t, []string{"n1", "n2", "n3"}, fsm.gotIDs, "manager receives the embedded ids")

	got := append([]string(nil), fsm.decodedID...)
	sort.Strings(got)
	require.Equal(t, []string{"n1", "n2", "n3"}, got, "decoded segment indexes exactly the embedded ids")

	// The server-side writeback still happened.
	require.Equal(t, 1, be.mutateCallCount(), "server vector writeback still fires")
	require.Equal(t, int64(3), p.Metrics().EmbedSucceeded, "all three embeds counted OK")
}

// TestEmbedWriteback_DrainFailureIsBestEffort asserts that when the segment
// manager's write returns an error, the embed writeback still completes (server
// writeback succeeded, embedOK incremented) and only a WARN is logged — proving
// the client HNSW build is additive/best-effort.
//
// WHAT THE KNOB NOW MODELS. This seam no longer ships: the write seals and marks
// the partitions dirty, and the owner re-emits and publishes on its own tick,
// which the pipeline never calls. A ship or publish failure is therefore not
// reachable from here, and the only error the write can return is an add or seal
// failure. The knob is KEPT rather than dropped precisely so the best-effort claim
// still has something red behind it — deleting it would leave this test green with
// nothing left to fail.
func TestEmbedWriteback_DrainFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n1": vec32(1),
		"n2": vec32(2),
	}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{err: errors.New("seal boom")}
	p.AttachSegmentManager(fsm)

	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
	}
	// Must not panic; writeback must complete despite the ship error.
	runEmbedWorkerBatch(ctx, p, batch)

	// TWO mutate calls now: the vector writeback, plus the durable ship-failure
	// marker the dropped ids are stamped with, so the drop is attributable rather
	// than surviving only as a log line.
	require.Equal(t, 2, be.mutateCallCount(),
		"server vector writeback still fires despite ship failure, and the dropped ship stamps its ids")
	require.Equal(t, int64(2), p.Metrics().EmbedSucceeded, "embedOK increments despite ship failure")
	require.Equal(t, int64(0), p.Metrics().EmbedFailed, "a ship failure does NOT mark embeds failed")
	require.Equal(t, 1, fsm.calls, "AddAndMarkDirty was attempted")
}

// TestEmbedWriteback_BuildsAndShipsBM25 is the client criterion: at
// the embed writeback seam the pipeline calls AddAndMarkDirtyFields with
// field-bearing Documents built from each item's server-composed Bm25Fields,
// alongside the HNSW write. The decoded BM25 segment indexes exactly the ids that
// carried fields.
func TestEmbedWriteback_BuildsAndShipsBM25(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n1": vec32(1),
		"n2": vec32(2),
		"n3": vec32(3),
	}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{}
	p.AttachSegmentManager(fsm)

	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be,
			Bm25Fields: map[string]string{"symbol_name": "alpha", "summary": "first node"}},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be,
			Bm25Fields: map[string]string{"symbol_name": "beta"}},
		// n3 carries NO Bm25Fields (e.g. a node with no indexable text) — it must be
		// excluded from the BM25 ship but still embedded/HNSW-shipped.
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n3", EmbedText: "c", Backend: be},
	}
	runEmbedWorkerBatch(ctx, p, batch)

	// HNSW ships all three (vectors); BM25 ships only the two that carry fields.
	require.Equal(t, 1, fsm.calls, "HNSW AddAndMarkDirty fires once")
	require.Equal(t, 1, fsm.fieldsCalls, "BM25 AddAndMarkDirtyFields fires once")

	gotBM25 := make([]string, 0, len(fsm.fieldDocs))
	for _, d := range fsm.fieldDocs {
		gotBM25 = append(gotBM25, d.ID)
		require.NotEmpty(t, d.Fields, "BM25 Document must carry Fields, not a Vector")
	}
	require.ElementsMatch(t, []string{"n1", "n2"}, gotBM25, "only field-bearing ids are BM25-indexed")

	got := append([]string(nil), fsm.bm25DecodedID...)
	sort.Strings(got)
	require.Equal(t, []string{"n1", "n2"}, got, "decoded BM25 segment indexes exactly the field-bearing ids")

	require.Equal(t, int64(3), p.Metrics().EmbedSucceeded, "all three embeds counted OK")
}

// TestEmbedWriteback_BM25DrainFailureIsBestEffort asserts an AddAndMarkDirtyFields
// error does NOT propagate: embed writeback completes, embedOK increments, embeds
// are not marked failed (best-effort/additive contract — server BM25 authoritative).
//
// As above, the knob now models a SEAL failure rather than a ship failure: this
// seam marks dirty and never publishes, so no ship error can arrive here.
func TestEmbedWriteback_BM25DrainFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1)}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{fieldsErr: errors.New("bm25 seal boom")}
	p.AttachSegmentManager(fsm)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be,
			Bm25Fields: map[string]string{"summary": "only summary"}},
	})

	// The vector writeback plus the BM25 ship-failure marker stamp.
	require.Equal(t, 2, be.mutateCallCount(),
		"server writeback still fires despite BM25 ship failure, and the dropped ship stamps its ids")
	require.Equal(t, int64(1), p.Metrics().EmbedSucceeded, "embedOK increments despite BM25 ship failure")
	require.Equal(t, int64(0), p.Metrics().EmbedFailed, "a BM25 ship failure does NOT mark embeds failed")
	require.Equal(t, 1, fsm.fieldsCalls, "AddAndMarkDirtyFields was attempted")
}

// TestEmbedWriteback_NilManagerNoOp asserts the seam is a no-op when no segment
// manager is wired (the common test/fake path).
func TestEmbedWriteback_NilManagerNoOp(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1)}}
	p := New(Config{}, be, nil, fe.call)
	// No AttachSegmentManager.
	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
	})
	require.Equal(t, int64(1), p.Metrics().EmbedSucceeded)
}
