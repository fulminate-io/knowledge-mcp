// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// segDeleteCall is one recorded drive of the segment delete seam.
type segDeleteCall struct {
	gt   kgtypes.GraphType
	name string
	ids  []string
}

// fakeSegmentDeleter records every DeleteFromBuckets call so a test can assert
// the exact (graph type, instance key, ids) a prune propagated. err, when set,
// is returned so a test can tell log-and-swallow from never-called.
type fakeSegmentDeleter struct {
	mu    sync.Mutex
	calls []segDeleteCall
	err   error
}

func (f *fakeSegmentDeleter) DeleteFromBuckets(_ context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) error {
	f.mu.Lock()
	f.calls = append(f.calls, segDeleteCall{gt: gt, name: name, ids: append([]string(nil), ids...)})
	f.mu.Unlock()
	return f.err
}

func (f *fakeSegmentDeleter) recorded() []segDeleteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]segDeleteCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestInterceptManage_Prune_RequiresGraph asserts prune rejects an empty graph
// (and ONLY that — no allowlist) before firing any RPC.
func TestInterceptManage_Prune_RequiresGraph(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"prune"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "prune without graph must error")
	assert.Contains(t, toolResultText(res), "requires")
	assert.Empty(t, ix.requests(), "no Index RPC when the required graph is missing")
}

// TestInterceptManage_Prune_AllTombstones asserts a graph-only prune (no before)
// fires ONE INDEX_OP_PRUNE RPC with before_nanos=0 and renders the count.
func TestInterceptManage_Prune_AllTombstones(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 7}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	require.EqualValues(t, 1, ix.indexCalls.Load(), "exactly one prune Index RPC")
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_PRUNE, reqs[0].GetOperation())
	assert.Equal(t, "knowledge", reqs[0].GetTarget().GetGraph())
	assert.EqualValues(t, 0, reqs[0].GetBeforeNanos(), "no before => prune all (0 cutoff)")

	body := toolResultText(res)
	assert.Contains(t, body, "Pruned 7 tombstoned node(s)")
	assert.Contains(t, body, "all tombstones")
}

// TestInterceptManage_Prune_GenericGraphRouting asserts prune routes a non-code,
// non-knowledge graph (practice) generically via the Language selector — no
// allowlist gate.
func TestInterceptManage_Prune_GenericGraphRouting(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 2}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"practice","name":"go"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_PRUNE, reqs[0].GetOperation())
	assert.Equal(t, "practice", reqs[0].GetTarget().GetGraph())
	assert.Equal(t, "go", reqs[0].GetTarget().GetLanguage(), "practice routes name via Language")
}

// TestInterceptManage_Prune_RelativeBefore asserts a relative window ("24h") is
// lowered to an absolute unix-nanos cutoff roughly 24h in the past.
func TestInterceptManage_Prune_RelativeBefore(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 1}
	before := time.Now()
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"24h"}`)
	after := time.Now()
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	cutoff := reqs[0].GetBeforeNanos()
	lo := before.Add(-24 * time.Hour).UnixNano()
	hi := after.Add(-24 * time.Hour).UnixNano()
	assert.GreaterOrEqual(t, cutoff, lo, "cutoff is ~24h before now (lower bound)")
	assert.LessOrEqual(t, cutoff, hi, "cutoff is ~24h before now (upper bound)")
}

// TestInterceptManage_Prune_RFC3339Before asserts an absolute RFC3339 timestamp
// is parsed straight to its unix-nanos.
func TestInterceptManage_Prune_RFC3339Before(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 1}
	const ts = "2026-01-02T15:04:05Z"
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"`+ts+`"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	want, perr := time.Parse(time.RFC3339, ts)
	require.NoError(t, perr)
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, want.UnixNano(), reqs[0].GetBeforeNanos(), "RFC3339 before parses to its exact unix-nanos")
}

// TestInterceptManage_Prune_GarbageBefore asserts an unparseable before is a
// validation error with no RPC fired.
func TestInterceptManage_Prune_GarbageBefore(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"not-a-duration"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "unparseable before must error")
	assert.Contains(t, toolResultText(res), "unparseable before")
	assert.Empty(t, ix.requests(), "no Index RPC on a bad before")
}

// TestInterceptManage_Prune_WiresPrunedIDsToSegmentDelete asserts the ids the
// server reports reach the segment delete seam exactly once, batched, on the
// corpus the prune actually addressed. The two legs carry DIFFERENT ids so
// neither can pass on the other's recording, and the practice leg is what proves
// the instance key routes rather than only the graph type.
func TestInterceptManage_Prune_WiresPrunedIDsToSegmentDelete(t *testing.T) {
	t.Run("knowledge", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"k1", "k2"}}
		del := &fakeSegmentDeleter{}
		handled, res := manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "prune: %s", toolResultText(res))

		calls := del.recorded()
		require.Len(t, calls, 1, "one batched delete for the whole id set, never a per-id loop")
		assert.Equal(t, kgtypes.GraphKnowledge, calls[0].gt)
		assert.Equal(t, knowledgeDefaultName, calls[0].name, "the knowledge corpus keys under its default instance")
		assert.Equal(t, []string{"k1", "k2"}, calls[0].ids)
	})

	t.Run("practice", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 1, prunedIDs: []string{"p1"}}
		del := &fakeSegmentDeleter{}
		handled, res := manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"practice","name":"go"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "prune: %s", toolResultText(res))

		calls := del.recorded()
		require.Len(t, calls, 1)
		assert.Equal(t, kgtypes.GraphPractice, calls[0].gt)
		assert.Equal(t, "go", calls[0].name, "the instance key routes, not just the graph type")
		assert.Equal(t, []string{"p1"}, calls[0].ids)
	})
}

// TestInterceptManage_Prune_NoPrunedIDsSkipsSegmentDelete covers the rolling
// deploy: a server built before the ids existed reports a count and no ids.
// Doing nothing is exactly the pre-field behavior and is safe — but silently,
// so it warns. The second leg is the known-negative that keeps the warn honest:
// an ordinary zero-count prune must not trip it.
func TestInterceptManage_Prune_NoPrunedIDsSkipsSegmentDelete(t *testing.T) {
	t.Run("old-server", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 3} // count, but no ids
		del := &fakeSegmentDeleter{}
		var handled bool
		var res kgtools.ToolResult
		logged := captureSlog(func() {
			handled, res = manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		})
		require.True(t, handled)
		require.False(t, res.IsError, "an id-less response must not turn the prune into an error")
		assert.Empty(t, del.recorded(), "no ids means nothing to propagate")
		assert.Contains(t, toolResultText(res), "Pruned 3 tombstoned node(s)")
		assert.Contains(t, logged, "pruned rows but no ids")
	})

	t.Run("genuine-noop", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 0}
		del := &fakeSegmentDeleter{}
		var handled bool
		var res kgtools.ToolResult
		logged := captureSlog(func() {
			handled, res = manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		})
		require.True(t, handled)
		require.False(t, res.IsError)
		assert.Empty(t, del.recorded())
		assert.NotContains(t, logged, "pruned rows but no ids",
			"a prune that removed nothing has nothing to warn about")
	})
}

// TestInterceptManage_Prune_NilDeleterIsSafe is a CHARACTERIZATION GUARD, green
// before and after the wiring: a headless client with no segment engine still
// prunes successfully. It exists so the nil-deleter guard inside
// reEmitDeletedFromSegments cannot be removed silently.
func TestInterceptManage_Prune_NilDeleterIsSafe(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 1, prunedIDs: []string{"x1"}}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Pruned 1 tombstoned node(s)")
}

// TestInterceptManage_Prune_SegmentDeleteFailureIsReported asserts a failing
// bucket delete is NON-FATAL BUT REPORTED: the prune already completed on the
// server, so a local re-emit failure must never be reported as a failed prune —
// and must never be reported as an unqualified successful one either.
//
// RE-POINTED, not merely renamed. It used to assert the swallow: exit non-error
// plus the bare "Pruned 2" ack, which is exactly what the code produced when the
// error had no path out of reEmitDeletedFromSegments. The non-error half is
// unchanged and still correct; the ack assertion now demands the qualifier that
// names what did not land. The clean leg below is the known-negative — without it,
// appending the warning to every prune would pass.
func TestInterceptManage_Prune_SegmentDeleteFailureIsReported(t *testing.T) {
	t.Run("failed re-emit", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"y1", "y2"}}
		del := &fakeSegmentDeleter{err: errors.New("bucket delete boom")}
		var handled bool
		var res kgtools.ToolResult
		logged := captureSlog(func() {
			handled, res = manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		})
		require.True(t, handled)
		require.False(t, res.IsError, "a re-emit failure must not turn a completed prune into an error")
		body := toolResultText(res)
		assert.Contains(t, body, "Pruned 2 tombstoned node(s)", "the sweep that DID complete is still reported")
		assert.Contains(t, body, "shipped segment corpus was NOT updated",
			"a hard prune leaves nothing to re-learn from, so an unqualified ack tells the caller "+
				"the documents are gone locally when only a rebuild will remove them")
		assert.Contains(t, body, "bucket delete boom")
		assert.Len(t, del.recorded(), 1, "the seam was driven; the error came from it")
		assert.Contains(t, logged, "segment delete re-emit failed")
	})

	t.Run("known-negative: a clean re-emit carries no qualifier", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"y1", "y2"}}
		del := &fakeSegmentDeleter{}
		handled, res := manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		require.True(t, handled)
		require.False(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "Pruned 2 tombstoned node(s)")
		assert.NotContains(t, body, "shipped segment corpus was NOT updated")
	})
}

// warningIndexer stamps a server WARNING onto an otherwise-successful Index
// response — the partial-success envelope a prune returns when the sweep
// committed but the persist that followed it failed. It COMPOSES the shared
// fakeIndexer rather than growing a field on it, so the partial-success fixture
// stays owned by the prune tests that need it.
type warningIndexer struct {
	*fakeIndexer
	warning string
}

func (w *warningIndexer) Index(ctx context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error) {
	resp, err := w.fakeIndexer.Index(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Warning = w.warning
	return resp, nil
}

// TestInterceptManage_Prune_WarningIsLoudButNonfatal pins the client half of the
// partial-success contract. A warning means the server finished the sweep and
// something after it degraded — the rows are as gone as on a clean run, so the
// propagation must be IDENTICAL and the text must reach the caller unedited. The
// failure this guards against is silent in both directions: skipping propagation
// would leave the pruned documents resident locally until a full rebuild, and
// swallowing the text would hide a degraded server behind a clean-looking ack.
func TestInterceptManage_Prune_WarningIsLoudButNonfatal(t *testing.T) {
	const serverWarning = "prune: 2 node(s) were hard-deleted and the transaction committed, " +
		"but the follow-up persist of knowledge/default failed: disk full." +
		" The removal stands and every pruned id is reported; the on-disk snapshot may lag until the next successful save."

	// degradedPrune drives one warning-carrying prune against a shipper already
	// holding a tombstoned id, mirroring the clean-path fixture exactly so the
	// two paths can be compared assertion for assertion.
	degradedPrune := func(t *testing.T, del SegmentDeleter, ship SegmentShipper) kgtools.ToolResult {
		t.Helper()
		ix := &warningIndexer{
			fakeIndexer: &fakeIndexer{affectedCount: 2, prunedIDs: []string{"gone-1", "gone-2"}},
			warning:     serverWarning,
		}
		handled, res := InterceptManage(opCtx(),
			interceptTestDeps{gc: ix, deleter: del, shipper: ship},
			kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(`{"operation":"prune","graph":"knowledge"}`)})
		require.True(t, handled)
		require.False(t, res.IsError,
			"a warning is a DEGRADATION on a completed prune, never a failed call: %s", toolResultText(res))
		return res
	}

	t.Run("propagation_runs_exactly_as_on_a_clean_success", func(t *testing.T) {
		del := &fakeSegmentDeleter{}
		ship := &fakeRebuildShipper{}

		degradedPrune(t, del, ship)

		calls := del.recorded()
		require.Len(t, calls, 1, "the bucket re-emit fires on the degraded path too — the rows are just as gone")
		assert.Equal(t, kgtypes.GraphKnowledge, calls[0].gt)
		assert.Equal(t, []string{"gone-1", "gone-2"}, calls[0].ids, "the FULL reported set propagates, not a subset")
		require.Len(t, ship.noted, 1, "the delete stamp is written")
		assert.Equal(t, []searchengine.ExternalID{"gone-1", "gone-2"}, ship.noted[0])
		require.Len(t, ship.seeded, 1, "the persisted tombstone record is seeded")
		assert.Equal(t, []searchengine.ExternalID{"gone-1", "gone-2"}, ship.seeded[0])
	})

	t.Run("the_warning_is_rendered_and_logged_verbatim", func(t *testing.T) {
		var res kgtools.ToolResult
		logged := captureSlog(func() {
			res = degradedPrune(t, &fakeSegmentDeleter{}, &fakeRebuildShipper{})
		})

		body := toolResultText(res)
		assert.Contains(t, body, "Pruned 2 tombstoned node(s)", "the ordinary ack survives alongside the warning")
		assert.Contains(t, body, serverWarning,
			"the server's text reaches the caller unedited — a reworded summary strips the detail the operator acts on")
		assert.Contains(t, logged, serverWarning, "and it is logged, not only rendered")
	})

	t.Run("a_clean_prune_renders_no_warning", func(t *testing.T) {
		// THE KNOWN-POSITIVE CONTROL for the two legs above: without it a render
		// that appended the warning banner unconditionally, or one that dropped
		// the warning entirely, would be indistinguishable from the correct one.
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"gone-1", "gone-2"}}
		del := &fakeSegmentDeleter{}
		handled, res := manageCallWithDeleter(t, ix, del, `{"operation":"prune","graph":"knowledge"}`)
		require.True(t, handled)
		require.False(t, res.IsError)

		body := toolResultText(res)
		assert.Contains(t, body, "Pruned 2 tombstoned node(s)")
		assert.NotContains(t, body, "WARNING", "a clean prune carries no warning banner")
		assert.Len(t, del.recorded(), 1, "and it propagates exactly like the degraded one")
	})
}

// TestInterceptManage_Prune_SeedsThePersistedTombstoneRecord gates the OTHER half of
// carrying a hard prune into the local corpus. A pruned row is gone server-side, so no
// later delta or tombstone scan can ever report it again: if the record does not learn
// it here, it never learns it at all, and a crash between the prune and the re-emit
// ship leaves the ids alive in every shipped blob for the next import to resurrect.
func TestInterceptManage_Prune_SeedsThePersistedTombstoneRecord(t *testing.T) {
	const seededWatermark = int64(1700000000000000000)

	// seededPrune drives one prune against a shipper already holding a tombstoned id,
	// which is what makes the merge legs discriminating: a handler that REPLACED the
	// record with its own window would erase that id.
	seededPrune := func(t *testing.T) *fakeRebuildShipper {
		t.Helper()
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"gone-1", "gone-2"}}
		ship := &fakeRebuildShipper{}
		ship.watermark = seededWatermark
		ship.tombstoned = []searchengine.ExternalID{"already-dead"}

		handled, res := manageCallWithDeleterAndShipper(t, ix, &fakeSegmentDeleter{}, ship,
			`{"operation":"prune","graph":"knowledge"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "prune: %s", toolResultText(res))
		return ship
	}

	t.Run("pruned_ids_enter_the_record", func(t *testing.T) {
		ship := seededPrune(t)
		assert.Equal(t,
			[]searchengine.ExternalID{"already-dead", "gone-1", "gone-2"},
			ship.tombstoned,
			"the record must MERGE the pruned ids into what it already held, survivors first")
	})

	t.Run("watermark_is_not_advanced", func(t *testing.T) {
		// THE VIOLATING-INPUT GATE: the watermark may move only when a publish landed.
		ship := seededPrune(t)
		assert.Equal(t, seededWatermark, ship.savedWatermark())
	})

	t.Run("engines_receive_the_merged_set", func(t *testing.T) {
		ship := seededPrune(t)
		require.Len(t, ship.seeded, 1, "one seed per prune")
		assert.Equal(t,
			[]searchengine.ExternalID{"already-dead", "gone-1", "gone-2"},
			ship.seeded[0],
			"handing over only the pruned ids would ERASE the accumulated set")
	})

	t.Run("pruned_ids_are_stamped", func(t *testing.T) {
		// THE CATCHER for the stamper site: stamping the MERGED set instead of this
		// window's own pruned ids would re-date the pre-existing delete and suppress a
		// write that legitimately followed it.
		ship := seededPrune(t)
		require.Len(t, ship.noted, 1, "one stamp per prune")
		assert.Equal(t, []searchengine.ExternalID{"gone-1", "gone-2"}, ship.noted[0],
			"only the ids THIS window reported may be stamped")
	})

	t.Run("unreadable_record_does_not_fail_the_prune", func(t *testing.T) {
		// The server already committed the hard deletes, so a local record write that
		// fails must warn and continue rather than report a completed prune as an error.
		ix := &fakeIndexer{affectedCount: 2, prunedIDs: []string{"gone-1", "gone-2"}}
		del := &fakeSegmentDeleter{}
		ship := &fakeRebuildShipper{}
		ship.loadErr = errors.New("record unreadable")

		var (
			handled bool
			res     kgtools.ToolResult
		)
		logged := captureSlog(func() {
			handled, res = manageCallWithDeleterAndShipper(t, ix, del, ship,
				`{"operation":"prune","graph":"knowledge"}`)
		})

		require.True(t, handled)
		require.False(t, res.IsError, "a record-write failure must not turn a completed prune into an error")
		assert.Contains(t, toolResultText(res), "Pruned 2 tombstoned node(s)")
		assert.Len(t, del.recorded(), 1, "the bucket delete must still fire")
		assert.Contains(t, logged, "could not record the pruned ids as deleted")
	})
}
