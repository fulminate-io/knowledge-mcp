// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"fmt"
	"log/slog"
	"strings"
)

// merge_validate.go — the gate between a finished merge and a published segment.
//
// WHY A GATE HERE AND NOT ONLY IN THE WRITER. A format's own write-side checks can
// only see what the format INTENDED: bm25's merge gate applies the reader's rule to
// the references its emitter recorded, which is the right check and cannot see a
// byte that a buffering layer later overwrote. The merged file is the artifact, and
// the only way to know the artifact is readable is to read it. That is what this
// does, on the bytes that are about to become a segment, in the process that made
// them — rather than leaving the discovery to a reader in another process on another
// day, which is how the incident this exists for was found.
//
// IT IS NOT A FALLBACK AND IT REPAIRS NOTHING. A merge whose output fails here is
// ABANDONED: the scratch file is discarded by mergeEntry's own cleanup, nothing is
// published, and the constituents stay exactly as they were — resident, searchable
// and unmodified. The caller is told, loudly, with the constituents named.

// MergeOutputInvalidError reports that a merge produced a segment its own format
// cannot read, so the output was discarded and the constituents were kept.
//
// IT IS A DISTINCT TYPE FROM CorruptSegmentError ON PURPOSE. That one means a
// STORED segment's bytes are wrong and the disposition is to quarantine the file
// and withdraw the id. This one means bytes this process just produced are wrong and
// there is nothing to quarantine: no segment was published, no id was assigned, and
// the correct disposition is to leave the constituents alone and stop selecting them
// for this merge. A caller that treated the two alike would quarantine a healthy
// constituent for a defect in the merge that read it.
type MergeOutputInvalidError struct {
	// Constituents are the segments the merge consolidated, named so an operator
	// reading the log knows which segments did NOT move.
	Constituents []SegmentID
	// Format is the segment family the merge was writing.
	Format string
	// Bytes is the length of the output that was discarded.
	Bytes int
	// Err is the format validator's own description of what is wrong.
	Err error
}

func (e *MergeOutputInvalidError) Error() string {
	return fmt.Sprintf(
		"searchengine: REFUSING to publish a %s segment this merge just produced: the %d-byte output does not validate (%v). "+
			"The output is discarded and its %d constituents (%s) stay resident and searchable; "+
			"they are unchanged, so nothing needs quarantining",
		e.Format, e.Bytes, e.Err, len(e.Constituents), strings.Join(e.Constituents, ", "))
}

func (e *MergeOutputInvalidError) Unwrap() error { return e.Err }

// runFormatValidation asks the format whether the payload is a readable segment of
// its own kind, and returns what it says — whether the format RETURNED the failure or
// RAISED it.
//
// THE CONVERSION HAPPENS HERE AND THE CLASSIFICATION HAPPENS OUTSIDE, which is the
// whole point of the split. Both shipped validators catch their own raises and return
// a *CorruptSegmentError, but the interface permits either, and a raise converted by
// the engine's REPORTING boundary (containCorrupt) would arrive at the caller as a
// bare stored-corruption error: every corruption arm then matches it and blames a
// healthy CONSTITUENT for a defect in the OUTPUT this process just wrote. Converting
// it here, with the non-reporting catch, keeps the failure a value this file then
// wraps exactly as it wraps a returned one.
//
// IT DOES NOT REPORT ANYTHING. There is nothing to report: no segment was published,
// no id was assigned, and the owner has no file to quarantine.
func (e *SegmentedIndex[Q, S]) runFormatValidation(payload []byte) (err error) {
	var corrupt *CorruptSegmentError
	defer func() {
		if corrupt != nil {
			err = corrupt
		}
	}()
	defer catchCorrupt("", &corrupt)
	return e.format.ValidateSegment("", payload)
}

// refuseMergeOutput turns a validation failure into the typed refusal callers
// dispatch on, and logs it.
//
// THE ERROR IS LOGGED HERE AS WELL AS RETURNED, and the duplication is deliberate.
// The return travels to whichever caller asked for the merge — the background
// merger, a bucket replacement, a harvest — and each of them disposes of it
// differently; the log line is the one record an operator sees whichever caller it
// was, and it is the only place the constituent ids and the discarded length appear
// together.
func (e *SegmentedIndex[Q, S]) refuseMergeOutput(payload []byte, constituents []SegmentID, verr error) error {
	invalid := &MergeOutputInvalidError{
		Constituents: constituents,
		Format:       e.format.Name(),
		Bytes:        len(payload),
		Err:          verr,
	}
	slog.Error("searchengine: MERGE OUTPUT REFUSED — a merge produced an unreadable segment and it was not published",
		"format", invalid.Format,
		"bytes", invalid.Bytes,
		"constituents", constituents,
		"invariant", verr,
		"impact", "no segment was published and no documents were lost: the constituents stay resident and searchable",
		"recovery", "none required for the corpus; the merge is retried on a later trigger and this line names the producer defect to fix")
	return invalid
}
