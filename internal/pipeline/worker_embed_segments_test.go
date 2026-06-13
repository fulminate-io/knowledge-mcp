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

// fakeShipManager captures the docs AddAndShip was asked to build+ship, and (to
// prove the production round-trip) actually builds an HNSW segment from them,
// encodes it, decodes it, and records the decoded segment's ids. It can be set to
// return an error to exercise the best-effort failure-isolation path. The
// AddAndShipFields counterpart does the same for the BM25 format + captures the
// field-bearing Documents.
type fakeShipManager struct {
	err       error
	calls     int
	gotIDs    []string
	decodedID []string
	// shipKeys records the (gt, name) every AddAndShip (HNSW) was keyed on, so the
	// capstone can assert a custom graph's segments were shipped under its own key.
	shipKeys []graphKey

	fieldsErr     error
	fieldsCalls   int
	fieldDocs     []searchengine.Document
	bm25DecodedID []string
	// fieldsShipKeys records the (gt, name) every AddAndShipFields (BM25) was keyed
	// on — the BM25 counterpart of shipKeys.
	fieldsShipKeys []graphKey

	flushErr   error
	flushCalls int
	flushKeys  []graphKey
}

func (f *fakeShipManager) AddAndShip(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
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

// AddAndShipFields captures the field-bearing Documents and builds a real BM25
// segment from them (build→encode→decode) so the test asserts on the DECODED
// (shippable) form. Returns fieldsErr to exercise the best-effort path.
func (f *fakeShipManager) AddAndShipFields(_ context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
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

// Flush captures the per-graph quiescence flush. Mirrors the AddAndShip
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

	require.Equal(t, 1, fsm.calls, "AddAndShip fires once for the embed group")
	require.ElementsMatch(t, []string{"n1", "n2", "n3"}, fsm.gotIDs, "manager receives the embedded ids")

	got := append([]string(nil), fsm.decodedID...)
	sort.Strings(got)
	require.Equal(t, []string{"n1", "n2", "n3"}, got, "decoded segment indexes exactly the embedded ids")

	// The server-side writeback still happened.
	require.Equal(t, 1, be.mutateCallCount(), "server vector writeback still fires")
	require.Equal(t, int64(3), p.Metrics().EmbedSucceeded, "all three embeds counted OK")
}

// TestEmbedWriteback_ShipFailureIsBestEffort asserts that when the segment
// manager's AddAndShip returns an error, the embed writeback still completes
// (server writeback succeeded, embedOK incremented) and only a WARN is logged —
// proving the client HNSW build is additive/best-effort. Criterion: Phase 3 Step 2.
func TestEmbedWriteback_ShipFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n1": vec32(1),
		"n2": vec32(2),
	}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{err: errors.New("ship boom")}
	p.AttachSegmentManager(fsm)

	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
	}
	// Must not panic; writeback must complete despite the ship error.
	runEmbedWorkerBatch(ctx, p, batch)

	require.Equal(t, 1, be.mutateCallCount(), "server vector writeback still fires despite ship failure")
	require.Equal(t, int64(2), p.Metrics().EmbedSucceeded, "embedOK increments despite ship failure")
	require.Equal(t, int64(0), p.Metrics().EmbedFailed, "a ship failure does NOT mark embeds failed")
	require.Equal(t, 1, fsm.calls, "AddAndShip was attempted")
}

// TestEmbedWriteback_BuildsAndShipsBM25 is the client criterion: at
// the embed writeback seam the pipeline calls AddAndShipFields with field-bearing
// Documents built from each item's server-composed Bm25Fields, alongside the HNSW
// AddAndShip. The decoded BM25 segment indexes exactly the embedded ids that
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
	require.Equal(t, 1, fsm.calls, "HNSW AddAndShip fires once")
	require.Equal(t, 1, fsm.fieldsCalls, "BM25 AddAndShipFields fires once")

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

// TestEmbedWriteback_BM25ShipFailureIsBestEffort asserts an AddAndShipFields error
// does NOT propagate: embed writeback completes, embedOK increments, embeds are not
// marked failed (best-effort/additive contract — server BM25 authoritative).
func TestEmbedWriteback_BM25ShipFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1)}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{fieldsErr: errors.New("bm25 ship boom")}
	p.AttachSegmentManager(fsm)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be,
			Bm25Fields: map[string]string{"summary": "only summary"}},
	})

	require.Equal(t, 1, be.mutateCallCount(), "server writeback still fires despite BM25 ship failure")
	require.Equal(t, int64(1), p.Metrics().EmbedSucceeded, "embedOK increments despite BM25 ship failure")
	require.Equal(t, int64(0), p.Metrics().EmbedFailed, "a BM25 ship failure does NOT mark embeds failed")
	require.Equal(t, 1, fsm.fieldsCalls, "AddAndShipFields was attempted")
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
