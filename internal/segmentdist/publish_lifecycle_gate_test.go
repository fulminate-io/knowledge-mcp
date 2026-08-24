// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// capturingSlogHandler is a minimal slog.Handler that records every emitted record's
// level + message + attributes. The publish-bound / heal-disarm terminal-WARN
// assertions install it via slog.SetDefault so they can prove the loud terminal log
// fired (message + graph identity) rather than inferring it from state alone.
type capturingSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingSlogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingSlogHandler) WithGroup(string) slog.Handler            { return h }
func (h *capturingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

// warnsContaining returns the recorded WARN messages (rendered with their attrs)
// whose message text contains substr, so a test can assert a specific terminal WARN
// fired with its identifying attributes present.
func (h *capturingSlogHandler) warnsContaining(substr string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level != slog.LevelWarn || !strings.Contains(r.Message, substr) {
			continue
		}
		var b strings.Builder
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		out = append(out, b.String())
	}
	return out
}

// installCapturingSlog swaps the default slog logger for a capturing handler for the
// duration of the test, restoring the prior default on cleanup.
//
// A TEST THAT CALLS THIS MUST NOT CALL t.Parallel(). The default logger is
// PROCESS-GLOBAL: two tests holding it at once means one's handler replaces the
// other's, so the records a test asserts over are whichever peer happened to
// install last — and peers' unrelated records land in this test's capture. Serial
// tests all complete before any parallel test body runs, which is what keeps the
// swap contained. Every caller below is deliberately serial for this reason.
func installCapturingSlog(t *testing.T) *capturingSlogHandler {
	t.Helper()
	h := &capturingSlogHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// RETIRED — DO NOT RESTORE: TestManagerSkipsPublishUntilABatchSeals.
//
// It asserted that a run of sub-threshold write calls seals nothing, so no List,
// Ship or PublishManifest fires until a quiescence Flush force-seals the tails.
//
// THE REASON IT IS GONE IS THAT ITS PREMISE IS UNCONSTRUCTIBLE, not that any gate
// it watched was removed. The test needs a sub-threshold batch to stay BUFFERED AND
// UNSEALED across a Manager call, and the write path now force-seals every batch it
// is handed. That state cannot be reached through the Manager surface at all, so
// there is no rewrite of this test on the Manager — restoring it would mean
// asserting over a condition the surface can no longer produce, and it would pass
// vacuously rather than fail honestly.
//
// Anyone who wants the unsealed-tail precondition must build it directly on the
// engine via dm.engine.Add, which is what TestManagerFlushSealsSubThresholdTail
// (manager_owner_test.go) does — that is where the surviving Flush coverage lives.

// prefixIDs returns a copy of docs with every id prefixed — the established
// per-batch distinct-id technique so successive batches seal DISTINCT segments.
func prefixIDs(docs []searchengine.Document, prefix string) []searchengine.Document {
	for i := range docs {
		docs[i].ID = prefix + docs[i].ID
	}
	return docs
}

// TestPublishPendingRetry covers every publishPending SET point in publishResident —
// transport error (:367), 409 manifestIncompleteError skip (:362), coverage-read
// List error (:346), and coverage-ratio gate skip (:349) — plus the CLEAR on a later
// successful publish. Each set point must leave publishRetryPending()==true so a
// subsequent reconcile tick re-attempts the publish, and the successful re-attempt
// clears it.
//
// THE VEHICLE FOR "ONLY THE PENDING BIT DRIVES THIS" IS A BARE TICK, not a
// sub-threshold write. A write that seals nothing is no longer constructible
// through the Manager — every batch is force-sealed — so the retry-only condition
// is now built by ticking with nothing genuinely unshipped, which is the same
// branch the reconcile loop takes on a quiet graph.
func TestPublishPendingRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// (1) Transport error (publishResident :367): a non-409 PublishManifest error
	// sets the retry bit; a later tick retries and clears it; a further tick with
	// the bit clear and nothing dirty skips entirely.
	t.Run("transport_error_retries_and_clears", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		dm := mgr.managerFor(kgtypes.GraphCode, "retryRepo")

		gc.publishErr = errors.New("boom")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "retryRepo", hnswVecDocs(1024)))
		require.Error(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "retryRepo"),
			"a transport PublishManifest error surfaces from the tick that performs the publish")
		require.Equal(t, int64(1), gc.shipCalls.Load(), "the sealed segment shipped once")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was attempted once")
		require.True(t, dm.publishRetryPending(), "the transport error set the retry bit")

		// The failed tick kept its backlog, so this one re-emits the SAME documents to
		// the same bytes: the ship diff is empty and only the publish is genuinely
		// retried. That is the retry-bit path — nothing here is unshipped.
		gc.publishErr = nil
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "retryRepo"))
		require.Equal(t, int64(1), gc.shipCalls.Load(), "the re-emit ships an empty diff — no new Ship RPC")
		require.Equal(t, int64(2), gc.publishCalls.Load(), "the retry re-attempted the publish")
		require.False(t, dm.publishRetryPending(), "the successful publish cleared the retry bit")

		// Backlog drained + bit clear: the tick returns before any ship or publish.
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "retryRepo"))
		require.Equal(t, int64(2), gc.publishCalls.Load(), "no publish when nothing is dirty and no retry is pending")
	})

	// (2) 409 incomplete skip (publishResident :362): a manifestIncompleteError is a
	// logged SKIP (the tick returns nil) that still sets the retry bit.
	t.Run("incomplete_409_skip_retries", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		dm := mgr.managerFor(kgtypes.GraphCode, "i409Repo")

		gc.publishErr = &manifestIncompleteError{Missing: []string{"seg-x"}}
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "i409Repo", hnswVecDocs(1024)))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "i409Repo"),
			"a 409 incomplete manifest is a logged skip, not an error")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was attempted once")
		require.True(t, dm.publishRetryPending(), "the 409 skip set the retry bit")

		// The skip is not an error, so that tick drained its backlog. This one has
		// nothing to re-emit and runs PURELY off the pending bit.
		gc.publishErr = nil
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "i409Repo"))
		require.Equal(t, int64(2), gc.publishCalls.Load(), "the retry re-attempted the publish")
		require.False(t, dm.publishRetryPending(), "the successful publish cleared the retry bit")
	})

	// (3) Coverage-read List error (publishResident :346): a ship that SUCCEEDS
	// followed by a coverage-read List failure must set the retry bit — the ship
	// stamped the ids so hasUnshippedExport is false on the next tick, and without
	// this set the publish would never retry.
	t.Run("coverage_read_error_retries_and_clears", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		dm := mgr.managerFor(kgtypes.GraphCode, "covRepo")

		// First batch seeds + ships + publishes cleanly (seeded latches).
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "covRepo", hnswVecDocs(1024)))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "covRepo"))
		require.Equal(t, int64(1), gc.shipCalls.Load())
		require.Equal(t, int64(1), gc.publishCalls.Load())

		// Second sealed batch (DISTINCT ids). The ship succeeds (listErr does not
		// affect Ship), but publishCoverageOK's coverage-read List then fails →
		// publishResident returns the error at :346 and sets the retry bit.
		//
		// The batch is deliberately SMALL so the corpus stays clear of a power-of-two
		// partition-count straddle. Crossing one makes the next re-emit a full corpus
		// rebuild, which is expected behavior but changes segment membership wholesale
		// and would make this test's subject — the retry bit — hard to read.
		batch2 := prefixIDs(hnswVecDocs(16), "cr-")
		gc.listErr = errors.New("coverage read boom")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, "covRepo", batch2))
		require.Error(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "covRepo"),
			"the coverage-read List failure surfaces from the tick")
		require.Equal(t, int64(2), gc.shipCalls.Load(), "the second batch shipped")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was never reached (coverage-read failed first)")
		require.True(t, dm.publishRetryPending(), "the coverage-read error set the retry bit (RED if :346 omits setPublishPending)")

		// Heal the coverage read; the next tick retries the publish and clears the bit.
		gc.listErr = nil
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "covRepo"))
		require.Equal(t, int64(2), gc.publishCalls.Load(), "the retry re-attempted the publish")
		require.False(t, dm.publishRetryPending(), "the successful publish cleared the retry bit")
	})

	// (4) Coverage-ratio gate skip (publishResident :349): a restarted writer that
	// ships a sub-floor tail BEFORE loading the prior corpus has a resident set far
	// below the coverage ratio → publishCoverageOK returns !ok → the publish is
	// SKIPPED (no error) and the retry bit is set. Healing the resident set (server
	// re-import) then lets a sub-threshold add publish successfully and clear it.
	t.Run("coverage_gate_skip_sets_and_clears", func(t *testing.T) {
		mgrs, svc := newMultiWriterFleet(t, 2)
		b := mgrs[1]
		gt, name := kgtypes.GraphCode, "covGateRepo"

		// Writer B ships a multi-segment prior corpus so the coverage ratio is ARMED
		// (>= residentBackstopFloor of shipped docs across DISTINCT segments).
		const corpusSegs = 4
		for s := range corpusSegs {
			batch := prefixIDs(hnswVecDocs(searchCorpusN), fmt.Sprintf("cg-b%d-", s))
			require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
		}
		// One tick discharges the whole seeded window: this is fixture construction and
		// only the END STATE matters. What lands is PARTITION-shaped rather than one
		// segment per batch, so assert that a prior corpus exists rather than counting
		// the batches that built it.
		require.NoError(t, b.ReEmitDirtyBuckets(ctx, gt, name))
		priorCorpus := shippedHNSWIDs(svc)
		require.NotEmpty(t, priorCorpus, "B ships the full prior corpus")

		// Writer A restarts (same writer_id, fresh L2) and force-seals a SUB-FLOOR tail
		// (< residentBackstopFloor=64 docs) via Flush WITHOUT loading the prior corpus.
		// Its resident set is far below the coverage ratio → the publish is gate-skipped
		// AND resident stays below the floor so the server re-import heal can later fire.
		aRestart := restartFleetMember(t, svc, 0, t.TempDir())
		tail := prefixIDs(hnswVecDocs(10), "cg-tail-")
		require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, tail)) // force-seals the tail, ships nothing
		require.NoError(t, aRestart.Flush(ctx, gt, name))                 // ship → coverage skip
		aDM := aRestart.managerFor(gt, name)
		require.True(t, aDM.publishRetryPending(),
			"the coverage-ratio gate skip set the retry bit (RED if :349 omits setPublishPending)")

		// The prior corpus survived the skipped publish (no refcount-GC wipe).
		afterSkip := shippedHNSWIDs(svc)
		for id := range priorCorpus {
			require.Contains(t, afterSkip, id, "every prior-corpus blob survives the gated sub-ratio publish")
		}

		// Heal A: the cheap server re-import climbs its resident set above the floor.
		degenerate, err := aRestart.ReconcileResidentDegenerate(ctx, gt, name)
		require.NoError(t, err)
		require.False(t, degenerate, "the server re-import restored coverage")
		require.GreaterOrEqual(t, aRestart.ResidentDocCount(gt, name), residentBackstopFloor,
			"resident climbed above the floor after the re-import")

		// A further write plus its tick now retries the publish against the healed
		// resident set — coverage passes, PublishManifest succeeds, the retry bit clears.
		require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(16), "cg-heal-")))
		require.NoError(t, aRestart.ReEmitDirtyBuckets(ctx, gt, name))
		require.False(t, aDM.publishRetryPending(), "the successful publish cleared the retry bit")
	})
}

// TestPublishRetryBoundedOnPersistentCoverageSkip models the NON-recovering case
// observed live: a restarted writer whose read-engine resident is
// permanently sub-ratio (List(0) stays large-shipped, resident never rises, and
// ReconcileResidentDegenerate is NOT called). Pre-fix, publishResident re-armed
// publishPending on every coverage skip, so every drain's Flush re-fired the coverage
// read (a steady per-drain page-read floor) FOREVER. Post-fix (markCoverageSkip bound), the
// re-arm stops after coverageSkipMaxStreak skips at a non-rising resident with a
// terminal WARN, and a genuine resident rise re-arms and lands once above ratio.
//
// RED-first: against pre-fix code the retry bit stays set and the coverage-read List
// climbs unbounded across the flush loop; this test fails there and passes only once
// the bound lands. Runs under -race (Flush + engine reads exercise the helper's
// resident-before-lock ordering).
//
// DELIBERATELY NOT PARALLEL. Shared resource: the process-global default slog
// logger, which this test swaps for a capturing handler to assert over the
// records the path emits. Concurrent peers would both install and restore that
// one global, so the handler this test reads could be a peer's, and a peer's
// unrelated records would land in this test's capture.
func TestPublishRetryBoundedOnPersistentCoverageSkip(t *testing.T) {
	warns := installCapturingSlog(t)
	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	b := mgrs[1]
	gt, name := kgtypes.GraphCode, "covBoundRepo"

	// Writer B ships a multi-segment prior corpus so the coverage ratio is ARMED
	// (>= residentBackstopFloor of shipped docs across DISTINCT segments).
	const corpusSegs = 4
	for s := range corpusSegs {
		batch := prefixIDs(hnswVecDocs(searchCorpusN), fmt.Sprintf("cb-b%d-", s))
		require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
	}
	// One tick discharges the seeded window — this is fixture construction, so only
	// the shipped end state matters.
	require.NoError(t, b.ReEmitDirtyBuckets(ctx, gt, name))

	// Writer A restarts (same writer_id, fresh L2) and force-seals a SUB-FLOOR tail
	// WITHOUT loading the prior corpus — its resident stays far below the coverage
	// ratio. The first Flush ships the tail and coverage-skips the publish (skip #1).
	aRestart := restartFleetMember(t, svc, 0, t.TempDir())
	tail := prefixIDs(hnswVecDocs(10), "cb-tail-")
	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, tail)) // force-seals the tail, ships nothing
	require.NoError(t, aRestart.Flush(ctx, gt, name))                 // ship → coverage skip #1
	aDM := aRestart.managerFor(gt, name)
	require.True(t, aDM.publishRetryPending(), "the first coverage skip armed the retry bit")

	// Baseline the coverage-read List counter AFTER the force-seal skip, then re-Flush
	// repeatedly WITHOUT healing the resident. Each re-fired Flush (driven only by the
	// pending bit — nothing new is sealed) pays a coverage-read List. Pre-fix that
	// climbs on every Flush; post-fix the bound suppresses the re-arm after
	// coverageSkipMaxStreak skips so the reads stop.
	baseList := aRestart.view.listCalls.Load()
	const extraFlushes = 8
	for range extraFlushes {
		require.NoError(t, aRestart.Flush(ctx, gt, name))
	}
	afterList := aRestart.view.listCalls.Load()

	// The retry bit is now CLEARED — the bound stopped re-arming (RED pre-fix: the bit
	// stays set forever because every skip re-arms it).
	require.False(t, aDM.publishRetryPending(),
		"after coverageSkipMaxStreak coverage skips at a non-rising resident, the bound stops re-arming the retry bit")

	// The coverage-read List does not climb AT ALL across the loop — and this arm no
	// longer discriminates, because TWO mechanisms now stack on it: the suppression
	// bound stops re-firing the publish after coverageSkipMaxStreak skips, AND the
	// publish-path denominator memo serves every re-fire inside coveragePublishMemoTTL
	// from cache (a retry-only re-Flush seals nothing, so shipNew early-returns without
	// invalidating). Do NOT read a zero here as the bound alone working; the bound's
	// real red-green content is the publishRetryPending assertion above. This one is
	// now a cost floor: the retry-only loop pays no denominator round-trip.
	require.Equal(t, int64(0), afterList-baseList,
		"a retry-only re-Flush loop pays ZERO coverage-read Lists (suppressed re-arms plus memo hits inside the TTL)")

	// The terminal suppression WARN fired with graph identity.
	suppressWarns := warns.warnsContaining("suspending coverage-skip republish")
	require.NotEmpty(t, suppressWarns, "the bound emits a terminal suppression WARN")
	require.Contains(t, suppressWarns[0], name, "the suppression WARN carries the graph identity")

	// A genuine resident rise re-arms the retry and it lands once above ratio: heal the
	// resident above the floor, then a NEW sealed segment re-fires the publish, which
	// now passes coverage and clears the bit.
	degenerate, err := aRestart.ReconcileResidentDegenerate(ctx, gt, name)
	require.NoError(t, err)
	require.False(t, degenerate, "the server re-import restored coverage")
	require.GreaterOrEqual(t, aRestart.ResidentDocCount(gt, name), residentBackstopFloor,
		"resident climbed above the floor after the re-import")

	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(searchCorpusN), "cb-heal-")))
	require.NoError(t, aRestart.ReEmitDirtyBuckets(ctx, gt, name))
	require.False(t, aDM.publishRetryPending(),
		"a resident rise + genuine new export re-armed the publish and it landed once above ratio")
}

// TestPublishSkipWarnsCarryGraphIdentity pins the target identity on BOTH
// publish-SKIP WARNs in publishResident — the degenerate/incomplete-live-set skip and
// the 409 missing-blob skip. A skip WARN without the manager's target cannot be
// attributed to a graph, so a skip storm across many graphs is unreadable.
//
// The two subtests deliberately run at DIFFERENT graph types. graphsel routes a code
// graph's instance name into GraphSelector.Repo and every other family's into .Name,
// so a code-only fixture can never fail when `name` is omitted (Name is legitimately
// empty there) and a knowledge-only fixture can never fail when `repo` is omitted.
// One subtest per side covers both halves of the mapping.
//
// RED-first: against the unmodified publishResident both WARNs render message +
// format/live/reason (or format/missing) only, so both `graph=` assertions fail.
//
// DELIBERATELY NOT PARALLEL. Shared resource: the process-global default slog
// logger, which this test swaps for a capturing handler to assert over the
// records the path emits. Concurrent peers would both install and restore that
// one global, so the handler this test reads could be a peer's, and a peer's
// unrelated records would land in this test's capture.
func TestPublishSkipWarnsCarryGraphIdentity(t *testing.T) {
	// The CODE-GRAPH side: repo carries the instance name, name is empty.
	t.Run("degenerate_live_set_skip_warn", func(t *testing.T) {
		warns := installCapturingSlog(t)
		ctx := context.Background()
		mgrs, svc := newMultiWriterFleet(t, 2)
		b := mgrs[1]
		gt, name := kgtypes.GraphCode, "covWarnRepo"

		// Writer B ships a multi-segment prior corpus so the coverage ratio is ARMED.
		const corpusSegs = 4
		for s := range corpusSegs {
			batch := prefixIDs(hnswVecDocs(searchCorpusN), fmt.Sprintf("cw-b%d-", s))
			require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
		}
		// One tick discharges the seeded window — fixture construction, end state only.
		require.NoError(t, b.ReEmitDirtyBuckets(ctx, gt, name))

		// Writer A restarts with a FRESH L2 (so its resident is degenerate) and
		// force-seals a sub-floor tail: ship lands, publish is coverage-gate skipped.
		aRestart := restartFleetMember(t, svc, 0, t.TempDir())
		tail := prefixIDs(hnswVecDocs(10), "cw-tail-")
		require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, tail)) // force-seals the tail, ships nothing
		require.NoError(t, aRestart.Flush(ctx, gt, name))                 // ship → coverage skip

		skips := warns.warnsContaining("degenerate/incomplete live set")
		require.NotEmpty(t, skips, "the coverage-gate skip emits its WARN")
		require.Contains(t, skips[0], "graph=code", "the skip WARN carries the graph type")
		require.Contains(t, skips[0], "repo="+name, "the skip WARN carries the code graph's instance name in repo")
	})

	// The KNOWLEDGE-GRAPH side: name carries the instance name, repo is empty. This is
	// the subtest that guards the `name` attr — without it, omitting `name` from either
	// WARN would pass every gate.
	t.Run("missing_blob_409_skip_warn", func(t *testing.T) {
		warns := installCapturingSlog(t)
		ctx := context.Background()
		_, gc := newSegmentHarness(t)
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

		gc.publishErr = &manifestIncompleteError{Missing: []string{"seg-x"}}
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphKnowledge, "kgWarnGraph", hnswVecDocs(1024)))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphKnowledge, "kgWarnGraph"),
			"a 409 incomplete manifest is a logged skip, not an error")

		skips := warns.warnsContaining("agent reported missing blob(s)")
		require.NotEmpty(t, skips, "the 409 skip emits its WARN")
		// One Contains pins all three identity attrs at once: the graph type, a
		// present AND non-empty `name`, and a present but EMPTY `repo` (the inverted
		// half of the mapping). It leans on the attr ORDER — identity trio first, then
		// detail — which every skip WARN in this package shares.
		require.Contains(t, skips[0], "graph=knowledge name=kgWarnGraph repo= format=",
			"the 409 skip WARN carries the graph identity trio ahead of the detail attrs")
	})
}
