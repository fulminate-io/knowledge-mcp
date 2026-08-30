// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

	// The BM25 arm's surface: the delete seam and the durable cursor round trip.
	deleteErr     error
	deleteCalls   int
	deletedIDs    []searchengine.ExternalID
	deleteKeys    []graphKey
	cursors       map[graphKey][]*knowledgev1.LayerCursor
	cursorSaves   int
	cursorLoadErr error
	cursorSaveErr error
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

// The BM25 arm's three additions to ShipManager. They capture rather than assert:
// the arm's own tests drive them, and the embed-writeback tests in this file must
// keep compiling without caring that a third axis exists.
//
// THE CURSOR PAIR IS AN IN-MEMORY ROUND TRIP, not a no-op returning nil. A fake
// whose SaveBM25Cursors discarded its argument would let a cursor-advance assertion
// pass against an arm that never advanced anything.
func (f *fakeShipManager) DeleteFromBuckets(_ context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) error {
	f.deleteCalls++
	f.deletedIDs = append(f.deletedIDs, ids...)
	f.deleteKeys = append(f.deleteKeys, graphKey{GraphType: gt, GraphName: name})
	return f.deleteErr
}

func (f *fakeShipManager) LoadBM25Cursors(gt kgtypes.GraphType, name string) ([]*knowledgev1.LayerCursor, error) {
	if f.cursorLoadErr != nil {
		return nil, f.cursorLoadErr
	}
	return f.cursors[graphKey{GraphType: gt, GraphName: name}], nil
}

func (f *fakeShipManager) SaveBM25Cursors(gt kgtypes.GraphType, name string, cursors []*knowledgev1.LayerCursor) error {
	if f.cursorSaveErr != nil {
		return f.cursorSaveErr
	}
	if f.cursors == nil {
		f.cursors = map[graphKey][]*knowledgev1.LayerCursor{}
	}
	f.cursors[graphKey{GraphType: gt, GraphName: name}] = cursors
	f.cursorSaves++
	return nil
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

// TestEmbedWriteback_ShipsNoBM25 is what replaced the two tests this ticket
// removed — TestEmbedWriteback_BuildsAndShipsBM25 and
// TestEmbedWriteback_BM25DrainFailureIsBestEffort — and it asserts the OPPOSITE of
// what they did, on purpose.
//
// THEY WERE DELETED RATHER THAN WEAKENED because the behaviour they pinned no
// longer exists: the embed writeback seam does not ship BM25 documents at all. A
// test kept alive against a deleted path asserts nothing while reading as coverage.
//
// THIS IS THE INVERSE ASSERTION, and it is the one that matters now: the vector
// ship still fires, and the BM25 ship NEVER does. Without it, "the embed axis
// stopped shipping BM25" would be pinned by nothing, and a future re-introduction
// would be invisible. The HNSW leg is the known positive — it proves the seam ran
// at all, so the zero below is a real absence rather than a batch that never
// reached the ship.
func TestEmbedWriteback_ShipsNoBM25(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n1": vec32(1),
		"n2": vec32(2),
	}}
	p := New(Config{}, be, nil, fe.call)

	fsm := &fakeShipManager{}
	p.AttachSegmentManager(fsm)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
	})

	require.Equal(t, 1, fsm.calls,
		"KNOWN POSITIVE: the HNSW ship still fires, so the seam genuinely ran and the zero below "+
			"is an absence rather than a batch that never reached it")
	require.Len(t, fsm.gotIDs, 2, "and it carried both vectors")

	require.Zero(t, fsm.fieldsCalls,
		"the embed axis must NOT ship BM25 — a node's keyword document is produced by the BM25 "+
			"arm off the CorpusDelta feed, which is the decoupling this ticket exists for")
	require.Empty(t, fsm.fieldDocs, "and no field-bearing Documents are built here")

	require.Equal(t, int64(2), p.Metrics().EmbedSucceeded, "both embeds counted OK")
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
