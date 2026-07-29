// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// collapsedBM25Shipped is the BM25 shipped doc count the collapsed-arm fixture
// ships: comfortably above residentBackstopFloor so the denominator is trustworthy
// and the ratio is armed.
const collapsedBM25Shipped = 128

// shipBM25Metas is the BM25 sibling of shipHNSWMetas: it ships BM25-format blobs
// carrying the given doc counts to target via the view so the server's List surfaces
// them as the BM25 arm's shipped denominator. It returns the shipped ids so a caller
// can hand one to view.setDrop.
//
// The bytes are a placeholder and are NOT decodable as a BM25 segment, so a fixture
// that wants a degenerate-but-ERROR-FREE BM25 arm must pair this with setDrop on the
// returned id: the Fetch then omits the blob entirely (a short-but-OK Fetch, which
// load treats as an error-free skip plus a load-floor clamp) rather than handing the
// engine bytes that fail to decode. setDrop holds exactly ONE id, so such a fixture
// must ship exactly one BM25 blob.
func shipBM25Metas(t *testing.T, view *fakeSegmentSource, target *knowledgev1.GraphSelector, docCounts ...int) []string {
	t.Helper()
	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(docCounts))
	ids := make([]string, 0, len(docCounts))
	for i, dc := range docCounts {
		id := target.GetRepo() + "-b" + string(rune('A'+i))
		ids = append(ids, id)
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{
			Id: id, Format: bm25.New().Name(),
			DocCount: int32(dc), Bytes: []byte("seg"),
		})
	}
	view.server.ship(target, "", blobs)
	return ids
}

// verdictFor picks one format's arm verdict out of a probe result, failing the test
// when that arm is missing rather than returning a zero value that would silently
// satisfy a false-y assertion.
func verdictFor(t *testing.T, verdicts []ArmVerdict, format string) ArmVerdict {
	t.Helper()
	for _, v := range verdicts {
		if v.Format == format {
			return v
		}
	}
	require.FailNowf(t, "missing arm verdict", "no verdict for format %q", format)
	return ArmVerdict{}
}

// newCollapsedBM25Fixture builds the shape the per-format verdict exists to see: a
// real, Fetch-able HNSW corpus (so the HNSW arm loads healthy) alongside a BM25
// manifest whose only blob every Fetch omits (so the BM25 arm's read pool stays
// empty against a shipped denominator above the floor). It returns a cold consumer
// Manager over the same server.
func newCollapsedBM25Fixture(t *testing.T, repo string) *Manager {
	t.Helper()
	ctx := context.Background()
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: repo}

	// Ship a real HNSW corpus (1024 docs == one sealed segment) via a producer
	// Manager pointed at the same server, so the HNSW arm has something to load.
	producer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	require.NoError(t, producer.AddAndShip(ctx, kgtypes.GraphCode, repo, hnswVecDocs(1024)))

	// Ship the BM25 manifest AFTER the producer's publish — a publish refcount-GCs
	// blobs no manifest references — then drop its single blob from every Fetch so
	// the BM25 arm imports nothing without erroring.
	bm25IDs := shipBM25Metas(t, gc, target, collapsedBM25Shipped)
	gc.setDrop(bm25IDs[0], true)

	return NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
}

// TestPerFormatVerdict_BM25ArmFlagged pins the per-format verdict: one format's arm
// can be collapsed while the other is healthy, and the overall verdict must see it.
// A single-format probe reports this graph healthy while half its search surface is
// empty.
func TestPerFormatVerdict_BM25ArmFlagged(t *testing.T) {
	ctx := context.Background()
	consumer := newCollapsedBM25Fixture(t, "perFormatRepo")

	verdicts, err := consumer.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "perFormatRepo")
	require.NoError(t, err)
	require.Len(t, verdicts, 2, "one verdict per format arm")

	h := verdictFor(t, verdicts, hnsw.New().Name())
	require.NoError(t, h.Err)
	require.False(t, h.Degenerate, "the HNSW arm loaded its full corpus and is healthy")
	require.GreaterOrEqual(t, h.ResidentAfterLoad, residentBackstopFloor,
		"the healthy arm cleared the floor on the cache-first load alone")

	b := verdictFor(t, verdicts, bm25.New().Name())
	// Err==nil is asserted EXPLICITLY: it pins the fixture's realizability. Swapping
	// setDrop for a real blob would make the arm fail to decode, and an errored arm
	// reports Degenerate false — which would fail identically before and after the
	// per-format verdict exists, destroying the discrimination.
	require.NoError(t, b.Err, "the collapsed arm must be produced without an arm error")
	require.True(t, b.Degenerate, "shipped corpus above the floor, empty read pool → degenerate")
	require.Equal(t, collapsedBM25Shipped, b.Shipped, "the BM25 arm reads its OWN format's denominator")
	require.False(t, b.Disarm)
	require.Equal(t, 0, b.ResidentAfterRecover, "the re-import could not raise the read pool")

	degenerate, err := consumer.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "perFormatRepo")
	require.NoError(t, err)
	require.True(t, degenerate,
		"a collapsed BM25 arm behind a healthy HNSW arm must flip the overall verdict")
}

// TestPerFormatVerdict_ArmDisarms pins that each arm disarms on its OWN denominator,
// mirroring the read-side backstop's disarms, and that a disarmed arm never flips the
// overall verdict.
func TestPerFormatVerdict_ArmDisarms(t *testing.T) {
	ctx := context.Background()
	hnswName, bm25Name := hnsw.New().Name(), bm25.New().Name()

	t.Run("pre-doc_count blob disarms (DocCount==0)", func(t *testing.T) {
		gc := newEmptyFetchHarness(t)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unknownBM25Repo"}
		// A shipped BM25 meta with DocCount==0 (an old pre-doc_count blob) makes the
		// denominator untrustworthy → disarm rather than churn.
		shipBM25Metas(t, gc, target, 0)

		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		verdicts, err := mgr.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "unknownBM25Repo")
		require.NoError(t, err)
		require.Len(t, verdicts, 2)

		b := verdictFor(t, verdicts, bm25Name)
		require.NoError(t, b.Err)
		require.True(t, b.Disarm, "a pre-doc_count blob disarms the ratio (conservative-unknown)")
		require.False(t, b.Degenerate)

		require.False(t, verdictFor(t, verdicts, hnswName).Degenerate,
			"the arm with no shipped corpus of its own is not degenerate either")

		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "unknownBM25Repo")
		require.NoError(t, err)
		require.False(t, degenerate, "a disarmed arm never flips the overall verdict")
	})

	t.Run("sub-floor corpus disarms", func(t *testing.T) {
		gc := newEmptyFetchHarness(t)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "tinyBM25Repo"}
		// A BM25 corpus summing to 4 docs (< floor 64) is too small for the ratio to
		// mean anything → disarm.
		shipBM25Metas(t, gc, target, 2, 2)

		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		verdicts, err := mgr.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "tinyBM25Repo")
		require.NoError(t, err)
		require.Len(t, verdicts, 2)

		b := verdictFor(t, verdicts, bm25Name)
		require.NoError(t, b.Err)
		require.Equal(t, 4, b.Shipped, "the arm summed its own format's metas")
		require.True(t, b.Disarm, "a sub-floor shipped corpus disarms the ratio")
		require.False(t, b.Degenerate)

		require.False(t, verdictFor(t, verdicts, hnswName).Degenerate)

		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "tinyBM25Repo")
		require.NoError(t, err)
		require.False(t, degenerate, "a disarmed arm never flips the overall verdict")
	})
}

// TestPerFormatVerdict_ArmErrorIsolated pins per-arm error isolation: one arm's probe
// failure must not destroy the other arm's verdict. Each format's L2 cache is rooted
// separately, so one arm can be cold in exactly the processes where the other is
// warm; propagating the cold arm's error would take out the warm arm's documented
// server-independent L2-first verdict.
func TestPerFormatVerdict_ArmErrorIsolated(t *testing.T) {
	ctx := context.Background()
	hnswName, bm25Name := hnsw.New().Name(), bm25.New().Name()

	t.Run("one arm errors, the other stays usable", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		producer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		require.NoError(t, producer.AddAndShip(ctx, kgtypes.GraphCode, "isolatedRepo", hnswVecDocs(1024)))

		// Warm the HNSW arm FIRST so its resident set is already imported. Only then
		// break the List, so the HNSW arm still answers server-independently while the
		// cold BM25 arm cannot read at all — that asymmetry is the contract.
		consumer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		require.NoError(t, consumer.managerFor(kgtypes.GraphCode, "isolatedRepo").load(ctx))
		gc.listErr = errors.New("segment list unavailable")

		verdicts, err := consumer.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "isolatedRepo")
		require.NoError(t, err, "a top-level error is returned only when EVERY arm errored")
		require.Len(t, verdicts, 2)

		b := verdictFor(t, verdicts, bm25Name)
		require.Error(t, b.Err, "the unreadable arm records its own error")
		require.False(t, b.Degenerate, "an arm that could not be measured never drives a rebuild")

		h := verdictFor(t, verdicts, hnswName)
		require.NoError(t, h.Err, "the warm arm is unaffected by the other arm's failure")
		require.GreaterOrEqual(t, h.ResidentAfterLoad, residentBackstopFloor,
			"the warm arm's resident set survived and answered without the server")
		require.False(t, h.Degenerate)
	})

	t.Run("every arm failing returns a top-level error", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		gc.listErr = errors.New("segment list unavailable")

		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		verdicts, err := mgr.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "allFailRepo")
		require.Error(t, err, "nothing was measurable, so the failure surfaces")
		require.Len(t, verdicts, 2)
		for _, v := range verdicts {
			require.Error(t, v.Err, "every arm recorded its own error: %s", v.Format)
			require.False(t, v.Degenerate)
		}

		degenerate, err := mgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "allFailRepo")
		require.Error(t, err)
		require.False(t, degenerate)
	})
}

// debugsContaining returns the recorded DEBUG records whose message contains substr,
// so a test can inspect a specific diagnostic's attributes. The WARN sibling
// (warnsContaining) renders to strings because it asserts on message text; this one
// returns the records because the per-format probe assertions read individual attrs.
func (h *capturingSlogHandler) debugsContaining(substr string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelDebug && strings.Contains(r.Message, substr) {
			out = append(out, r)
		}
	}
	return out
}

// recordAttrs collects a record's attributes into a keyed map so a test can assert
// both that a key is PRESENT and, where the value is meaningful, what it holds.
func recordAttrs(r slog.Record) map[string]slog.Value {
	attrs := make(map[string]slog.Value, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	return attrs
}

// TestPerFormatVerdict_DebugFieldsPerFormat pins the diagnostic's ATTRIBUTION, not
// merely its line count: the probe emits one record per arm, each tagged with its own
// format and carrying that arm's inputs and verdict, so a collapsed arm is
// identifiable in the log without re-deriving the decision.
func TestPerFormatVerdict_DebugFieldsPerFormat(t *testing.T) {
	ctx := context.Background()
	// The same collapsed-BM25/healthy-HNSW shape: the setDrop construction is what
	// keeps the BM25 arm error-free, so its record carries degenerate=true rather
	// than the degenerate=false an errored arm would report.
	consumer := newCollapsedBM25Fixture(t, "debugFieldsRepo")

	// Install the capturing handler AFTER the fixture is built so only the probe's
	// own records are captured.
	h := installCapturingSlog(t)
	_, err := consumer.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "debugFieldsRepo")
	require.NoError(t, err)

	records := h.debugsContaining("resident degeneracy reconcile probe")
	require.Len(t, records, 2, "one probe record per format arm")

	byFormat := make(map[string]map[string]slog.Value, 2)
	for _, r := range records {
		attrs := recordAttrs(r)
		format, ok := attrs["format"]
		require.True(t, ok, "every probe record carries its format")
		byFormat[format.String()] = attrs
	}
	require.Len(t, byFormat, 2, "the two records carry DISTINCT formats")
	require.Contains(t, byFormat, hnsw.New().Name())
	require.Contains(t, byFormat, bm25.New().Name())

	// Keys asserted by PRESENCE on both records; values only where they are defined.
	for format, attrs := range byFormat {
		for _, key := range []string{"resident_after_recover", "shipped", "degenerate"} {
			require.Contains(t, attrs, key, "record for %q must carry %q", format, key)
		}
	}

	b := byFormat[bm25.New().Name()]
	require.Equal(t, int64(collapsedBM25Shipped), b["shipped"].Int64())
	require.True(t, b["degenerate"].Bool(), "the collapsed arm is attributed as degenerate")

	// No shipped VALUE is asserted for the healthy arm: it stops at the entry floor
	// gate without ever computing a denominator, so its shipped is a short-circuit
	// artifact rather than a measurement.
	require.False(t, byFormat[hnsw.New().Name()]["degenerate"].Bool(),
		"the healthy arm is attributed as non-degenerate")
}

// TestPerFormatVerdict_RecoveredAboveFloorBelowRatio pins the band the floor
// re-application protects: a recovery that lands AT OR ABOVE the floor but BELOW the
// ratio is HEALTHY. Reading the ratio without re-applying the floor first would flag
// such a partial re-import as degenerate and drive a rebuild that cannot raise the
// read pool it is measured against.
func TestPerFormatVerdict_RecoveredAboveFloorBelowRatio(t *testing.T) {
	ctx := context.Background()
	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "bandRepo"}

	// TWO on-server HNSW blobs built by SEPARATE buildHNSWSegment calls, each with its
	// own engine so no background merge collapses them into one — a merge would make
	// the drop below starve the WHOLE corpus instead of just the large half.
	small := buildHNSWSegment(t, vecContentDocsSeed(100, 0))
	large := buildHNSWSegment(t, vecContentDocsSeed(1024, 1000))
	require.Len(t, small, 1, "the small corpus must seal as exactly one blob")
	require.Len(t, large, 1, "the large corpus must seal as exactly one blob so one drop omits all of it")

	shipper := svc.viewFor(target, "")
	_, err := shipper.Ship(ctx, append(append([]*knowledgev1.SegmentBlobProto{}, small...), large...))
	require.NoError(t, err)

	// The recovery can import only the small blob, so the resident pool lands in the
	// band: above the floor, below the ratio against the full shipped denominator.
	gc.setDrop(large[0].GetId(), true)

	// Stage the consumer into the partial-L2 state: a locally-built sub-floor segment
	// is already imported and the load once-guard is latched, so load() short-circuits
	// and the post-load resident is BELOW the floor — which is what forces execution
	// past the entry gate into the verdict.
	consumer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	dm := consumer.managerFor(kgtypes.GraphCode, "bandRepo")
	partial := buildHNSWSegment(t, vecContentDocsSeed(10, 5000))
	partialBlobs := make([]searchengine.SegmentBlob, 0, len(partial))
	for _, b := range partial {
		partialBlobs = append(partialBlobs, blobFromProto(b))
	}
	require.NoError(t, dm.engine.Import(partialBlobs, nil))
	dm.recordResident(partialBlobs)
	dm.l2Loaded.Store(true)
	dm.importedGen.Store(999)
	require.Less(t, dm.engine.ResidentDocCount(), residentBackstopFloor,
		"the consumer is below floor after the partial import")

	verdicts, err := consumer.ReconcileResidentDegenerateByFormat(ctx, kgtypes.GraphCode, "bandRepo")
	require.NoError(t, err)
	h := verdictFor(t, verdicts, hnsw.New().Name())
	require.NoError(t, h.Err)

	// ASSERT THE BAND ITSELF FIRST, so a fixture that drifts out of it fails loudly
	// instead of passing vacuously.
	require.GreaterOrEqual(t, h.ResidentAfterRecover, residentBackstopFloor,
		"the recovery must clear the floor for this fixture to exercise the band")
	require.Less(t, float64(h.ResidentAfterRecover), residentBackstopRatio*float64(h.Shipped),
		"the recovery must stay BELOW the ratio for this fixture to exercise the band")

	// THEN the verdict: floor-first, so this is healthy.
	require.False(t, h.Degenerate,
		"a recovery at or above the floor is healthy even when it is below the ratio")
}
