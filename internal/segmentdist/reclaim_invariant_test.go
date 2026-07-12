// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// invariantT is the minimal testing surface assertLiveSetBackedByL2 needs. It is
// satisfied by *testing.T (the matrix tests pass their real t, so a violation
// fails the test loudly) AND by the self-test's recorder (which captures the
// Errorf reports to prove the helper actually trips). Errorf is NON-fatal (no
// FailNow/Goexit), so the self-test can drive a deliberately-broken state and
// inspect the result without unwinding the test goroutine.
type invariantT interface {
	Helper()
	Errorf(format string, args ...any)
}

// assertLiveSetBackedByL2 is the CEO acceptance bar's central invariant, shared by
// the whole prune-safety matrix. It is GENERIC over [Q, S any] so the SAME helper
// serves both engine instantiations — the HNSW engine ([]byte, struct{}) and the
// BM25 engine (bm25.Query, *bm25.CorpusStats). It asserts three properties of the
// live segment set:
//
//  1. EVERY live segment is L2-backed: for every blob in dm.engine.Export(),
//     dm.cache.Get(blob.ID) is present. No live id is ever orphaned from L2 — the
//     anti-false-prune guarantee.
//  2. NO live id was reclaimed: no id in removedSoFar (the set the test's
//     instrumented cache accumulated as reclaimMerged/InvalidateLocal called
//     Remove) is still present in dm.engine.Export(). On the Manager.NewManager
//     path (no seam) this clause is vacuous — pass an empty or disk-derived
//     removedSoFar.
//  3. NO doc was lost: the caller-supplied searchLiveIDs closure (which issues the
//     format-specific full-corpus query the generic helper cannot construct
//     itself) returns exactly expectLive. Pass searchLiveIDs == nil to skip
//     clause 3 when a test asserts only L2 backing (e.g. on-disk-only checks).
//
// NOTE on the merged blob's Generation: doMerge surfaces the consolidated blob
// with Generation==0 (newEntry never stamps it). That is harmless here — the L2
// cache keys by id + raw bytes and ignores Generation — so a clause-1 cache.Get on
// the merged id hits regardless.
func assertLiveSetBackedByL2[Q, S any](
	t invariantT,
	dm *distManager[Q, S],
	removedSoFar map[searchengine.SegmentID]struct{},
	expectLive map[searchengine.ExternalID]struct{},
	searchLiveIDs func() []searchengine.ExternalID,
) {
	t.Helper()

	exported := dm.engine.Export()
	liveIDs := make(map[searchengine.SegmentID]struct{}, len(exported))
	for _, b := range exported {
		liveIDs[b.ID] = struct{}{}
		// Clause 1: every live segment must have a backing L2 file.
		if _, ok := dm.cache.Get(b.ID); !ok {
			t.Errorf("live segment %s is NOT backed by an L2 file (false-prune of a live id)", b.ID)
		}
	}

	// Clause 2: nothing the reclaim path removed may still be live.
	for id := range removedSoFar {
		if _, stillLive := liveIDs[id]; stillLive {
			t.Errorf("id %s was Removed from L2 yet is still live in Export() (removed a live id)", id)
		}
	}

	// Clause 3: full-corpus search returns exactly the expected live doc set.
	if searchLiveIDs == nil {
		return
	}
	got := make(map[searchengine.ExternalID]struct{})
	for _, id := range searchLiveIDs() {
		got[id] = struct{}{}
	}
	want := sortedKeys(expectLive)
	have := sortedKeys(got)
	if fmt.Sprint(want) != fmt.Sprint(have) {
		t.Errorf("full-corpus search live-id set != expected live set (a doc was lost or a dead doc resurfaced): want %v, got %v", want, have)
	}
}

// sortedKeys returns the map keys sorted, for stable set comparison in assertions.
func sortedKeys[V any](m map[searchengine.ExternalID]V) []searchengine.ExternalID {
	out := make([]searchengine.ExternalID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestAssertLiveSetBackedByL2SelfTest exercises the invariant helper itself against
// BOTH format instantiations: a clean state passes, and a deliberately orphaned
// live id (present in Export but Removed from the instrumented cache) makes each
// clause fail. This confirms the assertion detects the false-prune condition it
// guards and that it type-checks for both [Q,S] shapes.
func TestAssertLiveSetBackedByL2SelfTest(t *testing.T) {
	// --- HNSW instantiation ([]byte, struct{}) via a real embed engine. ---
	t.Run("hnsw_clean_passes_and_orphan_fails", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		dm := mgr.managerFor("code", "selftest-hnsw")

		docs := vecContentDocs(searchengine.DefaultMinSegmentDocs)
		require.NoError(t, dm.engine.Add(docs))
		// Warm every sealed segment into L2 by hand (no ship needed for the self-test).
		for _, b := range dm.engine.Export() {
			dm.cache.Put(b.ID, b.Bytes)
		}
		require.NotEmpty(t, dm.engine.Export(), "expected at least one sealed segment")

		// Clean state: clauses 1+2 hold (clause 3 skipped — nil closure).
		rec := &recorderT{}
		assertLiveSetBackedByL2(rec, dm, map[searchengine.SegmentID]struct{}{}, nil, nil)
		require.False(t, rec.failed, "clean state must pass the invariant: %v", rec.msgs)

		// Orphan a live id: claim it was Removed while it is still in Export() →
		// clause 2 must fire.
		liveID := dm.engine.Export()[0].ID
		rec = &recorderT{}
		assertLiveSetBackedByL2(rec, dm,
			map[searchengine.SegmentID]struct{}{liveID: {}}, nil, nil)
		require.True(t, rec.failed, "a Removed-yet-live id must fail clause 2")

		// Orphan from L2: delete a live segment's file → clause 1 must fire.
		dm.cache.Remove(liveID)
		rec = &recorderT{}
		assertLiveSetBackedByL2(rec, dm, map[searchengine.SegmentID]struct{}{}, nil, nil)
		require.True(t, rec.failed, "a live id missing its L2 file must fail clause 1")
	})

	// --- BM25 instantiation (bm25.Query, *bm25.CorpusStats) via a real engine. ---
	t.Run("bm25_clean_passes_and_orphan_fails", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		dm := mgr.bm25ManagerFor("code", "selftest-bm25")

		docs := vecContentDocs(searchengine.DefaultMinSegmentDocs)
		require.NoError(t, dm.engine.Add(docs))
		for _, b := range dm.engine.Export() {
			dm.cache.Put(b.ID, b.Bytes)
		}
		require.NotEmpty(t, dm.engine.Export(), "expected at least one sealed BM25 segment")

		rec := &recorderT{}
		assertLiveSetBackedByL2(rec, dm, map[searchengine.SegmentID]struct{}{}, nil, nil)
		require.False(t, rec.failed, "BM25: clean state must pass the invariant: %v", rec.msgs)

		liveID := dm.engine.Export()[0].ID
		rec = &recorderT{}
		assertLiveSetBackedByL2(rec, dm,
			map[searchengine.SegmentID]struct{}{liveID: {}}, nil, nil)
		require.True(t, rec.failed, "BM25: a Removed-yet-live id must fail clause 2")

		dm.cache.Remove(liveID)
		rec = &recorderT{}
		assertLiveSetBackedByL2(rec, dm, map[searchengine.SegmentID]struct{}{}, nil, nil)
		require.True(t, rec.failed, "BM25: a live id missing its L2 file must fail clause 1")
	})
}

// recorderT is a non-fatal invariantT that captures Errorf reports, letting the
// self-test drive a deliberately-broken state and inspect whether the invariant
// helper tripped — a helper that never fails would prove nothing.
type recorderT struct {
	failed bool
	msgs   []string
}

func (r *recorderT) Helper() {}
func (r *recorderT) Errorf(format string, args ...any) {
	r.failed = true
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}
