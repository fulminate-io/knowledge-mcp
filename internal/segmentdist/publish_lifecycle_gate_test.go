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
func installCapturingSlog(t *testing.T) *capturingSlogHandler {
	t.Helper()
	h := &capturingSlogHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// TestManagerSkipsPublishUntilABatchSeals is the lifecycle-gate regression: a run
// of sub-threshold AddAndShip/AddAndShipFields calls (each far below MinSegmentDocs)
// seals nothing, so the gate returns before ensureShippedSeeded — ZERO List, ZERO
// Ship, ZERO PublishManifest. Only the quiescence Flush force-seals the two tails,
// and then exactly two ships + two publishes fire (one per format).
func TestManagerSkipsPublishUntilABatchSeals(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	// Four sub-threshold batches per format (16 docs each, 64 total per format, all
	// << MinSegmentDocs=1024). Prefix every id per batch so the batches carry DISTINCT
	// ids and the coalescing buffer accumulates a 64-doc tail per format.
	for b := range 4 {
		vecs := hnswVecDocs(16)
		fields := bm25FieldDocs(16)
		for i := range vecs {
			vecs[i].ID = fmt.Sprintf("g-b%d-%s", b, vecs[i].ID)
		}
		for i := range fields {
			fields[i].ID = fmt.Sprintf("g-b%d-%s", b, fields[i].ID)
		}
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "gateRepo", vecs))
		require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphCode, "gateRepo", fields))
	}

	// No-progress invariant: the gate returned before ensureShippedSeeded on every
	// sub-threshold add — the unsealed tails have an empty Export, so
	// hasUnshippedExport is false and publishPending is false.
	require.Equal(t, int64(0), gc.listCalls.Load(), "sub-threshold adds never seed/List")
	require.Equal(t, int64(0), gc.shipCalls.Load(), "sub-threshold adds never Ship")
	require.Equal(t, int64(0), gc.publishCalls.Load(), "sub-threshold adds never PublishManifest")

	// Flush force-seals BOTH tails into one HNSW + one BM25 segment, then ships +
	// publishes each sealed format exactly once.
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphCode, "gateRepo"))
	require.Equal(t, int64(2), gc.shipCalls.Load(), "Flush ships the two sealed tails (HNSW + BM25)")
	require.Equal(t, int64(2), gc.publishCalls.Load(), "Flush publishes each sealed format once")
}

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
// subsequent SUB-THRESHOLD add (hasUnshippedExport()==false — driven only by the
// pending bit) re-attempts the publish, and the successful re-attempt clears it.
func TestPublishPendingRetry(t *testing.T) {
	ctx := context.Background()

	// (1) Transport error (publishResident :367): a non-409 PublishManifest error
	// sets the retry bit; a sub-threshold add retries and clears it; a further add
	// with the bit clear and nothing unshipped skips entirely.
	t.Run("transport_error_retries_and_clears", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		dm := mgr.managerFor(kgtypes.GraphCode, "retryRepo")

		gc.publishErr = errors.New("boom")
		require.Error(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "retryRepo", hnswVecDocs(1024)),
			"a transport PublishManifest error surfaces as an AddAndShip error")
		require.Equal(t, int64(1), gc.shipCalls.Load(), "the sealed segment shipped once")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was attempted once")
		require.True(t, dm.publishRetryPending(), "the transport error set the retry bit")

		// Sub-threshold add: hasUnshippedExport is false (the sealed segment is already
		// shipped), so ONLY the pending bit drives the retry — the publish succeeds now.
		gc.publishErr = nil
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "retryRepo", hnswVecDocs(16)))
		require.Equal(t, int64(1), gc.shipCalls.Load(), "no new ship on the sub-threshold retry")
		require.Equal(t, int64(2), gc.publishCalls.Load(), "the retry re-attempted the publish")
		require.False(t, dm.publishRetryPending(), "the successful publish cleared the retry bit")

		// Bit clear + nothing unshipped: the gate skips entirely — no new publish.
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "retryRepo", hnswVecDocs(16)))
		require.Equal(t, int64(2), gc.publishCalls.Load(), "no publish when nothing sealed and no retry pending")
	})

	// (2) 409 incomplete skip (publishResident :362): a manifestIncompleteError is a
	// logged SKIP (AddAndShip returns nil) that still sets the retry bit.
	t.Run("incomplete_409_skip_retries", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		dm := mgr.managerFor(kgtypes.GraphCode, "i409Repo")

		gc.publishErr = &manifestIncompleteError{Missing: []string{"seg-x"}}
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "i409Repo", hnswVecDocs(1024)),
			"a 409 incomplete manifest is a logged skip, not an error")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was attempted once")
		require.True(t, dm.publishRetryPending(), "the 409 skip set the retry bit")

		gc.publishErr = nil
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "i409Repo", hnswVecDocs(16)))
		require.Equal(t, int64(2), gc.publishCalls.Load(), "the retry re-attempted the publish")
		require.False(t, dm.publishRetryPending(), "the successful publish cleared the retry bit")
	})

	// (3) Coverage-read List error (publishResident :346): a ship that SUCCEEDS
	// followed by a coverage-read List failure must set the retry bit — the ship
	// stamped the ids so hasUnshippedExport is false on the next tick, and without
	// this set the publish would never retry.
	t.Run("coverage_read_error_retries_and_clears", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		dm := mgr.managerFor(kgtypes.GraphCode, "covRepo")

		// First batch seeds + ships + publishes cleanly (seeded latches).
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "covRepo", hnswVecDocs(1024)))
		require.Equal(t, int64(1), gc.shipCalls.Load())
		require.Equal(t, int64(1), gc.publishCalls.Load())

		// Second sealed batch (DISTINCT ids). The ship succeeds (listErr does not
		// affect Ship), but publishCoverageOK's coverage-read List then fails →
		// publishResident returns the error at :346 and sets the retry bit.
		batch2 := prefixIDs(hnswVecDocs(1024), "cr-")
		gc.listErr = errors.New("coverage read boom")
		require.Error(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "covRepo", batch2),
			"the coverage-read List failure surfaces as an AddAndShip error")
		require.Equal(t, int64(2), gc.shipCalls.Load(), "the second segment shipped")
		require.Equal(t, int64(1), gc.publishCalls.Load(), "PublishManifest was never reached (coverage-read failed first)")
		require.True(t, dm.publishRetryPending(), "the coverage-read error set the retry bit (RED if :346 omits setPublishPending)")

		// Heal the coverage read; a sub-threshold add retries the publish and clears.
		gc.listErr = nil
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "covRepo", hnswVecDocs(16)))
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
			require.NoError(t, b.AddAndShip(ctx, gt, name, batch))
		}
		priorCorpus := shippedHNSWIDs(svc)
		require.Len(t, priorCorpus, corpusSegs, "B ships the full prior corpus")

		// Writer A restarts (same writer_id, fresh L2) and force-seals a SUB-FLOOR tail
		// (< residentBackstopFloor=64 docs) via Flush WITHOUT loading the prior corpus.
		// Its resident set is far below the coverage ratio → the publish is gate-skipped
		// AND resident stays below the floor so the server re-import heal can later fire.
		aRestart := restartFleetMember(t, svc, 0, t.TempDir())
		tail := prefixIDs(hnswVecDocs(10), "cg-tail-")
		require.NoError(t, aRestart.AddAndShip(ctx, gt, name, tail)) // sub-threshold: just buffers
		require.NoError(t, aRestart.Flush(ctx, gt, name))            // force-seal → ship → coverage skip
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

		// A sub-threshold add now retries the publish against the healed resident set —
		// coverage passes, PublishManifest succeeds, the retry bit clears.
		require.NoError(t, aRestart.AddAndShip(ctx, gt, name, prefixIDs(hnswVecDocs(16), "cg-heal-")))
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
		require.NoError(t, b.AddAndShip(ctx, gt, name, batch))
	}

	// Writer A restarts (same writer_id, fresh L2) and force-seals a SUB-FLOOR tail
	// WITHOUT loading the prior corpus — its resident stays far below the coverage
	// ratio. The first Flush ships the tail and coverage-skips the publish (skip #1).
	aRestart := restartFleetMember(t, svc, 0, t.TempDir())
	tail := prefixIDs(hnswVecDocs(10), "cb-tail-")
	require.NoError(t, aRestart.AddAndShip(ctx, gt, name, tail)) // sub-threshold: just buffers
	require.NoError(t, aRestart.Flush(ctx, gt, name))            // force-seal → ship → coverage skip #1
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

	require.NoError(t, aRestart.AddAndShip(ctx, gt, name, prefixIDs(hnswVecDocs(searchCorpusN), "cb-heal-")))
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
			require.NoError(t, b.AddAndShip(ctx, gt, name, batch))
		}

		// Writer A restarts with a FRESH L2 (so its resident is degenerate) and
		// force-seals a sub-floor tail: ship lands, publish is coverage-gate skipped.
		aRestart := restartFleetMember(t, svc, 0, t.TempDir())
		tail := prefixIDs(hnswVecDocs(10), "cw-tail-")
		require.NoError(t, aRestart.AddAndShip(ctx, gt, name, tail)) // sub-threshold: just buffers
		require.NoError(t, aRestart.Flush(ctx, gt, name))            // force-seal → ship → coverage skip

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
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

		gc.publishErr = &manifestIncompleteError{Missing: []string{"seg-x"}}
		require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, "kgWarnGraph", hnswVecDocs(1024)),
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
