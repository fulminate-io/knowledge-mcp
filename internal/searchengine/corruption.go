// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"fmt"
	"log/slog"
)

// corruption.go — how a format reports an on-disk invariant it cannot honor,
// and how the engine contains it to the one segment that violated it.
//
// WHY A PANIC AT ALL, RATHER THAN AN ERROR RETURN THROUGHOUT. A format's read
// path is a tree of tiny zero-copy accessors — a term view, a posting run, a
// member name — reached through the hot loop of every query. Threading an error
// return through each of them costs a branch per accessor on the path that must
// stay fastest, and it would let a caller keep the value it was handed on the
// failing branch. These are not recoverable conditions the caller can act on:
// they mean the bytes on disk are not the bytes the format wrote, so there is no
// correct answer to return and no query that should continue against them.
//
// WHY IT MUST NOT REACH THE PROCESS. One corrupt segment used to take the whole
// daemon down on every search or merge that touched it — and because launchd
// restarts the daemon, the crash repeated on every retry, so a single bad file
// in one graph made the entire corpus unserviceable until someone quarantined it
// by hand. Contained here, that same file costs exactly the segments it names
// and nothing else: the rest of the corpus answers, and the owner quarantines
// the file and re-fetches it.
//
// THE CONTAINMENT IS NOT A RECOVER-EVERYTHING. catchCorrupt re-panics anything
// that is not a CorruptSegmentError, so an ordinary nil-dereference or a logic
// bug in a format still crashes loudly and still gets a stack. Swallowing those
// would trade a visible crash for silently wrong search results, which is the
// worse failure on a database.

// CorruptSegmentError reports that a segment's stored bytes violate an invariant
// its own format guarantees — an offset past the blob, a misaligned run, a span
// that does not fit. It is never a query miss and never a recoverable condition:
// segment blobs are content-addressed by the sha256 of their own bytes, so
// reaching one of these means the bytes on disk are not the bytes that were
// hashed.
type CorruptSegmentError struct {
	// ID is the segment the violation was observed in. It is empty at the point
	// the format raises the condition — a format accessor does not know which
	// stored file it is reading — and is stamped by the engine at the boundary
	// that knows.
	ID SegmentID
	// Detail is the format's own description of the invariant it violated.
	Detail string
}

func (e *CorruptSegmentError) Error() string {
	if e.ID == "" {
		return "searchengine: corrupt segment: " + e.Detail
	}
	return "searchengine: corrupt segment " + e.ID + ": " + e.Detail
}

// RaiseCorrupt is how a FORMAT reports a violated on-disk invariant from deep in
// its read path. It panics with a CorruptSegmentError, which the engine's
// per-segment boundaries convert back into an error naming the segment.
//
// A format calls this INSTEAD OF panicking with a string. The type is what makes
// the containment precise: a bare panic is indistinguishable from a genuine bug,
// so recovering it would mean recovering everything.
func RaiseCorrupt(format string, args ...any) {
	panic(&CorruptSegmentError{Detail: fmt.Sprintf(format, args...)})
}

// RaiseCorruptIn is RaiseCorrupt for a format that KNOWS which stored segment it
// is reading, and it exists because the nearest boundary is not always the right
// answer to that question.
//
// USE IT ON ANY PATH A DIFFERENT SEGMENT'S GOROUTINE CAN REACH. The bm25
// document-frequency probe is the case that forced it: a scored query sums
// per-segment frequencies across the whole resident set, so one segment's
// dictionary is read under another segment's containment boundary, and the
// boundary's own id is then the wrong file to withdraw. An attribution made here
// travels with the error and is not overwritten downstream.
func RaiseCorruptIn(id SegmentID, format string, args ...any) {
	panic(&CorruptSegmentError{ID: id, Detail: fmt.Sprintf(format, args...)})
}

// catchCorrupt converts a CorruptSegmentError panic raised beneath it into
// *dst, stamping id, and lets every other panic through untouched.
//
// Used as `defer catchCorrupt(id, &corrupt)` at each boundary that owns one
// segment and can therefore attribute the violation to it.
func catchCorrupt(id SegmentID, dst **CorruptSegmentError) {
	// recover() IS CALLED HERE, IN THE DEFERRED FUNCTION ITSELF, and it cannot be
	// moved into the shared helper below. recover only stops a panic when the
	// DEFERRED function calls it directly; one frame further down it returns nil,
	// the panic keeps unwinding, and the containment silently does nothing. That
	// is not a hypothetical — the exported form below was written as a delegation
	// to this one, and the merge test caught it still crashing.
	handleCorrupt(recover(), id, dst)
}

// handleCorrupt classifies an already-recovered panic value. It takes the value
// rather than calling recover itself for the reason stated above.
func handleCorrupt(r any, id SegmentID, dst **CorruptSegmentError) {
	if r == nil {
		return
	}
	ce, ok := r.(*CorruptSegmentError)
	if !ok {
		// NOT OURS — re-panic with the original value. Containing an unrelated
		// bug here would convert a crash with a stack into a wrong answer.
		panic(r)
	}
	// THE BOUNDARY STAMPS ONLY WHAT NOBODY HAS ATTRIBUTED, and that condition is
	// the difference between quarantining the corrupt segment and quarantining a
	// healthy one. A boundary names the segment it OWNS, which is not always the
	// segment that raised: a scored bm25 query resolves corpus-global document
	// frequency by probing EVERY resident segment from inside whichever
	// segment's goroutine happened to ask (CorpusStats.docFreqOf), so segment A's
	// corruption unwinds under segment B's boundary. Stamping unconditionally
	// there named B, and the owner then quarantined B — a healthy file — while A
	// kept serving and the next query repeated it on the next healthy segment.
	//
	// A format that knows which segment it is reading says so at the raise
	// (RaiseCorruptIn), and that attribution wins over the boundary's guess.
	if ce.ID == "" {
		ce.ID = id
	}
	*dst = ce
}

// CatchCorrupt is the exported form of catchCorrupt for FORMATS that own a
// boundary of their own — a merge that already returns an error can convert the
// condition itself rather than letting it cross into the engine.
//
// The id is left to the caller: a format merging several inputs knows which
// input it was draining, and passing "" is correct when it does not.
func CatchCorrupt(id SegmentID, dst **CorruptSegmentError) {
	// Calls recover() ITSELF rather than delegating to catchCorrupt — see the
	// note there. Delegating compiles, reads correctly, and does nothing.
	handleCorrupt(recover(), id, dst)
}

// containCorrupt runs fn behind a per-segment corruption boundary: a
// CorruptSegmentError raised anywhere beneath it becomes the returned error,
// stamped with id and reported to the owner, while every other panic is left to
// unwind.
//
// WHY A SHARED HELPER RATHER THAN THE PATTERN REPEATED AT EACH SITE. The
// boundary is four lines and two of them are easy to get subtly wrong:
// catchCorrupt must be the DEFERRED FUNCTION ITSELF, because recover() one frame
// further down returns nil and the panic keeps unwinding — this package has
// already shipped that bug once — and the reporting defer must be registered
// FIRST so it runs after the catch has filled the cell. Every path that owns one
// segment's read should reach for this rather than re-derive it.
//
// THE REPORT IS PART OF THE BOUNDARY, not a follow-up the caller may forget. A
// contained corruption nobody is told about leaves the bad segment published and
// the same failure repeating on the next request.
func (e *SegmentedIndex[Q, S]) containCorrupt(id SegmentID, fn func() error) (err error) {
	var corrupt *CorruptSegmentError
	defer func() {
		if corrupt != nil {
			e.reportCorrupt(corrupt)
			err = corrupt
		}
	}()
	defer catchCorrupt(id, &corrupt)
	return fn()
}

// reportCorrupt hands a contained corruption to the engine's owner so it can
// quarantine the stored file and arrange a rebuild. It NEVER re-panics and never
// returns an error: the read that hit this has already been contained, and a
// failure to notify must not turn a survivable query into a crash.
//
// IT WITHDRAWS THE SEGMENT FIRST, and an earlier version of this comment argued
// the opposite — that eviction is a set mutation on the read path and the owner
// should handle it alone. That reasoning was wrong twice over. The mutation is a
// CAS against an immutable snapshot, which is what every other publish path here
// already does and is safe against readers holding their own snapshot. And
// leaving the segment published while its FILE is quarantined produces a state
// worse than either half: the engine still lists the id, so the resident-set
// diff sees a blob the cache no longer has and RE-WRITES the corrupt bytes under
// the same name — undoing the quarantine, and passing the content-address check
// on the way, because this corruption class hashes correctly. The published set
// and the stored file have to leave together.
func (e *SegmentedIndex[Q, S]) reportCorrupt(err *CorruptSegmentError) {
	// AN UNATTRIBUTED CORRUPTION WITHDRAWS NOTHING. An empty id names no segment,
	// so there is nothing to withdraw and nothing a caller could safely resolve it
	// into. The owner is still told, because a corruption nobody could attribute
	// is a fact an operator needs to see.
	if err.ID != "" && !e.WithdrawSegment(err.ID) {
		// A CORRUPTION NAMING A SEGMENT THIS ENGINE DOES NOT PUBLISH is a fact, not
		// a no-op. It means the id the raise carried is not one the published set
		// is keyed on — the damage-in-place case, where a file keeps its filename
		// while its bytes hash to something else — and the owner is about to be
		// handed an id it cannot resolve either. Silently withdrawing nothing here
		// is how that mismatch stayed invisible while the same bytes kept serving.
		// THE MESSAGE IS AMBIGUOUS ON PURPOSE, because this branch genuinely
		// cannot tell its two cases apart. A repeat report — the engine reports a
		// corruption from EVERY concurrent query touching the segment, so the
		// second and later ones arrive after the first has already withdrawn it —
		// is routine and harmless. An id this engine never published is the
		// damage-in-place mismatch and is not. Asserting the second reading
		// false-alarms on the first every time a corrupt segment is touched
		// concurrently, which is the normal case.
		slog.Error("searchengine: corrupt segment is not in the published set "+
			"(already withdrawn by an earlier report, or an id this engine does not publish)",
			"segment", err.ID, "detail", err.Detail)
	}
	if e.opts.OnCorruptSegment == nil {
		return
	}
	e.opts.OnCorruptSegment(err)
}

// WithdrawSegment removes one segment from the published set: its entry leaves
// the snapshot, and the route map and cached stats are rebuilt without it.
// Reports whether the id was resident to begin with.
//
// WHY THE ENGINE OWNS THIS rather than the owner reaching in through some other
// door. The published set is this type's invariant — entries, the id-to-segment
// route and the format's aggregated stats have to agree, and newSegmentSet is
// what makes them agree. A caller that dropped an entry without rebuilding the
// route would leave ids routed to a segment no longer in the set.
//
// WHAT IT MAKES TRUE DOWNSTREAM. Export stops listing the id, so a resident-set
// diff no longer sees the quarantined blob as one it must write back — which is
// what was resurrecting corrupt bytes under their own name. ResidentDocCount and
// DistinctResidentDocCount both fall by what the segment held, which is how the
// loss reaches the coverage arithmetic the heal arms on; a withdrawal the
// accounting could not see would leave the corpus permanently short while every
// gate reported it whole.
//
// IT IS A CAS LOOP against the same snapshot pointer every other publish path
// uses, so a concurrent publish retries rather than clobbering, and a reader
// holding an older snapshot keeps reading a complete set until it lets go.
func (e *SegmentedIndex[Q, S]) WithdrawSegment(id SegmentID) bool {
	for {
		old := e.set.Load()
		if old.entryByID(id) == nil {
			return false
		}
		remaining := make([]*segmentEntry[Q, S], 0, len(old.entries))
		for _, entry := range old.entries {
			if entry.meta.ID != id {
				remaining = append(remaining, entry)
			}
		}
		if e.set.CompareAndSwap(old, newSegmentSet(e.format, remaining)) {
			return true
		}
	}
}
