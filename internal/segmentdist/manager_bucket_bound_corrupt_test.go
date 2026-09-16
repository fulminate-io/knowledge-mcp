// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_corrupt_test.go — THE CORRUPT-SEGMENT SEAM OF THE BOUND: a
// corruption found by a CONSOLIDATION reaches the same withdrawal a corruption found
// by a SEARCH reaches, and a census that withdrew one is not treated as a census that
// learned nothing.
//
// Driven by the PRESERVED INCIDENT ARTIFACT rather than by a synthesized failure: the
// error these rows observe is raised by the real BM25 reader walking the real bytes
// and attributed by the format's own RaiseCorruptIn. On the v0.10.6 release candidate
// the same condition failed one graph's bound 19 consecutive times and was quarantined
// only when an unrelated search happened to touch the same bytes.
//
// The sibling file holds the rows about a partition that cannot be consolidated for
// reasons that are not corruption, and the fixture helpers both files share.

package segmentdist

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// incidentPayload returns the PAYLOAD of the preserved incident artifact — the
// production blob whose dictionary addresses a posting run past the end of its own
// payload, kept in this package's testdata since the store census was written.
//
// THE BYTES ARE THE FIXTURE. Nothing in this file constructs a corruption or types
// its message: the error the rows below observe is raised by the real BM25 reader
// walking these real bytes, attributed by the format's own RaiseCorruptIn to the
// segment's own content address.
func incidentPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "incident-fbc34f-enveloped.seg"))
	require.NoError(t, err, "the preserved incident artifact must be present")
	_, payload, err := searchengine.SplitStoredBlob(raw)
	require.NoError(t, err)
	return payload
}

// incidentInjectingBM25 is the real BM25 format with ONE behaviour added: the
// merge of the partition its marker names is run with the PRESERVED INCIDENT
// SEGMENT among its constituents, so that merge fails exactly as the RC's did.
//
// WHY AN INJECTION RATHER THAN A RESIDENT CANDIDATE. The incident blob indexes 107
// documents whose ids hash across every partition at any count above one, so it can
// never be a SINGLE-PARTITION candidate the way the RC's own corrupt segment was —
// that one was itself a per-partition merge product, and this preserved one is not.
// Everything else is production: the segment is imported and published like any
// other, the raise comes from the real reader walking the real bytes, the
// attribution is the format's own, and the injection stops the moment the engine
// stops publishing the segment — which is what makes "the next census consolidates
// the partition WITHOUT it" an observation rather than a fixture switch.
type incidentInjectingBM25 struct {
	bm25.Format
	marker     searchengine.ExternalID
	incident   searchengine.Segment[bm25.Query, *bm25.CorpusStats]
	published  func() bool
	injections *atomic.Int64
}

func (f *incidentInjectingBM25) MergeTo(
	dst searchengine.MergeSink,
	segs []searchengine.Segment[bm25.Query, *bm25.CorpusStats],
	accept []func(searchengine.ExternalID) bool,
) (int64, error) {
	if f.marker == "" || !acceptsMarker(accept, f.marker) || !f.published() {
		return f.Format.MergeTo(dst, segs, accept)
	}
	f.injections.Add(1)
	return f.Format.MergeTo(dst,
		append(append([]searchengine.Segment[bm25.Query, *bm25.CorpusStats](nil), segs...), f.incident),
		append(append([]func(searchengine.ExternalID) bool(nil), accept...),
			func(searchengine.ExternalID) bool { return true }))
}

// corruptBoundFixture wires an engine whose merges reach the preserved incident
// segment, over a REAL disk cache holding that segment's file, with the production
// corruption disposition (quarantineCorrupt) as its OnCorruptSegment hook.
func corruptBoundFixture(t *testing.T, name string) (
	*Manager, *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats],
	*distManager[bm25.Query, *bm25.CorpusStats], *diskSegmentCache, *incidentInjectingBM25,
) {
	t.Helper()
	payload := incidentPayload(t)
	incident, err := bm25.New().Decode(payload)
	require.NoError(t, err, "the incident blob still OPENS: its damage is below the header, which is why it was served")

	cache := newDiskSegmentCache(t.TempDir(), 0, bm25ReadAdvice)
	raw, err := os.ReadFile(filepath.Join("testdata", "incident-fbc34f-enveloped.seg"))
	require.NoError(t, err)
	require.NoError(t, cache.Put(incidentCorruptID, raw),
		"the store must hold the corrupt segment's FILE, or there is nothing for a quarantine to move aside")

	var injections atomic.Int64
	format := &incidentInjectingBM25{incident: incident, injections: &injections}
	var dm *distManager[bm25.Query, *bm25.CorpusStats]
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](format, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		ScratchDir:         t.TempDir(),
		// THE PRODUCTION DISPOSITION, called by name rather than re-implemented:
		// this is the same function both engines in manager_factory.go hand to
		// their engines, so a change to what a quarantine does reaches this row.
		OnCorruptSegment: func(cerr *searchengine.CorruptSegmentError) { quarantineCorrupt(cache, dm, cerr) },
	})
	t.Cleanup(engine.Close)
	format.published = func() bool {
		return slices.Contains(engine.ResidentSegmentIDs(), incidentCorruptID)
	}

	m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	dm = installBM25ArmWithCache(m, name, engine, cache)
	require.NoError(t, engine.Import([]searchengine.SegmentBlob{{ID: incidentCorruptID, Bytes: payload}}, nil),
		"the corrupt segment is PUBLISHED, exactly as the RC's was while the bound kept failing on it")
	require.True(t, format.published(), "PRECONDITION: the engine publishes the corrupt segment")
	return m, engine, dm, cache, format
}

// installBM25ArmWithCache is installBM25Arm with the L2 cache the corruption
// disposition needs, and it returns the arm so a row can drive the bound directly.
func installBM25ArmWithCache(
	m *Manager, name string,
	engine *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats],
	cache segmentL2Cache,
) *distManager[bm25.Query, *bm25.CorpusStats] {
	dm := newDistManager(engine, cache, graphSelector(boundGraphType, name), bm25.New().Name())
	gate := &constructionGate[bm25.Query, *bm25.CorpusStats]{dm: dm, done: make(chan struct{})}
	close(gate.done)
	m.mu.Lock()
	m.bm25Managers[graphKey{graphType: boundGraphType, graphName: name}] = gate
	m.mu.Unlock()
	return dm
}

// TestCorruptConstituentFoundByTheBoundIsQuarantined is requirement 3: a
// corruption the CONSOLIDATION path finds must reach the same withdrawal a
// corruption the SEARCH path finds reaches.
//
// THE SEAM ALREADY EXISTED AND THIS PATH DID NOT USE IT. searchengine's background
// merger routes a corrupt merge failure into reportCorrupt, which withdraws the
// segment from the published set and hands it to the owner's OnCorruptSegment —
// the quarantine. ReplaceBucket returned the same error and reported nothing, so on
// the RC the same segment failed the bound 19 times and was quarantined only when a
// SEARCH happened to touch it, six seconds after the last consolidation attempt.
func TestCorruptConstituentFoundByTheBoundIsQuarantined(t *testing.T) {
	// NOT PARALLEL, and the reason is the instrument rather than the fixture: these
	// rows read the ERROR and INFO lines the bound writes, which means replacing the
	// PROCESS-GLOBAL slog default for the duration of a bound call. A sibling running
	// beside them restores that default halfway through and the buffer then holds a
	// fraction of the run — measured here as a missing INFO line on a census that had
	// demonstrably consolidated. The package's other log-reading rows are serial for
	// the same reason.
	const name = "bound-corrupt"
	m, engine, dm, cache, format := corruptBoundFixture(t, name)

	const failingCount, healthyCount = 4200, 400
	failing := sealSolo(t, engine, 0, 0, failingCount)
	sealSolo(t, engine, 1, 0, healthyCount)
	format.marker = failing[0]
	require.Greater(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: the set must be over budget, or the bound never attempts a consolidation")
	before := m.ResidentSegmentCounts(boundGraphType, name)[bm25.New().Name()]

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	boundResidentSegments(dm, corruptBuckets)
	slog.SetDefault(prev)
	log := buf.String()

	require.Positive(t, format.injections.Load(),
		"ANTI-VACUITY: the corrupt segment must actually have reached a merge, or nothing below is about corruption")
	require.Contains(t, log, "corrupt segment "+incidentCorruptID,
		"the failure must carry the corruption, attributed by the format to the segment's own content address")
	require.NotContains(t, engine.ResidentSegmentIDs(), incidentCorruptID,
		"a corruption surfacing from a consolidation must WITHDRAW the segment, exactly as one surfacing from a merge "+
			"does: left published it is offered to every later candidate set and the same failure repeats forever — "+
			"19 times on the RC, ended by a search rather than by the bound")
	require.Contains(t, log, "CORRUPT SEGMENT quarantined and withdrawn from service",
		"and it must reach the owner's disposition, which is what moves the FILE aside")
	_, stillIndexed := cache.sizeOf(incidentCorruptID)
	require.False(t, stillIndexed, "the L2 index entry must be dropped, or the next load re-adopts the bad bytes")
	require.NoFileExists(t, cache.path(incidentCorruptID))
	require.FileExists(t, filepath.Join(cache.root, quarantineDirName, incidentCorruptID+".seg"),
		"the file is MOVED, not deleted: it is the only evidence of how the store came to hold it")

	// AND THE STATUS SURFACE SEES IT, through the accounting it already had: the
	// per-format resident segment count manage(status) renders is the engine's own,
	// and a withdrawn segment leaves it.
	after := m.ResidentSegmentCounts(boundGraphType, name)[bm25.New().Name()]
	require.Less(t, after, before+failingCount,
		"manage(status)'s per-format resident count must reflect the withdrawal (%d before the bound, %d after)", before, after)

	// THE NEXT CENSUS CONSOLIDATES THE PARTITION WITHOUT IT. This is what a
	// quarantine BUYS, and it is why the row drives a second crossing: the RC's
	// bucket 52 never consolidated at all, because the segment that failed it was
	// still in every candidate set.
	require.Greater(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: still over budget after the first census, so a second crossing happens")
	boundResidentSegments(dm, corruptBuckets)
	require.LessOrEqual(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"the next census must consolidate the partition the corrupt segment was failing: withdrawn, it is in no later "+
			"candidate set, and there is nothing left to refuse the merge")
	require.Equal(t, int64(1), format.injections.Load(),
		"and the corrupt segment must have been reached ONCE: a second injection means it was still being offered")
}

// TestResidentBoundReCensusesAfterAWithdrawal is the latch's third case, and the
// one that keeps this file's header honest.
//
// A CENSUS THAT WITHDREW SOMETHING CHANGED THE WORLD. It consolidated nothing, so by
// the take-nothing rule it would arm the latch — and the next crossing would then
// decline to census until the set had grown by a whole slack, while the partition
// that failed sits consolidatable the entire time, its corrupt constituent already
// gone. The header promises that partition is consolidated at the NEXT crossing; a
// latch armed here makes that "the next crossing that censuses", a slack of growth
// away.
//
// THE FIXTURE IS THE SINGLE-CANDIDATE REGIME on purpose: ONE partition offers
// candidates, and its consolidation is the one that fails on the corruption, so
// `consolidated` is zero and nothing else can clear the latch.
func TestResidentBoundReCensusesAfterAWithdrawal(t *testing.T) {
	// Serial, for the reason the sibling rows state.
	const name = "bound-withdraw-latch"
	_, engine, dm, _, format := corruptBoundFixture(t, name)

	failing := sealSolo(t, engine, 0, 0, searchengine.ResidentSegmentFanoutBudget+1)
	format.marker = failing[0]
	candidates := consolidatablePartitions(engine.SegmentSpans(corruptBuckets))
	require.Len(t, candidates, 1,
		"PRECONDITION: exactly ONE partition offers candidates, so a failed consolidation leaves `consolidated` at "+
			"zero and the latch's take-nothing rule is the only thing that can arm it")
	before := engine.ResidentSegmentCount()
	require.Greater(t, before, searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: over budget, or the bound returns at the crossing gate")

	boundResidentSegments(dm, corruptBuckets)

	require.Equal(t, int64(1), dm.segmentSpanCensuses(), "PRECONDITION: the first crossing censused")
	require.Equal(t, before-1, engine.ResidentSegmentCount(),
		"PRECONDITION: and consolidated NOTHING — its only candidate partition failed on the corruption, so the ONLY "+
			"segment that left the set is the corrupt one the failure withdrew")
	require.NotContains(t, engine.ResidentSegmentIDs(), incidentCorruptID,
		"PRECONDITION: but it DID withdraw the corrupt constituent, which is what makes this census different from "+
			"one that learned nothing")
	_, slack := residentLowWater(corruptBuckets)
	require.Less(t, dm.lastCensusCount.Load(), int64(1),
		"a census that withdrew a segment must CLEAR the latch: the partition it failed on is consolidatable now, and "+
			"an armed latch defers the retry until the set has grown by a whole slack (%d) while that material sits "+
			"resident — the header promises the next CROSSING, not the next crossing that happens to census", slack)

	// AND THE NEXT CROSSING TAKES IT, with no new material at all: not one segment
	// is sealed between the two calls, so the only thing that can have changed is
	// the latch.
	boundResidentSegments(dm, corruptBuckets)

	require.Equal(t, int64(2), dm.segmentSpanCensuses(), "the next crossing must census")
	require.LessOrEqual(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"and it must consolidate the partition the withdrawal freed: %d resident against a budget of %d",
		engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget)
	require.Equal(t, int64(1), format.injections.Load(),
		"the corrupt segment was offered ONCE: withdrawn means out of every later candidate set")
}
