package searchengine

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// waitForSettle polls until the engine reports at least one merge and its settle
// counter has caught up with its publish counter, and FAILS the test naming the
// deadline and both counters when it does not.
//
// THE TWO COUNTERS ARE THE POINT. MergeCount moves at the CAS publish; the settle
// counter moves when doMerge RETURNS, which is after the OnMerge hook has returned
// on the arms that fire it. A settle counter wired to only some of doMerge's
// post-publish arms leaves the two permanently unequal, and this helper is what
// turns that into a named failure instead of a hang.
func waitForSettle(t testing.TB, e *SegmentedIndex[mockQuery, mockStats]) {
	t.Helper()
	deadline := time.Now().Add(mergeWaitTimeout)
	for time.Now().Before(deadline) {
		if pub, stl := e.MergeCount(), e.SettledMergeCount(); pub >= 1 && pub == stl {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("settle counter never caught up with the publish counter within %s: published=%d settled=%d",
		mergeWaitTimeout, e.MergeCount(), e.SettledMergeCount())
}

// TestSettleAdvancesWithNoMergeHook pins the arm doMerge takes when Options.OnMerge
// is nil: it publishes, skips the callback entirely, and returns. A settle counter
// bumped from inside the hook would never move here, and a completion wait built on
// it would burn its whole deadline on every hookless engine.
func TestSettleAdvancesWithNoMergeHook(t *testing.T) {
	e := mergeEngine(t, 2.0, 4) // no OnMerge; count-driven trigger at 4
	defer e.Close()
	if e.HasMergeHook() {
		t.Fatalf("fixture: this engine must carry NO merge hook, or it tests the wrong arm")
	}
	for i := range 5 {
		if err := e.Add([]Document{doc(ExternalID(rune('a'+i)), "x")}); err != nil {
			t.Fatal(err)
		}
	}
	waitForMerge(t, e)
	waitForSettle(t, e)
}

// TestSettleAdvancesWhenTheMergedBlobCannotBeEncoded pins the arm doMerge RETURNS
// on without firing the hook: the consolidated entry is published, and then
// blobParts fails, so OnMerge is never called at all. The settle counter must still
// advance — otherwise one un-encodable merge hangs every later completion wait on
// that engine forever.
func TestSettleAdvancesWhenTheMergedBlobCannotBeEncoded(t *testing.T) {
	var hookCalls atomic.Int64
	e := closeOnCleanup(t, New[mockQuery, mockStats](encodeFailAfterMergeFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 4,
		OnMerge:            func(MergeResult) { hookCalls.Add(1) },
	}))
	defer e.Close()

	for i := range 5 {
		if err := e.Add([]Document{doc(ExternalID(rune('a'+i)), "x")}); err != nil {
			t.Fatal(err)
		}
	}
	waitForMerge(t, e)
	waitForSettle(t, e)

	// THE DISCRIMINATING ASSERTION: this arm is only the arm under test while the
	// hook stayed unfired. If the hook ran, the merged blob encoded after all and
	// this test silently became a copy of the hook-fired case.
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("fixture: the merged blob must fail to encode so OnMerge is never reached, but the hook ran %d times", got)
	}
}

// TestSettleAdvancesOnlyAfterTheHookHasReturned is the ordering assertion the whole
// completion wait rests on: at the moment the hook is entered the merge is already
// PUBLISHED and not yet SETTLED, and it becomes settled only once the hook returns.
// A settle counter incremented beside the publish counter would read equal inside
// the hook and this test is what catches that.
func TestSettleAdvancesOnlyAfterTheHookHasReturned(t *testing.T) {
	var (
		e             *SegmentedIndex[mockQuery, mockStats]
		pubInHook     atomic.Uint64
		settledInHook atomic.Uint64
		hookCalls     atomic.Int64
	)
	e = closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 4,
		OnMerge: func(MergeResult) {
			if hookCalls.Add(1) == 1 {
				pubInHook.Store(e.MergeCount())
				settledInHook.Store(e.SettledMergeCount())
			}
			time.Sleep(50 * time.Millisecond)
		},
	}))
	defer e.Close()

	for i := range 5 {
		if err := e.Add([]Document{doc(ExternalID(rune('a'+i)), "x")}); err != nil {
			t.Fatal(err)
		}
	}
	waitForMerge(t, e)
	waitForSettle(t, e)

	if hookCalls.Load() < 1 {
		t.Fatalf("fixture: the hook must have fired, or the ordering below is vacuous")
	}
	if pub, stl := pubInHook.Load(), settledInHook.Load(); pub != stl+1 {
		t.Fatalf("inside the hook the merge must be published but NOT yet settled: published=%d settled=%d (want published == settled+1)", pub, stl)
	}
}

// TestMergeEligibleTracksTheTriggerPolicy pins the eligibility accessor against both
// triggers and both negatives.
//
// THE MERGER IS STOPPED FIRST, DELIBERATELY. An eligible corpus is exactly the
// corpus the background merger consolidates within one 50ms tick, so on a running
// engine "eligible" is a state that erases itself and cannot be asserted without a
// race. Close joins the merger (see Close's own doc), which freezes the trigger
// state and makes the predicate observable. Add still seals and publishes; only the
// merger that would have consumed the state is gone.
func TestMergeEligibleTracksTheTriggerPolicy(t *testing.T) {
	t.Run("segment count target", func(t *testing.T) {
		e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs:     1,
			DeletesPctAllowed:  2.0, // unreachable: only the count trigger can fire
			SegmentCountTarget: 2,
		}))
		e.Close()

		if e.MergeEligible() {
			t.Fatalf("an empty corpus must not be merge-eligible")
		}
		for i := range 2 {
			if err := e.Add([]Document{doc(ExternalID(rune('a'+i)), "x")}); err != nil {
				t.Fatal(err)
			}
		}
		if e.MergeEligible() {
			t.Fatalf("a corpus AT SegmentCountTarget must not be merge-eligible (the trigger is strictly greater)")
		}
		if err := e.Add([]Document{doc("c", "x")}); err != nil {
			t.Fatal(err)
		}
		if !e.MergeEligible() {
			t.Fatalf("a corpus past SegmentCountTarget must be merge-eligible")
		}
	})

	t.Run("dead ratio", func(t *testing.T) {
		e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs:     4,
			DeletesPctAllowed:  0.33,
			SegmentCountTarget: 1 << 30, // unreachable: only the dead-ratio trigger can fire
		}))
		e.Close()

		if err := e.Add([]Document{doc("a", "x"), doc("b", "x"), doc("c", "x"), doc("d", "x")}); err != nil {
			t.Fatal(err)
		}
		if e.MergeEligible() {
			t.Fatalf("an all-live segment must not be merge-eligible")
		}
		e.Delete("a")
		if e.MergeEligible() {
			t.Fatalf("a segment at 25%% dead must not be merge-eligible below DeletesPctAllowed=0.33")
		}
		e.Delete("b")
		if !e.MergeEligible() {
			t.Fatalf("a segment at 50%% dead must be merge-eligible at DeletesPctAllowed=0.33")
		}
	})

	t.Run("false after the consolidating merge", func(t *testing.T) {
		e := mergeEngine(t, 0.33, 4)
		defer e.Close()
		for i := range 5 {
			if err := e.Add([]Document{doc(ExternalID(rune('a'+i)), "x")}); err != nil {
				t.Fatal(err)
			}
		}
		waitForMerge(t, e)
		waitForSettle(t, e)
		if e.MergeEligible() {
			t.Fatalf("the consolidated all-live segment must leave the corpus with nothing to merge")
		}
	})
}

// errEncodeRefused is the injected failure encodeFailAfterMergeFormat raises.
var errEncodeRefused = errors.New("test: this segment refuses to encode")

// encodeFailAfterMergeFormat is mockFormat whose DECODED segments refuse to Encode.
// mergeEntry reads its merged artifact back through format.Decode (merge_entry.go),
// so the merged ENTRY's payload is one of these and entry.blobParts() therefore
// fails — which is the arm doMerge returns on without firing OnMerge.
//
// ONLY Decode IS OVERRIDDEN, and that is what makes the fixture reach the arm
// rather than short-circuit before it: Build is untouched so the inputs seal
// normally, and MergeTo is untouched so the merged artifact is genuinely written
// and read back. A format that refused to encode everywhere would fail inside
// MergeTo and never publish at all.
type encodeFailAfterMergeFormat struct{ mockFormat }

func (f encodeFailAfterMergeFormat) Decode(blob []byte) (Segment[mockQuery, mockStats], error) {
	seg, err := f.mockFormat.Decode(blob)
	if err != nil {
		return nil, err
	}
	return &encodeFailSegment{Segment: seg}, nil
}

// MergeTo and AggregateStats are overridden ONLY to unwrap. mockFormat reads its
// own concrete segment type — `s.(*mockSegment)` at both of those sites, and at no
// other site in this package (ast census over the package with tests: two hits) —
// so a wrapped segment reaching either of them panics on the type assertion rather
// than exercising anything. Unwrapping first hands mockFormat exactly the segments
// it would have had, which is what keeps this double a change to ONE behaviour.
func (f encodeFailAfterMergeFormat) MergeTo(
	dst MergeSink, segs []Segment[mockQuery, mockStats], accept []func(ExternalID) bool,
) (int64, error) {
	return f.mockFormat.MergeTo(dst, unwrapEncodeFail(segs), accept)
}

func (f encodeFailAfterMergeFormat) AggregateStats(segs []Segment[mockQuery, mockStats]) mockStats {
	return f.mockFormat.AggregateStats(unwrapEncodeFail(segs))
}

func unwrapEncodeFail(segs []Segment[mockQuery, mockStats]) []Segment[mockQuery, mockStats] {
	out := make([]Segment[mockQuery, mockStats], len(segs))
	for i, s := range segs {
		if w, ok := s.(*encodeFailSegment); ok {
			out[i] = w.Segment
			continue
		}
		out[i] = s
	}
	return out
}

// encodeFailSegment is a mock segment that encodes ONCE and refuses afterwards.
//
// THE COUNT IS WHAT AIMS THE FAILURE AT THE ARM UNDER TEST. A merged segment is
// encoded TWICE on its way through doMerge, and only the second encode is the one
// this fixture wants to break:
//
//  1. newEntry (engine.go) encodes it to derive its content-hash id. Failing here
//     aborts mergeEntry, so doMerge returns on its merge-failed arm and NOTHING is
//     ever published — the publish the settle counter is armed at never happens,
//     and the test would be measuring a different arm entirely.
//  2. entry.blobParts() (supersession.go) encodes it to build the MergeResult
//     payload, after the CAS publish and the merge count. THAT is the arm: doMerge
//     returns without firing OnMerge, and the merge must still settle.
//
// The caller's waitForMerge is what keeps this honest: if the failure ever moved
// back to encode 1, no merge publishes and the wait fails naming a MergeCount of 0
// instead of silently testing the wrong thing.
type encodeFailSegment struct {
	Segment[mockQuery, mockStats]
	encodes atomic.Int64
}

func (s *encodeFailSegment) Encode() ([]byte, error) {
	if s.encodes.Add(1) == 1 {
		return s.Segment.Encode()
	}
	return nil, errEncodeRefused
}
