// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// corruption_test.go — the ENGINE half of the containment: one segment whose
// stored bytes are unreadable must cost exactly that segment.
//
// IT USES ITS OWN FORMAT RATHER THAN THE SHARED mockFormat. A sentinel wired
// into the mock every engine test uses would be a trap: any future test whose
// fixture happened to contain the magic value would start raising for reasons
// its author never wrote. This format exists only in this file and raises only
// when explicitly told to.

// corruptibleRow is one document in the corruptible format.
type corruptibleRow struct {
	ID      ExternalID
	Content string
}

// corruptibleSegment raises a CorruptSegmentError from Search when raise is set,
// standing in for a format whose on-disk bytes violate an invariant it
// guarantees. searches counts the calls that actually reached the payload, so a
// test can tell "the boundary contained it" from "the search never ran".
type corruptibleSegment struct {
	rows     []corruptibleRow
	raise    bool
	raiseIDs bool
	mu       sync.Mutex
	searches int
}

func (s *corruptibleSegment) Search(q mockQuery, _ mockStats, k int, accept func(ExternalID) bool) []Hit {
	s.mu.Lock()
	s.searches++
	s.mu.Unlock()
	if s.raise {
		// Raised from where a format's read path would raise it: beneath the
		// engine, with no error return available.
		RaiseCorrupt("test: posting run past the blob")
	}
	var hits []Hit
	for _, r := range s.rows {
		if accept != nil && !accept(r.ID) {
			continue
		}
		if score := float64(strings.Count(r.Content, q.term)); score > 0 {
			hits = append(hits, Hit{ID: r.ID, Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func (s *corruptibleSegment) IDs() []ExternalID {
	if s.raiseIDs {
		// Raised from where a format raises when the LOAD path walks it: bm25's
		// member() and hnsw's externalIDAt both resolve per-document data here,
		// which a lazy Decode never touched.
		RaiseCorrupt("test: member span past the blob")
	}
	ids := make([]ExternalID, 0, len(s.rows))
	for _, r := range s.rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// Encode returns bytes unique to this segment's rows so the engine's content
// hash gives each fixture a distinct SegmentID — the id the boundary reports.
func (s *corruptibleSegment) Encode() ([]byte, error) {
	var b strings.Builder
	for _, r := range s.rows {
		b.WriteString(r.ID)
		b.WriteByte('=')
		b.WriteString(r.Content)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func (s *corruptibleSegment) HeapBytes() int64 { return int64(len(s.rows)) * 8 }

// corruptibleFormat builds corruptibleSegments. A document whose content is
// corruptibleMarker seals a segment that raises.
type corruptibleFormat struct{}

const corruptibleMarker = "RAISE-CORRUPTION"

// corruptibleIDsMarker seals a segment that raises when its MEMBER LIST is
// walked rather than when it is searched. It is a separate marker so the
// search-path fixtures above keep raising exactly where they always did: a
// segment that raised on IDs() too would blow up while the engine was still
// sealing it, and those tests would stop testing the fan-out.
const corruptibleIDsMarker = "RAISE-CORRUPTION-ON-IDS"

func (corruptibleFormat) Name() string { return "corruptible" }

func (corruptibleFormat) Build(docs []Document) (Segment[mockQuery, mockStats], error) {
	seg := &corruptibleSegment{}
	for _, d := range docs {
		content := d.Fields[FieldContent]
		if content == corruptibleMarker {
			seg.raise = true
		}
		if content == corruptibleIDsMarker {
			seg.raiseIDs = true
		}
		seg.rows = append(seg.rows, corruptibleRow{ID: d.ID, Content: content})
	}
	return seg, nil
}

func (corruptibleFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	seg := &corruptibleSegment{}
	for line := range strings.SplitSeq(strings.TrimRight(string(blob), "\n"), "\n") {
		id, content, _ := strings.Cut(line, "=")
		if content == corruptibleMarker {
			seg.raise = true
		}
		if content == corruptibleIDsMarker {
			seg.raiseIDs = true
		}
		seg.rows = append(seg.rows, corruptibleRow{ID: id, Content: content})
	}
	return seg, nil
}

func (corruptibleFormat) MergeTo(MergeSink, []Segment[mockQuery, mockStats], []func(ExternalID) bool) (int64, error) {
	return 0, errors.New("corruptibleFormat does not merge")
}

func (corruptibleFormat) AggregateStats([]Segment[mockQuery, mockStats]) mockStats {
	return mockStats{}
}

// TestSearch_CorruptSegmentIsContainedAndReported is the containment proof at the
// engine's fan-out — the exact goroutine whose panic used to take the process
// down and, because the daemon is restarted automatically, took it down again on
// every retry until a human intervened.
//
// THE THREE ASSERTIONS ARE ONE PROPERTY EACH, and the middle one is what makes
// this a containment rather than a swallow: the healthy segment must still
// answer. A boundary that caught the panic and returned nothing would satisfy
// "no panic" while leaving the corpus just as unserviceable.
func TestSearch_CorruptSegmentIsContainedAndReported(t *testing.T) {
	var (
		mu       sync.Mutex
		reported []*CorruptSegmentError
	)
	e := closeOnCleanup(t, New[mockQuery, mockStats](corruptibleFormat{}, Options{
		MinSegmentDocs: 1,
		OnCorruptSegment: func(err *CorruptSegmentError) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err)
		},
	}))

	require.NoError(t, e.Add([]Document{{ID: "healthy-1", Fields: map[string]string{FieldContent: "alpha alpha"}}}))
	require.NoError(t, e.Add([]Document{{ID: "poisoned-1", Fields: map[string]string{FieldContent: corruptibleMarker}}}))

	var hits []Hit
	require.NotPanics(t, func() {
		hits = e.Search(mockQuery{term: "alpha"}, 10)
	}, "a corrupt segment must not panic the search fan-out — this is the crash the incident was")

	// THE SURVIVING CORPUS STILL ANSWERS. Without this the test would pass
	// against a boundary that turned one bad segment into an empty result set,
	// which is the outage restated rather than fixed.
	require.Len(t, hits, 1, "the healthy segment must still answer")
	require.Equal(t, ExternalID("healthy-1"), hits[0].ID)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, reported, 1, "the owner must be told exactly once per search that touched it")
	require.NotEmpty(t, reported[0].ID, "the report must NAME the segment, or the owner cannot know which file to quarantine")
	require.Contains(t, reported[0].Detail, "posting run past the blob")
	require.Contains(t, reported[0].Error(), reported[0].ID, "the rendered error carries the id")
}

// TestSearch_NonCorruptionPanicStillCrashes pins the OTHER half of the
// containment, and it is the assertion that keeps this from becoming a
// recover-everything.
//
// A boundary that swallowed any panic would convert an ordinary bug — a nil map,
// an index slip — into silently wrong search results, which on a database is
// worse than the crash it replaced. Only CorruptSegmentError is contained;
// everything else must still reach the runtime with its stack.
func TestSearch_NonCorruptionPanicStillCrashes(t *testing.T) {
	var corrupt *CorruptSegmentError
	require.PanicsWithValue(t, "an ordinary bug", func() {
		defer catchCorrupt("seg-1", &corrupt)
		panic("an ordinary bug")
	}, "a panic that is not a corruption must pass straight through the boundary")
	require.Nil(t, corrupt, "an unrelated panic must not be recorded as corruption")
}

// TestCatchCorrupt_RecoversWhenDeferredDirectly is a regression pin on a defect
// this change shipped and the merge test caught.
//
// recover() only stops a panic when the DEFERRED FUNCTION CALLS IT ITSELF. The
// exported CatchCorrupt was first written as a one-line delegation to the
// unexported catchCorrupt, which put recover one frame too deep: it returned
// nil, the panic kept unwinding, and the containment did nothing at all while
// reading as though it worked. Both entry points now call recover directly, and
// this test drives each of them through a real panic.
func TestCatchCorrupt_RecoversWhenDeferredDirectly(t *testing.T) {
	t.Run("unexported", func(t *testing.T) {
		var got *CorruptSegmentError
		require.NotPanics(t, func() {
			defer catchCorrupt("seg-unexported", &got)
			RaiseCorrupt("boom %d", 1)
		})
		require.NotNil(t, got)
		require.Equal(t, SegmentID("seg-unexported"), got.ID)
		require.Equal(t, "boom 1", got.Detail)
	})
	t.Run("exported", func(t *testing.T) {
		var got *CorruptSegmentError
		require.NotPanics(t, func() {
			defer CatchCorrupt("seg-exported", &got)
			RaiseCorrupt("boom %d", 2)
		})
		require.NotNil(t, got)
		require.Equal(t, SegmentID("seg-exported"), got.ID)
		require.Equal(t, "boom 2", got.Detail)
	})
}

// TestImport_CorruptSegmentIsContainedAndReported is the containment proof on the
// LOAD path, which is the worst place this failure can arrive.
//
// WHY THE LOAD PATH IS WORSE THAN THE QUERY PATH. Import runs one goroutine per
// blob and calls entryFromDecoded, which walks the segment's members to build its
// id map — resolving per-document data that a lazy Decode deliberately never
// touches. Raised there, with nothing above it to recover, the panic ends a
// process that is still STARTING UP: the daemon never reaches a state where any
// segment can be quarantined, and a supervisor restarting it walks straight back
// into the same blob. That is the crash loop the whole containment effort exists
// to end, and it survived here until this test.
//
// THE HEALTHY BLOB IN THE SAME BATCH IS THE KNOWN-NEGATIVE: a boundary that
// failed the whole import would satisfy "no panic" while leaving the corpus as
// unserviceable as the crash did.
func TestImport_CorruptSegmentIsContainedAndReported(t *testing.T) {
	var (
		mu       sync.Mutex
		reported []*CorruptSegmentError
	)
	e := closeOnCleanup(t, New[mockQuery, mockStats](corruptibleFormat{}, Options{
		MinSegmentDocs: 1,
		OnCorruptSegment: func(err *CorruptSegmentError) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err)
		},
	}))

	bad := SegmentBlob{ID: "bad-on-load", Bytes: []byte("doc-1=" + corruptibleIDsMarker + "\n")}
	good := SegmentBlob{ID: "good", Bytes: []byte("doc-2=healthy\n")}

	err := e.Import([]SegmentBlob{bad, good}, nil)

	// The import FAILS, naming the segment — it does not silently publish a
	// partial batch, and it does not take the process with it.
	require.Error(t, err, "importing a segment whose member walk raises must return an error, not panic the process")
	var ce *CorruptSegmentError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, SegmentID("bad-on-load"), ce.ID,
		"the load boundary must name the blob it was reading, or an operator cannot tell which file to quarantine")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, reported, 1, "the owner must be told, or nothing quarantines the file and the next start hits it again")
	require.Equal(t, SegmentID("bad-on-load"), reported[0].ID)
}

// TestReportCorrupt_WithdrawsTheNamedSegmentAndOnlyThat is the proof the
// withdrawal works, and it is the fix four separate review findings consume.
//
// WHY WITHDRAWAL AND NOT JUST QUARANTINE. Moving the FILE aside while the engine
// still publishes the segment produces a state worse than either half. Export
// keeps listing the id, so the owner's resident-set diff sees a blob its cache no
// longer holds, decides it is new, and writes the corrupt bytes back under the
// same name — passing the content-address check on the way, because this class
// hashes correctly. The quarantine is undone by the next persist.
//
// THE SURVIVING SEGMENT IS THE KNOWN-NEGATIVE: a withdrawal that dropped the
// whole set would satisfy "the corrupt id is gone" while destroying the corpus.
func TestReportCorrupt_WithdrawsTheNamedSegmentAndOnlyThat(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](corruptibleFormat{}, Options{MinSegmentDocs: 1}))

	require.NoError(t, e.Import([]SegmentBlob{
		{ID: "seg-a", Bytes: []byte("a-1=alpha\n")},
		{ID: "seg-b", Bytes: []byte("b-1=bravo\n")},
	}, nil))
	require.Len(t, e.Export(), 2, "precondition: both segments are published")
	require.Equal(t, 2, e.DistinctResidentDocCount())

	// The engine is told seg-a is corrupt, exactly as a contained raise would.
	e.reportCorrupt(&CorruptSegmentError{ID: "seg-a", Detail: "test"})

	// EXPORT NO LONGER LISTS IT, which is what stops the resident-set diff from
	// writing the bytes back.
	var exported []SegmentID
	for _, b := range e.Export() {
		exported = append(exported, b.ID)
	}
	require.Equal(t, []SegmentID{"seg-b"}, exported,
		"the withdrawn segment must leave the published set, or the next persist resurrects its bytes under the same id")

	// THE ACCOUNTING SEES THE LOSS, which is how the heal learns the corpus is
	// short. A withdrawal the count could not see leaves every gate reporting a
	// corpus that is whole when it is not.
	require.Equal(t, 1, e.DistinctResidentDocCount(),
		"the documents the withdrawn segment answered for must stop counting as resident")

	// KNOWN-NEGATIVE: the healthy segment is untouched and still answers.
	require.NotNil(t, e.set.Load().entryByID("seg-b"))
	require.Nil(t, e.set.Load().entryByID("seg-a"))
}

// TestHandleCorrupt_BoundaryDoesNotOverwriteAnAttributedRaise is F1 in one
// assertion: a cross-segment probe raises with the id of the segment it was
// READING, and the boundary of the segment that happened to be ASKING must not
// relabel it.
//
// Unfixed, this is how a healthy segment gets quarantined: bm25's scored query
// sums document frequency across every resident segment from one goroutine, so
// segment A's corruption surfaces under segment B's boundary, and B's file was
// the one moved aside — repeatedly, until every healthy segment had been
// quarantined and the corrupt one was still serving.
func TestHandleCorrupt_BoundaryDoesNotOverwriteAnAttributedRaise(t *testing.T) {
	t.Run("an attributed raise keeps its own id", func(t *testing.T) {
		var got *CorruptSegmentError
		func() {
			defer catchCorrupt("the-asking-segment", &got)
			RaiseCorruptIn("the-reading-segment", "cross-segment probe")
		}()
		require.NotNil(t, got)
		require.Equal(t, SegmentID("the-reading-segment"), got.ID,
			"the segment that RAISED must be the one named, not the boundary that caught it")
	})

	t.Run("an unattributed raise is still stamped by the boundary", func(t *testing.T) {
		// The known-negative for the rule above: without this, refusing to stamp
		// would look identical to stamping correctly, and every single-segment
		// raise would arrive nameless.
		var got *CorruptSegmentError
		func() {
			defer catchCorrupt("the-owning-segment", &got)
			RaiseCorrupt("read inside one segment")
		}()
		require.NotNil(t, got)
		require.Equal(t, SegmentID("the-owning-segment"), got.ID,
			"a raise that names no segment must still be attributed by the boundary that owns one")
	})
}

// TestImport_ReleasesTheMappingWhenTheBlobIsUnusable reproduces F7: the load
// path's failure arms never released the blob's mapping.
//
// Release is normally handed to a cleanup keyed on the ENTRY's reachability, so
// a path that produces no entry leaves the mapping with nobody holding a
// reference to free it. It leaks per blob per attempt, and the attempt repeats:
// an evicted pool re-tries the load on every touch, so one unreadable segment
// leaks a mapping for as long as anything touches that graph.
func TestImport_ReleasesTheMappingWhenTheBlobIsUnusable(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](corruptibleFormat{}, Options{MinSegmentDocs: 1}))

	var released atomic.Int32
	release := func() { released.Add(1) }

	t.Run("a corrupt blob releases its mapping", func(t *testing.T) {
		released.Store(0)
		err := e.Import([]SegmentBlob{{
			ID:      "corrupt-on-load",
			Bytes:   []byte("doc-1=" + corruptibleIDsMarker + "\n"),
			Release: release,
		}}, nil)
		require.Error(t, err)
		require.Equal(t, int32(1), released.Load(),
			"the corruption path produced no entry, so nothing else will ever free this mapping")
	})

	t.Run("KNOWN-NEGATIVE: a good blob does NOT release, because its entry owns the mapping", func(t *testing.T) {
		// Without this arm, releasing unconditionally would satisfy the assertion
		// above while freeing a mapping the published entry is still reading.
		released.Store(0)
		require.NoError(t, e.Import([]SegmentBlob{{
			ID:      "healthy",
			Bytes:   []byte("doc-2=healthy\n"),
			Release: release,
		}}, nil))
		require.Zero(t, released.Load(),
			"a published entry owns its mapping through its own cleanup; releasing here would unmap bytes a reader is using")
	})
}

// corruptMergeOutputFormat merges by WRITING a segment that raises when its
// members are walked. It models the case F3 names: not a corrupt input, but an
// inconsistent artifact this engine itself just produced.
type corruptMergeOutputFormat struct{ corruptibleFormat }

func (corruptMergeOutputFormat) MergeTo(dst MergeSink, _ []Segment[mockQuery, mockStats], _ []func(ExternalID) bool) (int64, error) {
	out := []byte("merged-1=" + corruptibleIDsMarker + "\n")
	n, err := dst.WriteAt(out, 0)
	return int64(n), err
}

// TestMergeEntry_ContainsACorruptMergedOutput reproduces F3.
//
// The merge boundary covered MergeTo and stopped one line short of the READ-BACK
// — Decode, and newEntry, which calls IDs() and walks the member table. So an
// artifact the merge itself wrote inconsistently raised on the merger's own
// goroutine with nothing above it, and took the process down. That is the crash
// loop again, for ENGINE-WRITTEN output: the file it dies on is one this process
// produced, so a restart re-merges the same constituents and dies the same way.
func TestMergeEntry_ContainsACorruptMergedOutput(t *testing.T) {
	e := closeOnCleanup(t, New[mockQuery, mockStats](corruptMergeOutputFormat{}, Options{MinSegmentDocs: 1}))

	require.NoError(t, e.Import([]SegmentBlob{
		{ID: "in-1", Bytes: []byte("a=alpha\n")},
		{ID: "in-2", Bytes: []byte("b=bravo\n")},
	}, nil))

	set := e.set.Load()
	segs := make([]Segment[mockQuery, mockStats], 0, len(set.entries))
	for _, entry := range set.entries {
		segs = append(segs, entry.payload)
	}

	entry, err := e.mergeEntry(segs, nil)
	require.Error(t, err,
		"a merged artifact whose member walk raises must come back as an ERROR from the merge, not as a panic on the merger goroutine")
	require.Nil(t, entry)
	var ce *CorruptSegmentError
	require.ErrorAs(t, err, &ce, "and it must be the typed corruption, so the caller can route it rather than guess")
}
