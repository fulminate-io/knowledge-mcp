// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// residencyMeasureEnv names a LEAF directory of REAL stored bm25v2 .seg blobs to
// import, e.g. <store>/segments/storage-<hash>/bm25v2/<graph>/<name>. The L2 cache
// nests its blobs four levels below segments/, so naming the root reports that the
// directory holds no .seg files; and the instrument imports BM25 blobs only, so an
// hnswv3 leaf fails with "nothing was imported" rather than measuring something
// else. The measurement is inert without it: a synthetic corpus measures the
// fixture's id lengths and document density rather than a deployment's, which is
// what the in-repo calibration row below is for.
const residencyMeasureEnv = "KNOWLEDGE_RESIDENCY_MEASURE_CORPUS"

// residencyModelTolerance is the ticket's requirement: the modeled resident heap
// must land within 10% of the heap a process measurably holds for those segments.
const residencyModelTolerance = 0.10

// TestResidencyModelTracksMeasuredHeap is requirement 2's measurement, and the one
// that decides whether ResidentHeapBytes is a budget an operator can size against.
//
// WHAT IT COMPARES, stated because the two sides are not the same kind of quantity.
// The MODEL is ResidentHeapBytes — a documented formula. The MEASUREMENT is the
// process's own live-heap (HeapAlloc) delta across importing the corpus, taken after two forced
// GCs so it reads retained heap rather than work in flight. The comparison is
// therefore model-against-measurement for ONE specific resident set, which is
// exactly the claim the budget makes.
//
// IT MUST BE RUN ON BOTH CORPUS SHAPES, and the reason is a term one of them
// cannot see: a merge-disabled CODE corpus carries no supersession records at all,
// so a model fitted on one alone read 42% under on a rebuilt KNOWLEDGE corpus,
// where a minority of blobs carry hundreds of recorded ids each. Measured after the
// record term landed: -3.4% on 9,277 code segments, +0.3% on 3,802 code segments,
// +5.4% on 1,232 knowledge/default segments carrying 128 records.
//
// EVERY SEGMENT IS MAPPING-BACKED HERE, deliberately: the blobs are mmapped and
// imported with their release, so there is no retained encoder output in play and
// what is being measured is the BOOKKEEPING — the membership index, the liveness
// bitsets and the flat route. That is the half the blob term cannot cover, and the
// half that was ~40% under-modeled before the route term existed and before the
// per-member constant was measured rather than estimated.
//
// WHAT THE MODEL DELIBERATELY LEAVES OUT, named here because an unexplained
// residual would be a defect rather than a rounding: the segmentEntry struct
// itself, the liveDocs header in front of its words, the format's per-segment
// statistics node and the snapshot's entry slot — together about 180 bytes per
// resident segment — plus the allocator's size-class rounding on the copied member
// ids. On the reference corpus (9,277 segments, 156,723 members, 130,708 distinct
// ids) that residual is the whole of the gap the assertion below tolerates:
// measured 41.7 MB against 37.8 MB modeled, -9.3%. Modeling those terms would add
// per-type constants that move with Go's struct layout to buy a few percent on a
// number whose consumer is an eviction threshold.
//
// IT IS SKIPPED BY NAME rather than passing vacuously when the corpus is absent, so
// a run that measured nothing cannot be read as a run that measured agreement.
func TestResidencyModelTracksMeasuredHeap(t *testing.T) {
	dir := os.Getenv(residencyMeasureEnv)
	if dir == "" {
		t.Skipf("%s must name a LEAF directory of stored bm25v2 .seg blobs "+
			"(<store>/segments/storage-<hash>/bm25v2/<graph>/<name>) to run the residency-model measurement",
			residencyMeasureEnv)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)
	require.NotEmpty(t, paths, "the corpus directory holds no .seg blobs")

	advice, err := adviceForFormat(bm25.New().Name())
	require.NoError(t, err)

	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)

	baseline := liveHeapAfterGC()

	var mappedBytes int64
	batch := make([]searchengine.SegmentBlob, 0, 250)
	imported, skipped := 0, 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		require.NoError(t, engine.Import(batch, nil))
		imported += len(batch)
		batch = batch[:0]
	}
	for _, p := range paths {
		m, err := mapBlobFile(p, advice)
		if err != nil {
			skipped++
			continue
		}
		envelope, payload, err := searchengine.SplitStoredBlob(m.data)
		if err != nil {
			skipped++
			_ = m.release()
			continue
		}
		sum := sha256.Sum256(payload)
		id := hex.EncodeToString(sum[:])
		// A corrupt blob is skipped rather than imported: Import would fail the
		// whole batch, and a corpus copy legitimately carries one.
		if verr := bm25.ValidateSegment(id, payload); verr != nil {
			skipped++
			_ = m.release()
			continue
		}
		mappedBytes += int64(len(m.data))
		// Release non-nil is what marks this payload page-cache-backed, which is the
		// provenance this arm is measuring.
		batch = append(batch, searchengine.SegmentBlob{
			ID: id, Bytes: payload, Envelope: envelope,
			Release: func(mb *mappedBlob) func() { return func() { _ = mb.release() } }(m),
		})
		if len(batch) == cap(batch) {
			flush()
		}
	}
	flush()
	require.Positive(t, imported, "nothing was imported, so there is nothing to measure")

	measured := liveHeapAfterGC() - baseline
	modeled := engine.ResidentHeapBytes()
	segments := engine.ResidentSegmentCount()
	docs := engine.ResidentDocCount()

	require.Empty(t, engine.HeapBackedResidentIDs(),
		"control: every imported payload must be mapping-backed, or this measures the blob term instead of the bookkeeping")
	require.Positive(t, measured, "control: importing the corpus must have moved the heap")

	deviation := float64(modeled-measured) / float64(measured)
	t.Logf("residency model vs measured heap: segments=%d docs=%d mapped=%.1fMB "+
		"measured=%.1fMB modeled=%.1fMB per_segment_measured=%.2fKB per_segment_modeled=%.2fKB deviation=%+.1f%%",
		segments, docs, float64(mappedBytes)/1e6,
		float64(measured)/1e6, float64(modeled)/1e6,
		float64(measured)/float64(segments)/1024, float64(modeled)/float64(segments)/1024,
		deviation*100)

	require.LessOrEqual(t, absFloat(deviation), residencyModelTolerance,
		"the residency model must land within %.0f%% of the heap the process measurably holds for these segments",
		residencyModelTolerance*100)
}

// liveHeapAfterGC reads HeapAlloc after two forced collections. Two, not one: the
// first can leave objects that only became unreachable during it.
//
// HeapAlloc, NOT HeapInuse, and the difference is the difference between measuring
// the program and measuring the allocator. HeapInuse counts whole spans the heap
// holds, so it carries per-size-class fragmentation the model cannot predict and
// has no business predicting — 3 MB of the 46 MB on the reference corpus. HeapAlloc
// is live object bytes, which is what a model of "what these segments hold" claims.
func liveHeapAfterGC() int64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// syntheticSegments is how many segments the in-repo calibration fixture builds.
// Large enough that the per-segment terms dominate the engine's own fixed cost,
// small enough to build and map in a second.
const syntheticSegments = 400

// syntheticDocsPerSegment mirrors the density the reference corpus has (156,723
// members over 9,277 segments, ~17), because the model's two biggest terms are
// per-MEMBER and per-DISTINCT-ID: a fixture of one document per segment would
// measure almost none of what the constants govern.
const syntheticDocsPerSegment = 17

// syntheticRecordIDs is how many ids a record-carrying blob names. The measured
// knowledge corpus runs up to 1,932 on one blob; this is enough that the term is a
// visible share of the fixture's heap and cheap enough to build 100 times.
const syntheticRecordIDs = 40

// TestResidencyModelHoldsOnASyntheticCorpus is the CALIBRATION row, and it runs
// with no operator input at all.
//
// WHY IT EXISTS BESIDE THE CORPUS MEASUREMENT. memberEntryOverheadBytes and
// routeEntryOverheadBytes are measurements of the Go runtime's map layout, not of
// anything in this package, so a toolchain bump can move them under a model that
// still compiles and still passes every behavioral test. The corpus-scale row is
// the INSTRUMENT — it needs a real corpus and skips by name without one, which is
// how every CI run takes it — so on its own a runtime change would shift the
// budget's calibration silently. This row builds its own corpus from the bm25
// format, maps it, and holds the same model against the same measurement, so the
// class is observed on every ordinary `go test`.
//
// ITS TOLERANCE IS WIDER THAN THE CORPUS ROW'S, and the reason is the fixture
// rather than the model: at 400 segments the engine's own fixed cost and the
// allocator's size-class rounding are a visible share of two megabytes, where at
// 9,277 they are noise. It is not wide enough to hide a term, which is the point:
// the fixture measured +2.7%, +3.6% and +4.1% over three runs, and dropping the
// supersession-record term alone moves it past this bound.
func TestResidencyModelHoldsOnASyntheticCorpus(t *testing.T) {
	const tolerance = 0.12

	dir := t.TempDir()
	advice, err := adviceForFormat(bm25.New().Name())
	require.NoError(t, err)
	recordEnvelope := supersessionEnvelopeFixture(t, syntheticRecordIDs)

	// BUILD AND WRITE FIRST, THEN DROP EVERYTHING. The documents, the built
	// segments and their encoded bytes are the fixture's own garbage; measuring
	// with them still reachable would attribute them to the engine.
	paths := make([]string, 0, syntheticSegments)
	for seg := range syntheticSegments {
		docs := make([]searchengine.Document, 0, syntheticDocsPerSegment)
		for d := range syntheticDocsPerSegment {
			id := "pkg/dir/file" + strconv.Itoa(seg) + ".go:Type.Method" + strconv.Itoa(d)
			docs = append(docs, searchengine.Document{
				ID: id,
				Fields: map[string]string{
					searchengine.FieldContent:    strings.Repeat("alpha beta gamma delta ", 12),
					searchengine.FieldSymbolName: id,
				},
			})
		}
		built, _, berr := bm25.New().Build(docs)
		require.NoError(t, berr)
		blob, eerr := built.Encode()
		require.NoError(t, eerr)
		// EVERY FOURTH BLOB CARRIES A SUPERSESSION RECORD, because the model's record
		// term is invisible on a corpus that has none: a merge-disabled code corpus
		// carries zero records and a rebuilt knowledge corpus carries hundreds of ids
		// on a minority of blobs, which is the shape that read 42% under before the
		// term existed. The proportion and the per-record id count are the measured
		// corpus's, rounded: 128 of 1,232 blobs, up to 1,932 ids each.
		var envelope []byte
		if seg%4 == 0 {
			envelope = recordEnvelope
		}
		path := filepath.Join(dir, strconv.Itoa(seg)+".seg")
		require.NoError(t, os.WriteFile(path, append(envelope, blob...), 0o600))
		paths = append(paths, path)
	}

	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)

	baseline := liveHeapAfterGC()

	blobs := make([]searchengine.SegmentBlob, 0, len(paths))
	withRecord := 0
	for _, path := range paths {
		m, merr := mapBlobFile(path, advice)
		require.NoError(t, merr)
		envelope, payload, serr := searchengine.SplitStoredBlob(m.data)
		require.NoError(t, serr)
		if len(envelope) > 0 {
			withRecord++
		}
		sum := sha256.Sum256(payload)
		// Release non-nil is what marks the payload page-cache-backed, which is the
		// provenance this row measures: the bookkeeping, with no retained encoder
		// output in play.
		blobs = append(blobs, searchengine.SegmentBlob{
			ID: hex.EncodeToString(sum[:]), Bytes: payload, Envelope: envelope,
			Release: func(mb *mappedBlob) func() { return func() { _ = mb.release() } }(m),
		})
	}
	require.Positive(t, withRecord,
		"fixture control: the corpus must carry supersession records, or the record term is unmeasured here")
	// The blobs slice is dead from here, which Go's own liveness analysis already
	// acts on — an explicit nil assignment would be an ineffectual one.
	require.NoError(t, engine.Import(blobs, nil))

	measured := liveHeapAfterGC() - baseline
	modeled := engine.ResidentHeapBytes()
	segments := engine.ResidentSegmentCount()
	require.Equal(t, syntheticSegments, segments, "fixture control: every synthetic segment must be resident")
	require.Empty(t, engine.HeapBackedResidentIDs(),
		"control: every imported payload must be mapping-backed, or this measures the blob term instead")
	require.Positive(t, measured, "control: importing the fixture must have moved the heap")

	deviation := float64(modeled-measured) / float64(measured)
	t.Logf("synthetic calibration: segments=%d docs=%d measured=%.2fMB modeled=%.2fMB deviation=%+.1f%%",
		segments, engine.ResidentDocCount(), float64(measured)/1e6, float64(modeled)/1e6, deviation*100)
	require.LessOrEqual(t, absFloat(deviation), tolerance,
		"the residency model's per-member and per-route constants are measurements of the Go runtime's map layout; "+
			"this row is what reddens when a toolchain bump moves them (re-measure with %s)", residencyMeasureEnv)
}

// supersessionEnvelopeFixture returns a REAL supersession envelope naming ids
// superseded ids, produced by a real consolidation rather than hand-assembled.
//
// THE ENVELOPE IS BUILT BY THE ENGINE, not written by this test: a hand-rolled
// mirror of the stored format would drift from it silently, and this fixture's
// whole purpose is to put the bytes a deployment's blobs carry in front of the
// decoder. A single envelope is reused across the record-carrying blobs, which is
// sound for a MEMORY measurement and stated rather than assumed: readIDs allocates
// its own copy of every id on every decode, so what the resident entries cost is
// decided by the id count and their lengths, not by whether two blobs name the
// same ids.
func supersessionEnvelopeFixture(t *testing.T, ids int) []byte {
	t.Helper()
	e := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	defer e.Close()

	for i := range ids {
		require.NoError(t, e.Add([]searchengine.Document{{
			ID:     "constituent-" + strconv.Itoa(i),
			Fields: map[string]string{searchengine.FieldContent: "alpha " + strconv.Itoa(i)},
		}}))
	}
	require.NoError(t, e.Flush())
	resident := e.Export()
	require.Len(t, resident, ids, "fixture control: one segment per constituent")

	constituents := make([]searchengine.SegmentID, 0, len(resident))
	for _, b := range resident {
		constituents = append(constituents, b.ID)
	}
	published, err := e.ReplaceBucket(0, 1, constituents, nil, []searchengine.Document{{
		ID:     "consolidated",
		Fields: map[string]string{searchengine.FieldContent: "alpha consolidated"},
	}})
	require.NoError(t, err)
	require.NotEmpty(t, published, "the consolidation must have published a segment")

	for _, b := range e.Export() {
		if b.ID == published {
			require.NotEmpty(t, b.Envelope, "the consolidated blob must carry its supersession record")
			superseded, _, serr := searchengine.SupersededBy(b.Envelope)
			require.NoError(t, serr)
			require.Len(t, superseded, ids, "the record must name every constituent the swap replaced")
			return b.Envelope
		}
	}
	t.Fatal("the consolidated segment is not resident")
	return nil
}
