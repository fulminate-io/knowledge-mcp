// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// corrupt_segment_test.go — the containment proof, driven by a REAL corrupt
// segment taken from the incident rather than by a synthesized one.
//
// testdata/corrupt-posting-run.seg IS THE PRODUCTION ARTIFACT — the file that
// crashed the daemon. Its dictionary addresses a posting run at an offset that
// belongs to a much larger layout than the payload it sits in. Every number in
// the assertion below — 107 documents, offset 327938, a 64739-byte payload — is
// read out of that file, not chosen.
//
// ITS CONTENT ADDRESS IS INTACT, and that is the surprising part. The file was
// first read as a hash-vs-name mismatch, which is what pointed the investigation
// at a producer writing stale-id bytes; that reading hashed the whole stored
// file, and the id names the PAYLOAD. Hashed correctly the payload gives exactly
// fbc34f9566…, its own filename. So the producer hashed precisely the bytes it
// wrote, and the damage is INTERNAL to a faithfully-stored payload — which is
// why no write-time hash check could have caught it, and why containment is the
// defense that matters here. See the segmentdist package's
// TestIncidentArtifacts_ContentAddressingIsIntact for that correction, executable.
//
// A SYNTHESIZED BLOB WOULD NOT HAVE PROVEN THIS. The condition depends on the
// dictionary and the payload disagreeing in a specific way that a hand-built
// "corrupt" blob only reproduces if the author already knows the shape — which
// is precisely the knowledge the incident had to supply. Keeping the real bytes
// means the test fails if the containment stops covering the case that actually
// happened.

// realCorruptSegment opens the preserved blob through the same path a cache read
// uses: split the stored file into envelope and payload, then open the payload.
func realCorruptSegment(t *testing.T) *mappedSegment {
	t.Helper()
	raw, err := os.ReadFile("testdata/corrupt-posting-run.seg")
	require.NoError(t, err, "the preserved incident artifact must be present")
	_, payload, err := searchengine.SplitStoredBlob(raw)
	require.NoError(t, err)

	// THE HEADER IS INTACT AND THAT IS THE POINT. This segment opens cleanly —
	// openSegmentV2 validates the header and the O(1) sections and finds nothing
	// wrong, because the damage is in per-term data that open deliberately does
	// not walk. A containment that only covered open-time rejection would not
	// have caught this.
	seg, err := openSegmentV2(payload)
	require.NoError(t, err, "the corrupt segment still opens: the damage is below the header")
	require.Len(t, payload, 64739, "payload size is the one the incident reported")
	require.Equal(t, 107, seg.docCount)
	return seg
}

// TestRealCorruptSegment_RaisesTypedCorruptionNotBarePanic pins that the
// incident's exact condition is reachable from the real bytes AND that it now
// arrives as a value the engine can contain.
//
// THE TYPE IS THE WHOLE FIX AT THIS LAYER. The condition was already loud — it
// panicked with this same sentence — but it panicked with a STRING, which is
// indistinguishable from a nil dereference or any other bug. Nothing could
// recover it without also swallowing genuine defects, so nothing recovered it,
// and one bad file killed the process on every query that touched it.
func TestRealCorruptSegment_RaisesTypedCorruptionNotBarePanic(t *testing.T) {
	seg := realCorruptSegment(t)

	var raised any
	func() {
		defer func() { raised = recover() }()
		for _, mf := range seg.fields {
			mf.eachTerm(func(string, []uint32, []uint16) {})
		}
	}()

	require.NotNil(t, raised, "walking the corrupt dictionary must still raise: a silent walk would mean the invariant check stopped running")
	ce, ok := raised.(*searchengine.CorruptSegmentError)
	require.Truef(t, ok, "raised %T, want *searchengine.CorruptSegmentError — an untyped panic cannot be contained without swallowing real bugs", raised)
	require.Equal(t,
		"bm25: posting run of 107 at offset 327938 is misaligned or past the 64739-byte blob",
		ce.Detail,
		"the detail is the incident's own message, verbatim")
}

// TestRealCorruptSegment_MergeReturnsErrorRatherThanCrashing is the merge half of
// the containment.
//
// IT IS A SEPARATE PATH FROM SEARCH AND HAD TO BE COVERED SEPARATELY. The
// incident's stack showed the crash arriving through BOTH mappedSegment.Search
// and the merge's drainCursor, and the merge runs on a background goroutine
// where a panic is equally fatal and rather harder to attribute. A merge that
// hits a corrupt constituent must fail as a merge, leaving the constituents
// untouched for the owner to quarantine.
func TestRealCorruptSegment_MergeReturnsErrorRatherThanCrashing(t *testing.T) {
	seg := realCorruptSegment(t)

	sink, err := os.CreateTemp(t.TempDir(), "merge-*.seg")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	accept := []func(searchengine.ExternalID) bool{func(searchengine.ExternalID) bool { return true }}

	var n int64
	var mergeErr error
	require.NotPanics(t, func() {
		n, mergeErr = streamMergeToFile(sink, []*mappedSegment{seg}, accept, seg.kind)
	}, "a corrupt constituent must not panic the merge goroutine")

	require.Error(t, mergeErr, "the merge must report the corruption rather than emitting a segment built from it")
	var ce *searchengine.CorruptSegmentError
	require.ErrorAs(t, mergeErr, &ce, "the merge error must carry the corruption type")
	require.Zero(t, n, "a refused merge reports no length")
}

// TestRealCorruptSegment_DictionaryCursorRaisesTypedToo covers a SECOND way the
// same file kills a reader, found only because the fixture was driven through a
// path the first test does not take.
//
// eachTerm calls postings() for every term, so it hits the posting-run invariant
// almost immediately and never advances the cursor far enough to reach the
// front-coding arithmetic. Walking the dictionary WITHOUT reading postings does,
// and before the guards in nextBlocked that walk panicked with a bare runtime
// error — "slice bounds out of range [:32] with capacity 8" — which the engine's
// boundary deliberately does NOT contain, because an untyped panic is
// indistinguishable from a genuine bug. The daemon would still have died.
//
// THE LESSON THIS PINS is that containment is per-ACCESSOR, not per-file: adding
// a typed raise to the three checks that already existed did not make the format
// safe, and only exercising a different traversal showed it.
func TestRealCorruptSegment_DictionaryCursorRaisesTypedToo(t *testing.T) {
	seg := realCorruptSegment(t)

	var raised any
	func() {
		defer func() { raised = recover() }()
		for _, mf := range seg.fields {
			it := mf.iter()
			for it.next() { //nolint:revive // the walk itself is the probe; the cursor is what must not escape its blob
			}
		}
	}()

	require.NotNil(t, raised, "walking this file's dictionary must raise")
	_, ok := raised.(*searchengine.CorruptSegmentError)
	require.Truef(t, ok,
		"the dictionary cursor raised %T rather than a typed corruption — an untyped panic escapes the engine's boundary and kills the process", raised)
}

// TestRealCorruptSegment_SearchRaisesTypedNotRuntimePanic is the path an
// ORDINARY QUERY takes, and it is the one the incident's stack actually named.
//
// The segment's dictionary is BLOCKED, so a search resolves a term through
// lookupBlocked into scanBlock — the front-coded walk that is nextBlocked's
// twin. Before scanBlock was bounds-checked that walk raised a bare runtime
// slice panic on this file, which the engine's boundary re-panics by design, so
// the daemon died on a plain search even with the posting-run invariant typed.
//
// The terms are ordinary English so the query resolves through the dictionary
// rather than missing early; what matters is that whatever it reaches, it
// reaches as a CONTAINABLE value.
func TestRealCorruptSegment_SearchRaisesTypedNotRuntimePanic(t *testing.T) {
	seg := realCorruptSegment(t)
	require.Equal(t, dictBlocked, seg.kind,
		"this fixture is a blocked dictionary, which is why a search reaches scanBlock")

	stats := &CorpusStats{TotalDocs: 1000, FieldAvgLen: map[string]float64{}}
	q := NewQuery("node graph segment store index search cache merge value type func")

	var raised any
	func() {
		defer func() { raised = recover() }()
		_ = seg.Search(q, stats, 10, func(searchengine.ExternalID) bool { return true })
	}()

	// NO "maybe it did not reach the damage" ESCAPE. This query is known to reach
	// it, and a branch that passes when it does not would make the test report
	// green on a containment that had stopped working.
	require.NotNil(t, raised, "this query reaches the damaged region and must raise")
	ce, ok := raised.(*searchengine.CorruptSegmentError)
	require.Truef(t, ok,
		"a search raised %T rather than a typed corruption — an untyped panic escapes the engine's boundary and kills the daemon", raised)
	t.Logf("search raised: %s", ce.Detail)
}

// TestMergeWithdrawsTheCorruptConstituentAndStops is the review's own repro
// shape, and it is the gate my first attempt at this needed and did not have.
//
// WHAT WAS WRONG BEFORE. The merge boundary cannot say which of several
// interleaved inputs raised, so it reported an UNATTRIBUTED corruption;
// reportCorrupt skips the withdrawal on an empty id, the quarantine refuses it,
// and the refusal was discarded — so nothing was withdrawn, both segments stayed
// published, and the merge re-selected the same pair on the next tick. Executed
// on the production artifact that produced 11 corruption reports in 550ms with
// both segments still resident. Making the raise carry its own segment's id is
// what turns that loop into one withdrawal.
//
// THE PRODUCTION ARTIFACT IS THE INPUT, not a synthesized one: the raise this
// exercises is the incident's own class (a posting run past the blob), which is
// exactly the site that was still unattributed after the first fix.
func TestMergeWithdrawsTheCorruptConstituentAndStops(t *testing.T) {
	raw, err := os.ReadFile("testdata/corrupt-posting-run.seg")
	require.NoError(t, err, "the preserved incident artifact must be present")
	_, corruptPayload, err := searchengine.SplitStoredBlob(raw)
	require.NoError(t, err)

	sum := sha256.Sum256(corruptPayload)
	corruptID := hex.EncodeToString(sum[:])

	var (
		mu       sync.Mutex
		reported []searchengine.SegmentID
	)
	// SegmentCountTarget 1 keeps the merge trigger armed for as long as more than
	// one segment is published, which is what turned the unattributed report into
	// a spin rather than a single event.
	eng := searchengine.New[Query, *CorpusStats](New(), searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 1,
		ScratchDir:         t.TempDir(),
		OnCorruptSegment: func(cerr *searchengine.CorruptSegmentError) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, cerr.ID)
		},
	})
	t.Cleanup(eng.Close)

	healthy, _, err := New().Build([]searchengine.Document{{
		ID: "healthy-1", Fields: map[string]string{"content": "a healthy segment beside the corrupt one"},
	}})
	require.NoError(t, err)
	healthyBlob, err := healthy.Encode()
	require.NoError(t, err)
	healthySum := sha256.Sum256(healthyBlob)

	require.NoError(t, eng.Import([]searchengine.SegmentBlob{
		{ID: corruptID, Bytes: corruptPayload},
		{ID: hex.EncodeToString(healthySum[:]), Bytes: healthyBlob},
	}, nil))

	// Let the merger run well past several 50ms ticks.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reported) > 0
	}, 10*time.Second, 20*time.Millisecond, "the merge must reach the corrupt constituent and report it")

	time.Sleep(600 * time.Millisecond) // the window the review measured 11 reports in

	mu.Lock()
	got := append([]searchengine.SegmentID(nil), reported...)
	mu.Unlock()

	// EVERY REPORT NAMES THE CORRUPT SEGMENT. An unattributed one withdraws
	// nothing, which is precisely how the spin survived the first fix.
	for i, id := range got {
		require.Equal(t, corruptID, id,
			"report %d of %d is unattributed or names the wrong segment; an unattributed corruption withdraws nothing and the merge re-fires", i+1, len(got))
	}

	// AND THE LOOP TERMINATES, because the withdrawal removed the constituent the
	// merge kept choosing. The review measured 11 reports in 550ms without it.
	// EXACTLY ONE. The margin is not a guess: a correct withdrawal reports once
	// and the spin reports eleven to thirteen in this same window, so anything
	// above one is the loop still running. A loose bound here would pass a
	// withdrawal that took two attempts to stick, which is a different behaviour
	// wearing the same green.
	require.Len(t, got, 1,
		"the merge must report the corrupt constituent ONCE and stop; %d reports means it is still re-selecting it", len(got))

	var stillPublished []searchengine.SegmentID
	for _, b := range eng.Export() {
		stillPublished = append(stillPublished, b.ID)
	}
	require.NotContains(t, stillPublished, corruptID,
		"the corrupt segment must be withdrawn from the published set, or the next merge picks it up again")
}
