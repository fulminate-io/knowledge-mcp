// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// testkit_fake_test.go is the successor to the deleted source_test.go
// httptest+GraphClient harness. With SegmentService gone, the machinery tests
// drive a Manager/distManager over an in-memory segmentSource instead of a wire
// double. Two pieces:
//
//   - sharedServerFake: the in-memory model of the server/agent segment registry —
//     a per-target blob store + per-(writerID,format) manifests + refcount-GC. It is
//     the direct successor to the deleted fakeSegmentService, exposing the SAME
//     read surface (mu, key, byKey, gen, seenWriterIDs, manifests, ListDelta) the
//     multiwriter/reconcile/restart tests read. A fakeSegmentSource view over it is
//     what a Manager/distManager consumes.
//   - fakeSegmentSource: an in-memory segmentSource bound to one (target, writerID)
//     over a sharedServerFake, carrying the per-leg CALL COUNTERS the old
//     countingCaller exposed (listCalls/fetchCalls/shipCalls/shipBlobs/pruneCalls/
//     publishCalls/recordedShipBytes) plus Fetch hooks (reject error, hang, drop-one)
//     the fetch/reconcile tests inject. It is injected via withSegmentSource.

// loginStateStub is the trivial loginState the migrated tests hand NewManager. A
// test that injects a source via withSegmentSource does not care about the login
// gate (the injected source short-circuits it), but NewManager still needs a
// non-nil caller; loginStateStub{true} models a logged-in cloud client.
type loginStateStub struct{ loggedIn bool }

func (s loginStateStub) LoggedIn(context.Context) bool { return s.loggedIn }

var _ loginState = loginStateStub{}

// sharedServerFake is the in-memory segment registry model. It stamps monotonic
// generations on Ship (server-as-ordering-point), serves ListDelta/Fetch from a
// per-target map, and models the registry manifest swap + refcount-GC on Publish.
// It is shared across the K views a multi-writer fleet builds.
type sharedServerFake struct {
	mu    sync.Mutex
	byKey map[string][]*knowledgev1.SegmentBlobProto
	gen   uint64
	// manifests holds each writer's published id-set, keyed target-key -> (writerID
	// "\x00" format) -> id-set. Publish swaps a writer's manifest and refcount-GCs
	// blobs no manifest references — the in-memory mirror of the server's
	// __segment_manifests + NOT EXISTS GC.
	manifests map[string]map[string]map[string]bool
	// seenWriterIDs records every non-empty writer_id observed on any inbound leg —
	// the window onto the last-connection liveness wiring.
	seenWriterIDs map[string]bool
}

func newSharedServerFake() *sharedServerFake {
	return &sharedServerFake{
		byKey:         map[string][]*knowledgev1.SegmentBlobProto{},
		manifests:     map[string]map[string]map[string]bool{},
		seenWriterIDs: map[string]bool{},
	}
}

func (f *sharedServerFake) key(t *knowledgev1.GraphSelector) string {
	return t.GetGraph() + ":" + t.GetRepo() + t.GetAccount() + t.GetName()
}

// recordWriter notes a non-empty writer_id (caller holds mu).
func (f *sharedServerFake) recordWriter(writerID string) {
	if writerID != "" {
		f.seenWriterIDs[writerID] = true
	}
}

// viewFor returns a fakeSegmentSource bound to (target, writerID) over this shared
// server — the seam a Manager/distManager consumes. Every leg the view drives
// records into this shared server under the bound target+writerID.
func (f *sharedServerFake) viewFor(target *knowledgev1.GraphSelector, writerID string) *fakeSegmentSource {
	return &fakeSegmentSource{server: f, target: target, writerID: writerID}
}

// metaOf carries doc_count onto the meta exactly as the real server does — the
// coverage probe reads meta.DocCount off ListDelta.
func metaOf(b *knowledgev1.SegmentBlobProto) *knowledgev1.SegmentMetaProto {
	return &knowledgev1.SegmentMetaProto{
		Id: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration(), DocCount: b.GetDocCount(),
	}
}

// ship stamps monotonic generations on new blobs for (target, writerID); an already
// present id is a no-op (content-hash idempotency). Returns the stamped metas.
func (f *sharedServerFake) ship(target *knowledgev1.GraphSelector, writerID string, blobs []*knowledgev1.SegmentBlobProto) []*knowledgev1.SegmentMetaProto {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordWriter(writerID)
	k := f.key(target)
	existing := map[string]*knowledgev1.SegmentBlobProto{}
	for _, b := range f.byKey[k] {
		existing[b.GetId()] = b
	}
	var stamped []*knowledgev1.SegmentMetaProto
	for _, b := range blobs {
		if cur, ok := existing[b.GetId()]; ok {
			stamped = append(stamped, metaOf(cur))
			continue
		}
		f.gen++
		stored := &knowledgev1.SegmentBlobProto{
			Id: b.GetId(), Format: b.GetFormat(), Generation: f.gen,
			DocCount: b.GetDocCount(), Bytes: b.GetBytes(),
		}
		f.byKey[k] = append(f.byKey[k], stored)
		existing[b.GetId()] = stored
		stamped = append(stamped, metaOf(stored))
	}
	return stamped
}

// listDelta returns the metas for (target) with generation > sinceGen, ascending.
func (f *sharedServerFake) listDelta(target *knowledgev1.GraphSelector, writerID string, sinceGen uint64) []*knowledgev1.SegmentMetaProto {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordWriter(writerID)
	k := f.key(target)
	var metas []*knowledgev1.SegmentMetaProto
	for _, b := range f.byKey[k] {
		if b.GetGeneration() > sinceGen {
			metas = append(metas, metaOf(b))
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].GetGeneration() < metas[j].GetGeneration() })
	return metas
}

// listMetas is the read surface a few tests (e2e, prune_reconcile) call directly on
// svc to count/inspect the server's segments for a target. The successor to the
// deleted fakeSegmentService.ListDelta connect method (the SegmentService RPC is
// gone); it returns the ascending metas with generation > sinceGen.
func (f *sharedServerFake) listMetas(target *knowledgev1.GraphSelector, sinceGen uint64) []*knowledgev1.SegmentMetaProto {
	return f.listDelta(target, "", sinceGen)
}

// fetch serves the requested ids for (target) from the store.
func (f *sharedServerFake) fetch(target *knowledgev1.GraphSelector, writerID string, ids []searchengine.SegmentID) []*knowledgev1.SegmentBlobProto {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordWriter(writerID)
	k := f.key(target)
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var blobs []*knowledgev1.SegmentBlobProto
	for _, b := range f.byKey[k] {
		if want[b.GetId()] {
			blobs = append(blobs, b)
		}
	}
	return blobs
}

// prune deletes the named ids for (target), returning how many were removed.
func (f *sharedServerFake) prune(target *knowledgev1.GraphSelector, ids []searchengine.SegmentID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(target)
	del := map[string]bool{}
	for _, id := range ids {
		del[id] = true
	}
	kept := f.byKey[k][:0]
	removed := 0
	for _, b := range f.byKey[k] {
		if del[b.GetId()] {
			removed++
			continue
		}
		kept = append(kept, b)
	}
	f.byKey[k] = kept
	return removed
}

// publish swaps (target, writerID, format)'s manifest and refcount-GCs blobs no
// manifest references. Returns how many blobs it removed.
func (f *sharedServerFake) publish(target *knowledgev1.GraphSelector, writerID, format string, ids []searchengine.SegmentID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordWriter(writerID)
	k := f.key(target)
	if f.manifests[k] == nil {
		f.manifests[k] = map[string]map[string]bool{}
	}
	mk := writerID + "\x00" + format
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	f.manifests[k][mk] = set

	referenced := map[string]bool{}
	for _, s := range f.manifests[k] {
		for id := range s {
			referenced[id] = true
		}
	}
	kept := f.byKey[k][:0]
	removed := 0
	for _, b := range f.byKey[k] {
		if referenced[b.GetId()] {
			kept = append(kept, b)
			continue
		}
		removed++
	}
	f.byKey[k] = kept
	return removed
}

// fakeSegmentSource is an in-memory segmentSource bound to one (target, writerID)
// over a sharedServerFake. It subsumes the deleted countingCaller: every leg bumps
// the corresponding call counter, and Ship records the serialized request size so
// the ship-split test can assert per-Ship byte bounds. Fetch hooks (rejectFetch,
// hangFetch, dropFetchID) model the byte-ceiling / cancellation / short-fetch cases.
type fakeSegmentSource struct {
	server   *sharedServerFake
	target   *knowledgev1.GraphSelector
	writerID string

	listCalls    atomic.Int64
	fetchCalls   atomic.Int64
	shipCalls    atomic.Int64
	shipBlobs    atomic.Int64
	pruneCalls   atomic.Int64
	publishCalls atomic.Int64

	shipReqMu    sync.Mutex
	shipReqBytes []int

	// Fetch hooks (all optional). rejectFetch, when set and returning non-nil, fails
	// the Fetch with that error before serving (the byte-ceiling halving stand-in).
	// hangFetch, when set, blocks until the caller ctx is done (the C2 cancellation
	// probe). dropFetchID (guarded by hookMu) omits exactly that id from any Fetch
	// (the short-but-OK C1 skew).
	rejectFetch func(ids []searchengine.SegmentID) error
	hangFetch   bool
	// emptyFetch, when true, serves an EMPTY Fetch (no blobs, no error) even when the
	// server holds the ids — the post-restart "List shows the corpus but the engine
	// imports nothing" collapse the resident-vs-shipped probe must catch.
	emptyFetch bool

	hookMu      sync.Mutex
	dropFetchID string
	dropActive  bool

	// verifies overrides verifiesCompletenessServerSide (default false — an in-memory
	// server-model source keeps the client subset check, matching the rpc/local shape
	// the machinery tests grew up on).
	verifies bool
	// listErr, when set, fails List (the seed-List transient-failure probe).
	listErr error
	// publishErr, when set, fails PublishManifest with it (the publishPending
	// retry probe). publishCalls still counts the attempt.
	publishErr error
}

var _ segmentSource = (*fakeSegmentSource)(nil)

// setDrop configures the short-but-OK Fetch: omit exactly id from any Fetch while
// active. The successor to shortFetchService.setDrop.
func (s *fakeSegmentSource) setDrop(id string, active bool) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.dropFetchID = id
	s.dropActive = active
}

func (s *fakeSegmentSource) List(_ context.Context, sinceGen uint64) ([]searchengine.SegmentMeta, error) {
	s.listCalls.Add(1)
	if s.listErr != nil {
		return nil, s.listErr
	}
	protos := s.server.listDelta(s.target, s.writerID, sinceGen)
	metas := make([]searchengine.SegmentMeta, 0, len(protos))
	for _, m := range protos {
		metas = append(metas, metaFromMeta(m))
	}
	return metas, nil
}

func (s *fakeSegmentSource) Fetch(ctx context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	s.fetchCalls.Add(1)
	if s.rejectFetch != nil {
		if err := s.rejectFetch(ids); err != nil {
			return nil, err
		}
	}
	if s.hangFetch {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.emptyFetch {
		return nil, nil
	}
	protos := s.server.fetch(s.target, s.writerID, ids)
	s.hookMu.Lock()
	drop, active := s.dropFetchID, s.dropActive
	s.hookMu.Unlock()
	blobs := make([]searchengine.SegmentBlob, 0, len(protos))
	for _, b := range protos {
		if active && b.GetId() == drop {
			continue // short-but-OK: omit exactly this id
		}
		blobs = append(blobs, blobFromProto(b))
	}
	return blobs, nil
}

func (s *fakeSegmentSource) Ship(_ context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	s.shipCalls.Add(1)
	s.shipBlobs.Add(int64(len(blobs)))
	// Record the summed per-Ship serialized blob size — the same measure the chunker
	// bounds each Ship batch by (chunker.go: proto.Size(b)+16 per blob), so the
	// ship-split test's per-Ship sanity-bound assertion reads a faithful request size.
	// (The old ShipRequest wrapper proto is gone with the SegmentService.)
	total := 0
	for _, b := range blobs {
		total += proto.Size(b) + 16
	}
	s.shipReqMu.Lock()
	s.shipReqBytes = append(s.shipReqBytes, total)
	s.shipReqMu.Unlock()
	return s.server.ship(s.target, s.writerID, blobs), nil
}

func (s *fakeSegmentSource) Prune(ids []searchengine.SegmentID) (int, error) {
	s.pruneCalls.Add(1)
	return s.server.prune(s.target, ids), nil
}

func (s *fakeSegmentSource) PublishManifest(format string, digests []segmentDigest) (int, error) {
	s.publishCalls.Add(1)
	if s.publishErr != nil {
		return 0, s.publishErr
	}
	ids := make([]searchengine.SegmentID, len(digests))
	for i, d := range digests {
		ids[i] = d.ID
	}
	return s.server.publish(s.target, s.writerID, format, ids), nil
}

func (s *fakeSegmentSource) verifiesCompletenessServerSide() bool { return s.verifies }

// bindTarget implements the targetBindable seam: newSegmentSource calls it to
// re-bind this injected view to the graph the manager is building/probing.
func (s *fakeSegmentSource) bindTarget(target *knowledgev1.GraphSelector) { s.target = target }

// recordedShipBytes returns a copy of the per-Ship-call serialized request sizes.
func (s *fakeSegmentSource) recordedShipBytes() []int {
	s.shipReqMu.Lock()
	defer s.shipReqMu.Unlock()
	out := make([]int, len(s.shipReqBytes))
	copy(out, s.shipReqBytes)
	return out
}

// metaFromMeta maps a wire meta carrier to an engine SegmentMeta (the successor to
// the deleted metaFromProto, which mapped ListDelta metas the rpc source consumed).
func metaFromMeta(p *knowledgev1.SegmentMetaProto) searchengine.SegmentMeta {
	return searchengine.SegmentMeta{
		ID:         p.GetId(),
		Format:     p.GetFormat(),
		Generation: p.GetGeneration(),
		DocCount:   int(p.GetDocCount()),
		DeadCount:  int(p.GetDeadCount()),
	}
}

// newSegmentHarness is the migrated harness entry point: it returns a fresh
// sharedServerFake and a default fakeSegmentSource view over it (bound to a default
// target + empty writerID). Machinery tests read svc.* off the server and inject the
// view via withSegmentSource / hand it to buildManager. The default view's target is
// overwritten by buildManager (which binds the test's real target); tests that call
// NewManager directly inject withSegmentSource(svc.viewFor(target, "")).
func newSegmentHarness(t testingTB) (*sharedServerFake, *fakeSegmentSource) {
	t.Helper()
	svc := newSharedServerFake()
	return svc, svc.viewFor(&knowledgev1.GraphSelector{}, "")
}

// testingTB is the minimal testing.TB subset newSegmentHarness needs (Helper),
// matching the testing.TB the deleted harness took.
type testingTB interface {
	Helper()
	TempDir() string
}

// bindViewTarget binds src's underlying fakeSegmentSource view to target so its
// server reads/writes land under the right target-key. It unwraps the known
// fault-injecting wrappers (failAfterWarmSource) to reach the inner view. A source
// that carries no fakeSegmentSource (e.g. localSegmentSource) is left untouched.
func bindViewTarget(src segmentSource, target *knowledgev1.GraphSelector) {
	switch s := src.(type) {
	case *fakeSegmentSource:
		s.target = target
	case *failAfterWarmSource:
		bindViewTarget(s.inner, target)
	}
}

// fleetWriterID returns the distinct, restart-stable 16-lowercase-hex writer_id for
// fleet member n. n+1 keeps member 0 off the all-zeros sentinel. It is a pure
// fmt.Sprintf helper — no longer tied to the deleted writerid.go machine-id path.
func fleetWriterID(n int) string {
	return fmt.Sprintf("%016x", n+1)
}

// TestBlobProtoRoundTripsDocCount pins the doc_count plumbing: blobToProto carries
// SegmentBlob.DocCount into the wire carrier and blobFromProto reads it back
// unchanged, so the per-segment live doc count survives the ship → store → list
// round-trip the coverage levers depend on. (Relocated from the deleted source_test.go
// — blobToProto/blobFromProto survive the SegmentService deletion.)
func TestBlobProtoRoundTripsDocCount(t *testing.T) {
	orig := searchengine.SegmentBlob{
		ID:         "seg-dc",
		Format:     "hnsw",
		Generation: 7,
		DocCount:   1024,
		Bytes:      []byte("payload"),
	}
	p := blobToProto(orig)
	require.Equal(t, int32(1024), p.GetDocCount(), "blobToProto carries DocCount into the proto")
	back := blobFromProto(p)
	require.Equal(t, orig.DocCount, back.DocCount, "blobFromProto reads DocCount back unchanged")
	require.Equal(t, orig.ID, back.ID)
	require.Equal(t, orig.Format, back.Format)
	require.Equal(t, orig.Generation, back.Generation)
}
