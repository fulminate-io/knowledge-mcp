// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// bucket_swap_corruption_test.go — A CORRUPT CONSTITUENT IS WITHDRAWN BY THE SWAP
// THAT FOUND IT, on both swap forms.
//
// THE GAP THIS CLOSES. containCorrupt reports what it CAUGHT — a raise that unwound
// into it — but a format whose own merge catches its raise and RETURNS the typed
// error hands the engine an ordinary error return, and bm25's MergeTo does exactly
// that (formats/bm25/corrupt_segment_test.go pins it: the merge "must report the
// corruption rather than emitting a segment built from it", ErrorAs
// *CorruptSegmentError). The background merger routed that error into reportCorrupt
// and the two bucket swaps did not, so a corruption found by a CONSOLIDATION was
// reported to nobody: on the v0.10.6 release candidate one segment failed a graph's
// resident-count bound 19 consecutive times, and the file was quarantined only when
// an unrelated search happened to touch the same bytes.
//
// BOTH SHAPES A FORMAT CAN PRODUCE ARE DRIVEN, because they reach the reporter by
// DIFFERENT ROUTES and only one of them was covered at first. bm25 catches its own
// raise and RETURNS the condition, which reaches the reporter through the swap's
// classification of its error return. hnsw's MergeTo RAISES, and so does the
// read-back for every format (merge_entry.go), which reaches the reporter through
// containCorrupt's own boundary — and containCorrupt then returns that same value
// as the error, so the swap classifies a condition already reported. One report per
// condition is the invariant, and the raise shape is the one that can break it.

// returnedCorruptionFormat is the mock format with ONE behaviour changed: MergeTo
// RETURNS a corruption attributed to the segment named in `blame`, the way a format
// that contains its own raise does.
type returnedCorruptionFormat struct {
	mockFormat
	blame *SegmentID
}

func (f returnedCorruptionFormat) MergeTo(MergeSink, []Segment[mockQuery, mockStats], []func(ExternalID) bool) (int64, error) {
	return 0, &CorruptSegmentError{ID: *f.blame, Detail: "test: posting run past the blob"}
}

// raisingCorruptionFormat is the same double for the OTHER shape: MergeTo raises
// from beneath, where a format's read path raises, and never returns.
type raisingCorruptionFormat struct {
	mockFormat
	blame *SegmentID
}

// SIGNATURE PARITY, and the vacuous error result is the parity exemption rather
// than an oversight: SegmentFormat.MergeTo (segment.go:61) returns (int64, error)
// and every real implementation can fail, so this double keeps the shared
// signature. This body cannot fail — it raises — and the return below is
// unreachable.
func (f raisingCorruptionFormat) MergeTo(MergeSink, []Segment[mockQuery, mockStats], []func(ExternalID) bool) (int64, error) {
	RaiseCorruptIn(*f.blame, "test: posting run past the blob")
	return 0, nil
}

// swapCorruptionFixture publishes two segments through the real seal path and
// returns the engine, the id the merge will blame, and the reports the owner saw.
func swapCorruptionFixture(t *testing.T) (
	*SegmentedIndex[mockQuery, mockStats], SegmentID, func() []*CorruptSegmentError,
) {
	t.Helper()
	return swapCorruptionFixtureOf(t, func(blame *SegmentID) SegmentFormat[mockQuery, mockStats] {
		return returnedCorruptionFormat{blame: blame}
	})
}

// swapCorruptionFixtureOf is swapCorruptionFixture over a caller-chosen format, so
// the returned and raised shapes run the same fixture rather than two of them.
func swapCorruptionFixtureOf(
	t *testing.T, mk func(blame *SegmentID) SegmentFormat[mockQuery, mockStats],
) (*SegmentedIndex[mockQuery, mockStats], SegmentID, func() []*CorruptSegmentError) {
	t.Helper()
	var (
		mu       sync.Mutex
		reported []*CorruptSegmentError
	)
	blame := new(SegmentID)
	e := closeOnCleanup(t, New[mockQuery, mockStats](mk(blame), Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: MergeDisabledCountTarget,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		ScratchDir:         t.TempDir(),
		OnCorruptSegment: func(err *CorruptSegmentError) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err)
		},
	}))
	for _, id := range []ExternalID{"swap-a", "swap-b"} {
		_, err := e.AddSealAndSupersede([]Document{{ID: id, Fields: map[string]string{FieldContent: "alpha"}}})
		require.NoError(t, err)
	}
	ids := e.ResidentSegmentIDs()
	require.Len(t, ids, 2, "PRECONDITION: two resident segments, so the swap has something to consolidate")
	*blame = ids[0]
	return e, ids[0], func() []*CorruptSegmentError {
		mu.Lock()
		defer mu.Unlock()
		return append([]*CorruptSegmentError(nil), reported...)
	}
}

// requireWithdrawn is what both rows assert, so the two swap forms cannot drift
// into asserting different things about one seam.
func requireWithdrawn(
	t *testing.T, e *SegmentedIndex[mockQuery, mockStats], corrupt SegmentID, reports []*CorruptSegmentError,
) {
	t.Helper()
	require.Len(t, reports, 1,
		"the owner must be told EXACTLY once: it is what quarantines the file, and a swap that reports nothing "+
			"leaves the segment published to fail the next attempt the same way, forever")
	require.Equal(t, corrupt, reports[0].ID,
		"and the report must name the segment the format blamed — an unattributed corruption withdraws nothing")
	require.NotContains(t, e.ResidentSegmentIDs(), corrupt,
		"the corrupt segment must leave the PUBLISHED SET, or it is offered to every later candidate set")
}

// TestReplaceBucketWithdrawsACorruptConstituent is the per-partition swap — the
// form the resident-count bound drives on every crossing.
func TestReplaceBucketWithdrawsACorruptConstituent(t *testing.T) {
	e, corrupt, reports := swapCorruptionFixture(t)

	id, err := e.ReplaceBucket(0, 1, e.ResidentSegmentIDs(), nil, nil)
	require.Error(t, err, "the swap still FAILS: a merge that could not read its inputs must not publish an output")
	require.Empty(t, id)
	var ce *CorruptSegmentError
	require.ErrorAs(t, err, &ce, "and the caller still receives the typed corruption it needs to log")
	requireWithdrawn(t, e, corrupt, reports())
}

// TestReplaceBucketGroupWithdrawsACorruptConstituent is the GROUP form, and it owes
// the same withdrawal for a sharper reason: a group publishes nothing on a failure
// and the group swap is the only path that can consolidate a segment spanning
// several partitions, so a corruption it reports to nobody is a corpus that can
// never be re-emitted.
func TestReplaceBucketGroupWithdrawsACorruptConstituent(t *testing.T) {
	e, corrupt, reports := swapCorruptionFixture(t)

	published, _, err := e.ReplaceBucketGroup(
		t.Context(), 1, e.ResidentSegmentIDs(), []BucketWork{{Bucket: 0}})
	require.Error(t, err)
	require.Empty(t, published, "the group's all-or-nothing contract is unchanged: a failed group publishes nothing")
	requireWithdrawn(t, e, corrupt, reports())
}

// TestASwapReportsARaisedCorruptionExactlyOnce is the OTHER route into the
// reporter, and the one a per-call-site rule gets wrong.
//
// A RAISE IS REPORTED BY THE BOUNDARY IT UNWOUND INTO. containCorrupt reports what
// it caught AND returns that same value as the error (corruption.go), so the swap
// then classifies a condition that has already been reported. Reporting it again
// costs two things an operator reads: the second WithdrawSegment MISSES, which
// prints the deliberately ambiguous "not in the published set" line — the one
// signal that means DAMAGE IN PLACE, here fired deterministically on a clean
// single-threaded consolidation — and the owner's hook fires twice for one event,
// which in this product is a second quarantine attempt against a file already moved
// aside.
//
// BOTH SWAP FORMS, because both classify their error return and both reach
// mergeEntry: a rule that lived at the call sites would have to be right twice.
func TestASwapReportsARaisedCorruptionExactlyOnce(t *testing.T) {
	// Serial: the damage-in-place assertion reads the PROCESS-GLOBAL slog default.
	swaps := map[string]func(*testing.T, *SegmentedIndex[mockQuery, mockStats]){
		"the_per_partition_swap": func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			_, err := e.ReplaceBucket(0, 1, e.ResidentSegmentIDs(), nil, nil)
			require.Error(t, err)
		},
		"the_group_swap": func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			_, _, err := e.ReplaceBucketGroup(t.Context(), 1, e.ResidentSegmentIDs(), []BucketWork{{Bucket: 0}})
			require.Error(t, err)
		},
	}
	for name, swap := range swaps {
		t.Run(name, func(t *testing.T) {
			e, corrupt, reports := swapCorruptionFixtureOf(t, func(blame *SegmentID) SegmentFormat[mockQuery, mockStats] {
				return raisingCorruptionFormat{blame: blame}
			})

			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			swap(t, e)
			slog.SetDefault(prev)

			requireWithdrawn(t, e, corrupt, reports())
			require.NotContains(t, buf.String(), "corrupt segment is not in the published set",
				"a corruption reported TWICE prints the damage-in-place line on its second withdrawal attempt, which "+
					"is the one signal that tells an operator the bytes changed under a filename nothing is keyed on; "+
					"firing it on an ordinary consolidation spends that signal")
		})
	}
}
