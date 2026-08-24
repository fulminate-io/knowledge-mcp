// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// remapFixture builds a mock-engine pool holding ONE resident segment whose blob
// is genuinely in L2, and returns the manager, the instrumented cache and the
// segment's blob. This is the state reclaimMerged reaches after step (a): the
// merged blob is durable and the entry is published heap-backed, awaiting its
// mapping swap.
func remapFixture(t *testing.T) (*distManager[mockQuery, mockStats], *instrumentedCache, searchengine.SegmentBlob) {
	t.Helper()
	ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
	dm := newReclaimDMOverCache(t, ic)

	require.NoError(t, dm.engine.Add([]searchengine.Document{doc("a", "alpha"), doc("b", "beta")}))
	require.NoError(t, dm.engine.Flush())
	blobs := dm.engine.Export()
	require.Len(t, blobs, 1)
	merged := blobs[0]

	ic.Put(merged.ID, merged.Bytes)
	require.NoError(t, dm.engine.Import([]searchengine.SegmentBlob{merged}, nil))
	return dm, ic, merged
}

func (m *distManager[Q, S]) pendingRemapIDs() []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	out := make([]searchengine.SegmentID, 0, len(m.remapPending))
	for id := range m.remapPending {
		out = append(out, id)
	}
	return out
}

// TestRemapFailureConvergesOnNextTouch proves the primary convergence path: a
// remap that fails is REMEMBERED, and the next consumer touch repairs it.
func TestRemapFailureConvergesOnNextTouch(t *testing.T) {
	dm, ic, merged := remapFixture(t)

	// KNOWN-NEGATIVE CONTROL, and it is what stops this test passing on an
	// implementation that never degraded: with mapping HEALTHY the same call
	// leaves nothing pending. Without this, a build where GetMapped always
	// succeeds would report a convergence it never had to perform.
	dm.remapMerged(merged)
	require.Empty(t, dm.pendingRemapIDs(),
		"control: a healthy remap must leave NOTHING pending, or the degraded arm below proves nothing")

	// Now break the mapping arm and re-run the republication.
	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	dm.remapMerged(merged)
	require.Equal(t, []searchengine.SegmentID{merged.ID}, dm.pendingRemapIDs(),
		"a failed remap must be RECORDED as pending, not logged and forgotten")

	// A drain while the cause persists must NOT drop it — it is still repairable.
	dm.drainRemapPending()
	require.Equal(t, []searchengine.SegmentID{merged.ID}, dm.pendingRemapIDs(),
		"a drain that cannot repair must keep the id pending, not silently forfeit it")

	// Clear the cause; the next consumer touch repairs.
	ic.mu.Lock()
	ic.failMapping = false
	ic.mu.Unlock()

	dm.drainRemapPending()
	require.Empty(t, dm.pendingRemapIDs(),
		"once the cause clears, the next drain must converge and drop the id")
}

// TestRemapPendingStopsReArmingAtTheBound proves the terminus: bounded, loud, and
// STRICTLY NON-DESTRUCTIVE.
//
// Assertions (5) and (6) are the ones that matter most. An earlier draft of this
// terminus removed the merged blob from L2 and cleared l2Loaded; both passed
// their own behavioral test by construction, because a test that only checks
// "the loop stopped" cannot see that the stop destroyed the pool's only durable
// copy. These two assertions are what make this test FAIL on that shape.
func TestRemapPendingStopsReArmingAtTheBound(t *testing.T) {
	dm, ic, merged := remapFixture(t)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	l2Before := dm.l2Loaded.Load()

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	dm.remapMerged(merged)
	require.Equal(t, []searchengine.SegmentID{merged.ID}, dm.pendingRemapIDs())

	putsBefore := countOps(ic, "put", merged.ID)

	// Drive drains past the bound. The cause never clears.
	for range remapMaxAttempts + 2 {
		dm.drainRemapPending()
	}

	// (1) EXACTLY ONE additive re-Put was attempted at the bound.
	require.Equal(t, putsBefore+1, countOps(ic, "put", merged.ID),
		"the bound must attempt EXACTLY ONE re-Put of the merged bytes")

	// (2) TERMINAL: the id is gone from pending and further drains attempt no
	// further remaps. Counting GetMapped is what distinguishes "terminal" from
	// "still re-arming quietly".
	require.Empty(t, dm.pendingRemapIDs(), "past the bound the id must be TERMINAL, not still pending")
	mappedBefore := countOps(ic, "getmapped", merged.ID)
	dm.drainRemapPending()
	dm.drainRemapPending()
	require.Equal(t, mappedBefore, countOps(ic, "getmapped", merged.ID),
		"a terminal id must never be re-armed — further drains must attempt nothing")

	// (3) The published entry is still the CORRECT payload and still serves.
	hits := dm.engine.Search(mockQuery{term: "alpha"}, 10)
	require.NotEmpty(t, hits, "the correct heap-backed payload must stay published and searchable")

	// (4) ONE loud persistent-degradation Error naming the segment.
	logs := logBuf.String()
	require.Contains(t, logs, "level=ERROR", "the terminus must announce itself loudly")
	require.Contains(t, logs, merged.ID, "the terminal Error must name the segment")

	// (5) NON-DESTRUCTION: the merged blob is STILL in L2. Post-merge it is the
	// ONLY durable copy of its constituents' documents.
	_, present := ic.sizeOf(merged.ID)
	require.True(t, present,
		"the terminus must NOT remove the merged blob — it is the only durable copy of its constituents")

	// (6) NON-DESTRUCTION: l2Loaded is untouched. Clearing it reaches no heal and
	// only re-runs the same short import.
	require.Equal(t, l2Before, dm.l2Loaded.Load(), "the terminus must NOT clear l2Loaded")
}

// countOps counts recorded cache operations of one kind against one id.
func countOps(ic *instrumentedCache, kind string, id searchengine.SegmentID) int {
	n := 0
	for _, op := range ic.opLog() {
		if op.kind == kind && op.id == id {
			n++
		}
	}
	return n
}
