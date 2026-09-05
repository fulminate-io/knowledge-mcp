// SPDX-License-Identifier: Apache-2.0

package bm25

// validate.go — the READER-side structural check on a stored segment, and the
// counterpart of the write-side gate in merge_emit.go.
//
// WHY BOTH SIDES EXIST. mergeEmitter.verifyWithin refuses to PUBLISH a segment
// whose dictionary points past what the merge wrote; it can only speak for
// segments this code produces from here on. Every segment already on disk was
// written before that gate existed, and a build path that is not the merge does
// not pass through it at all. This function is how those are examined: it opens
// a stored payload and walks it the way a query would, so a segment that would
// kill a read announces itself to a census instead.
//
// WHY A FULL WALK RATHER THAN A HEADER CHECK. openSegmentV2 is deliberately
// O(1) — it validates the header and the fixed sections and returns, because
// opening is on the path of every segment the daemon loads. The incident's
// damage was entirely BELOW that: the file opened cleanly, reported a plausible
// document count, and only failed when a term's posting run was resolved. A
// check that does not walk every term cannot see it.

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// FormatName is the segment-family name this package's blobs are stored under.
// Exported so a caller that dispatches a validator by format — the store census
// — names the family from its owner rather than from a duplicated literal.
const FormatName = formatName

// ValidateSegment opens payload and resolves every term in every field,
// returning nil when the segment is structurally consistent with itself.
//
// It reports three distinguishable outcomes:
//
//   - nil — every dictionary entry and posting run resolved inside the payload.
//   - *searchengine.CorruptSegmentError — a dictionary addressed something the
//     payload does not contain. This is the shape that crashed the daemon; the
//     error carries the format's own description of which invariant broke.
//   - any other error — the payload could not be opened as a bm25 segment at
//     all, which is a different and more obvious failure than a segment that
//     opens and then lies about its contents.
//
// The id is stamped onto a corruption error so a caller walking many files can
// name the offending one; pass "" when there is no meaningful id.
//
// It allocates nothing per term: the walk discards each term it resolves, and
// resolving is the whole point — the accessors raise on the way.
func ValidateSegment(id searchengine.SegmentID, payload []byte) (err error) {
	// Deferred LIFO: CatchCorrupt is registered last so it runs FIRST, converting
	// the panic into corrupt; the outer defer then publishes it as the return.
	var corrupt *searchengine.CorruptSegmentError
	defer func() {
		if corrupt != nil {
			err = corrupt
		}
	}()
	defer searchengine.CatchCorrupt(id, &corrupt)

	seg, openErr := openSegmentV2(payload)
	if openErr != nil {
		return fmt.Errorf("bm25: segment %s (%d bytes) does not open: %w", id, len(payload), openErr)
	}
	// THE PER-FIELD DICTIONARIES, which is where the incident lived: every term,
	// its front-coding, and the posting run it addresses.
	for _, mf := range seg.fields {
		mf.eachTerm(func(string, []uint32, []uint16) {})
	}

	// THE MEMBER TABLE, which the dictionaries do not reach. Every id the segment
	// indexes is a (lo,hi) pair into the blob and member() raises on a span that
	// does not fit; the dictionaries address TERMS while this addresses DOCUMENT
	// IDS, so a segment with wrong member offsets passed a dictionary-only
	// validator while IDs() still killed the process. IDs() is what the load path
	// calls, so the class this used to miss is the one that crashes at startup.
	for i := range seg.docCount {
		_ = seg.member(i)
	}

	// THE docFreq DICTIONARY, a third addressed structure again: its rows carry
	// their own term offsets and lengths, resolved through termView, and every
	// scored query reads it through segmentDocFreq. A validator silent about it
	// certifies segments that die on the next search.
	seg.docFreqEach(func(string, int64) {})

	return nil
}
