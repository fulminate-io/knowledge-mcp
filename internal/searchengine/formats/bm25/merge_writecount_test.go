// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// countingSink is a MergeSink that counts the WRITE CALLS a merge issues and
// forwards them to a real file.
//
// IT COUNTS CALLS, NOT BYTES WRITTEN, because the call count is the quantity
// that became the problem: against an *os.File each WriteAt is one pwrite(2)
// syscall, and the merge writer issued one per field row and one per append.
// Profiled on the live daemon, the two lines that issued them were 72-76% of all
// daemon CPU during merges — 2.68 cores sustained over a 12.2-minute capture.
// Bytes written did not change and were never the problem.
//
// IT FORWARDS TO A REAL FILE rather than standing in for one. A sink that
// swallowed the bytes would let this test pass over a merge that produced
// nothing readable, and the decodability leg below would have nothing to check.
type countingSink struct {
	f      *os.File
	writes int
	bytes  int64
}

func (c *countingSink) WriteAt(p []byte, off int64) (int, error) {
	c.writes++
	c.bytes += int64(len(p))
	return c.f.WriteAt(p, off)
}

func (c *countingSink) ReadAt(p []byte, off int64) (int, error) { return c.f.ReadAt(p, off) }

// TestMergeToWritesTheSegmentWithoutBufferingIt is the writer-side property gate
// for MergeTo: it fills a caller-owned file with a real, decodable, searchable
// segment, allocates nothing sized by that segment, and leaves the file alone
// when it fails.
//
// THE FOURTH LEG IS THE ONE THAT IS NOT A REPEAT OF A SIBLING. Ownership of the
// merge file moved from the format to the engine, and that contract is only
// observable on the error path: a format that still cleaned up after itself would
// look correct here and would hide an engine that forgot to unlink. So the error
// leg asserts the file SURVIVES rather than that it is gone.
//
// WHAT THAT LEG CAN AND CANNOT CATCH, said plainly rather than left to be
// assumed: MergeTo receives a MergeSink, which carries no name and no Remove, so
// today it CANNOT unlink anything and the assertion cannot fail against the
// current implementation. What it pins is the next version — an implementation
// that type-asserted dst to *os.File to reach Name() and clean up after itself,
// which is the shape someone reaches for when a failed merge leaves a file
// behind. It is a guard on a boundary, not a reproduction of a live defect.
func TestMergeToWritesTheSegmentWithoutBufferingIt(t *testing.T) {
	require.Equal(t, int64(math.MaxUint32), v2MaxBlobBytes,
		"the shipped ceiling must be the full u32 range; this test lowers it and must not mask a lowered default")

	// THE FIXTURE IS THE HEAVY CORPUS, and it has to be. The writer holds a fixed
	// set of coalescing windows — 64 KiB in total, allocated whether or not they
	// fill — so an under-half bound is unsatisfiable against a segment that is
	// itself only a few tens of KiB, no matter how perfectly the writer streams.
	// Measured: manyTermDocs(200) over two inputs produces a 60,765-byte segment
	// and the bound demands under 30,382, which the windows alone exceed. This is
	// the same fixture, and the same reasoning, as the writer bound in
	// merge_stream_test.go, whose own comment records that merge state is
	// per-DOCUMENT while the bound is against the OUTPUT SIZE.
	docs := mergeHeavyDocs(600, 40)
	ins := mergeInputs(t, docs, 6)
	accept := dropEverySeventh(ins)
	segs := make([]searchengine.Segment[Query, *CorpusStats], len(ins))
	for i, in := range ins {
		segs[i] = in
	}

	// The query comes OUT OF THE FIXTURE rather than from the shared
	// equalityQueries table, which is written against manyTermDocs' vocabulary and
	// would match nothing here — a search returning no hits would satisfy nothing
	// while looking like a check. docs[1] survives the filter (ordinal 1 is not a
	// multiple of 7), so a term from its summary is in the merged segment.
	surviving := strings.Fields(docs[1].Fields[searchengine.FieldSummary])
	require.NotEmpty(t, surviving, "the fixture produced a document with no summary terms")

	// THE EXPECTED SURVIVORS COME FROM THE FIXTURE, not from the merge. Deriving
	// them through resolveMergeLayout would ask the code under test for its own
	// answer key; reading them off the documents the fixture was built from is an
	// expectation the merge cannot influence.
	var wantIDs []searchengine.ExternalID
	for _, d := range docs {
		if docOrdinal(d.ID)%7 != 0 {
			wantIDs = append(wantIDs, d.ID)
		}
	}
	require.NotEmpty(t, wantIDs, "the filter kept nothing, so every assertion below is vacuous")
	require.Less(t, len(wantIDs), len(docs), "the filter dropped nothing, so the liveness arm is untested")

	path := filepath.Join(t.TempDir(), "merged.seg")
	f, err := os.Create(path) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := Format{}.MergeTo(f, segs, accept)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	allocated := int64(after.TotalAlloc - before.TotalAlloc)

	// (a) n is authoritative: after the CALLER truncates to it, the file is n bytes.
	require.NoError(t, f.Truncate(n))
	info, err := f.Stat()
	require.NoError(t, err)
	require.Equal(t, n, info.Size(), "the reported length must be the segment's length")

	// (b) The bytes are a real segment, not merely the right size.
	blob, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	seg, err := Format{}.Decode(blob)
	require.NoError(t, err)
	require.Equal(t, wantIDs, seg.IDs(), "the merged segment's members must be exactly the surviving documents")
	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})
	require.NotEmpty(t, seg.Search(NewQuery(surviving[0]), stats, 10, nil),
		"the merged segment must be searchable, not just decodable")

	// (c) Nothing output-sized was allocated writing it.
	t.Logf("MergeTo allocated %d bytes writing a %d-byte segment", allocated, n)
	require.Less(t, allocated, n/2,
		"MergeTo allocated %d bytes for a %d-byte output — that is a buffered merge, not a streamed one",
		allocated, n)

	// (d) THE ERROR LEG, driven through the real ceiling guard rather than a fake
	// filesystem, so the failure happens AFTER the destination exists — the only
	// window in which ownership is observable.
	errPath := filepath.Join(t.TempDir(), "failed.seg")
	ef, err := os.Create(errPath) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { _ = ef.Close() })

	original := v2MaxBlobBytes
	t.Cleanup(func() { v2MaxBlobBytes = original })
	v2MaxBlobBytes = n - 1

	failedN, err := Format{}.MergeTo(ef, segs, accept)
	require.Error(t, err, "the lowered ceiling must make this merge fail")
	require.Zero(t, failedN)
	require.FileExists(t, errPath,
		"MergeTo unlinked the caller's file on the error path — the engine owns that file, and a format that cleans up here hides an engine that forgot to")
}

// mergeWriteCountFixture builds a representative merge and reports both the
// write calls it issued and the number of (term, field) groups it emitted.
//
// THE GROUP COUNT IS THE EXTERNAL EXPECTATION, and it has to be, because a
// write-count bound compared against a number the writer itself produced would
// be an identity check. It is derived by walking the SAME inputs through the
// SAME mergeWalk the emitter walks, counting the groups the emitter will visit —
// so it is a property of the fixture, not of the buffering under test.
//
// WHY GROUPS IS THE RIGHT DENOMINATOR: per (term, field) group the emitter
// issues six stores — the posting run, the term bytes, and four dictionary-row
// patches — so an unbuffered writer's call count is at least the group count,
// and in practice several times it.
func mergeWriteCountFixture(t *testing.T) (writes, groups int, size int64) {
	t.Helper()
	docs := manyTermDocs(400)
	ins := mergeInputs(t, docs, 4)
	accept := dropEverySeventh(ins)

	_, remap := resolveMergeLayout(ins, accept)
	mergeWalk(ins, remap,
		func(string, int, []uint32, []uint16) { groups++ },
		func(string, int64) {})

	f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	sink := &countingSink{f: f}
	segs := make([]searchengine.Segment[Query, *CorpusStats], len(ins))
	for i, in := range ins {
		segs[i] = in
	}
	n, err := Format{}.MergeTo(sink, segs, accept)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(n))

	return sink.writes, groups, n
}

// TestMergeToCoalescesWritesIntoFlushes is THE GATE on the merge writer's
// syscall count: writes must scale with FLUSHES, not with fields.
//
// It is a deterministic assertion in the ordinary suite rather than a benchmark,
// and that split is deliberate. A benchmark does not run under `go test`, so a
// regression guarded only by one is guarded by a gate nobody executes; this runs
// on every package run and goes red the moment a per-field write is
// reintroduced. The benchmark beside it reports the magnitudes.
//
// THE DENOMINATOR IS FIXTURE-DERIVED, NOT A PINNED CONSTANT. Comparing the write
// count against a number transcribed from a previous run of this same writer
// would be an identity check that ratifies whatever the writer currently does.
// The group count comes from walking the inputs, so it says what the emitter has
// to emit regardless of how it chooses to write it.
func TestMergeToCoalescesWritesIntoFlushes(t *testing.T) {
	writes, groups, size := mergeWriteCountFixture(t)
	t.Logf("merge issued %d write calls for %d (term,field) groups producing a %d-byte segment", writes, groups, size)

	// The fixture must be big enough for the comparison to mean something: a
	// merge with a handful of groups would satisfy any ratio.
	require.Greater(t, groups, 1000,
		"the fixture emitted only %d groups — too few for a write-count ratio to say anything", groups)
	require.Positive(t, size, "the merge produced no output, so its write count describes nothing")

	// O(FLUSHES), NOT O(FIELDS). An unbuffered writer issues at least one call
	// per group and in practice six; the coalescing writer issues one per filled
	// window. The 10x margin is what makes this a statement about the SHAPE of
	// the relationship rather than a pinned ratio that would need editing every
	// time the fixture moved.
	require.Less(t, writes*10, groups,
		"the merge issued %d write calls for %d groups — that is per-field writing, not per-flush writing",
		writes, groups)
}

// BenchmarkMergeToWriteSyscalls reports the magnitudes the gate above only
// bounds: write calls, bytes and time per merge.
//
// IT EXISTS BECAUSE THE GATE CANNOT REPORT A NUMBER, and the number is what the
// pre-change baseline was recorded as. Run it with:
//
//	go test ./cmd/knowledge/internal/searchengine/formats/bm25/ \
//	  -run '^$' -bench '^BenchmarkMergeToWriteSyscalls$' -benchtime 20x
//
// RECORDED BASELINE, both sides measured through this same benchmark, on the same
// machine, over the same fixture, at -benchtime 20x, AND AT THE SAME COMMIT — the
// pair is re-measured together whenever the base moves, because a before taken at
// one base and an after at another is a comparison of two things at once:
//
//	before (one write call per field row and per append):
//	  4540 writes/op   89268 bytes/op   4956331 ns/op
//	after (coalescing windows):
//	    19 writes/op   90504 bytes/op    953679 ns/op
//
// 239x fewer write calls and 5.2x less wall time per merge. The before side is
// produced by forcing every store straight through to the sink, which is
// precisely the writer this changeset replaced.
//
// THE WRITE AND BYTE COUNTS ARE DETERMINISTIC; ONLY ns/op MOVES between runs and
// between machines. A reader comparing against these figures should hold the
// first two to the digit and treat the third as an order of magnitude.
//
// BYTES/OP ROSE BY 1236, AND THAT IS NOT NOISE. The coalescing writer emits the
// alignment gaps between appends as explicit zeros so a window stays one
// contiguous run, where the per-field writer skipped them and left holes a sparse
// file reads back as zeros. The bytes on disk are identical either way — the
// golden hashes are unchanged across this change — but the byte COUNT the sink
// observes includes the padding.
func BenchmarkMergeToWriteSyscalls(b *testing.B) {
	docs := manyTermDocs(400)
	ins := mergeInputsB(b, docs, 4)
	accept := dropEverySeventh(ins)
	segs := make([]searchengine.Segment[Query, *CorpusStats], len(ins))
	for i, in := range ins {
		segs[i] = in
	}
	dir := b.TempDir()

	var writes int
	var bytesWritten int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := os.Create(filepath.Join(dir, "merged.seg")) //nolint:gosec // test-owned temp path
		if err != nil {
			b.Fatal(err)
		}
		sink := &countingSink{f: f}
		if _, err := (Format{}).MergeTo(sink, segs, accept); err != nil {
			b.Fatal(err)
		}
		writes += sink.writes
		bytesWritten += sink.bytes
		if err := f.Close(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	b.ReportMetric(float64(writes)/float64(b.N), "writes/op")
	b.ReportMetric(float64(bytesWritten)/float64(b.N), "bytes/op")
}

// mergeInputsB is mergeInputs for a benchmark. The helper it mirrors takes a
// *testing.T, and testing.TB would not let it call require's T-typed helpers.
func mergeInputsB(b *testing.B, docs []searchengine.Document, n int) []*mappedSegment {
	b.Helper()
	per := (len(docs) + n - 1) / n
	var ins []*mappedSegment
	for i := 0; i < len(docs); i += per {
		seg, err := Format{}.Build(docs[i:min(i+per, len(docs))])
		if err != nil {
			b.Fatal(err)
		}
		ins = append(ins, seg.(*mappedSegment))
	}
	if len(ins) != n {
		b.Fatalf("fixture built %d inputs, want %d", len(ins), n)
	}
	return ins
}
