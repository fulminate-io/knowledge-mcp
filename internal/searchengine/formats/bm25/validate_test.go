// SPDX-License-Identifier: Apache-2.0

package bm25

// validate_test.go — what the census validator's walk actually covers.
//
// THE QUESTION THIS ANSWERS. The incident segment reports two DIFFERENT
// violations depending on which path reaches it: the sequential walk raises on
// its posting run ("posting run of 107 at offset 327938…") while an ordinary
// Search raises on its front-coding ("front-coded term shares 32 bytes of a
// 3-byte prefix…"). Two paths, two first-violations on one file, which invites
// the reasonable worry that a validator built on the walk is blind to whatever
// only the search path checks.
//
// IT IS NOT, AND THE REASON IS COVERAGE RATHER THAN LUCK. The two readers are
// twins: dictIter (openBlock / extendPreviousTerm / readEntry) and the search
// path (scanBlock / blockEntry) apply the same four invariants — block index in
// bounds, front-coded header in bounds, shared prefix no longer than the term it
// extends, entry in bounds. What differs is REACH: a search binary-searches to
// ONE block and scans it, while the walk visits every block and every entry in
// the segment, and then resolves each term's posting run on top. The walk's
// bytes are a superset of any single search's, so the file that fails a search
// fails the walk — it just fails at whichever violation comes first in its own
// traversal order.
//
// The test below is that claim made falsifiable: damage ONLY the front-coding of
// a segment whose posting offsets are all intact, and require the walk to raise
// the front-coding violation. If the walk were blind to that class this passes a
// segment an ordinary query would die on, and the census would report a store
// clean that is not.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// buildValidatorFixture seals a real segment from real documents. The corpus is
// deliberately wide enough to fill more than one front-coding block (32 terms per
// block), so the encoding under test is the one Build actually writes.
func buildValidatorFixture(t *testing.T) []byte {
	t.Helper()
	docs := make([]searchengine.Document, 0, 64)
	for i := range 64 {
		docs = append(docs, searchengine.Document{
			ID: fmt.Sprintf("doc-%d", i),
			Fields: map[string]string{
				"name":    fmt.Sprintf("validator fixture %d", i),
				"content": strings.Repeat(fmt.Sprintf("term%d front coding block filler ", i), 8),
			},
		})
	}
	seg, err := bm25Build(docs)
	require.NoError(t, err)
	payload, err := seg.Encode()
	require.NoError(t, err)
	return payload
}

// bm25Build is Format.Build under a name that reads at the call site.
func bm25Build(docs []searchengine.Document) (searchengine.Segment[Query, *CorpusStats], error) {
	return Format{}.Build(docs)
}

// widestField returns the field carrying the most terms — the one whose first
// block is certain to hold a second term to damage — and its INDEX, so the same
// field can be addressed again in the damaged copy of the segment.
func widestField(t *testing.T, seg *mappedSegment) (*mappedField, int) {
	t.Helper()
	best, bestIdx := (*mappedField)(nil), -1
	for i, mf := range seg.fields {
		if best == nil || mf.termCount > best.termCount {
			best, bestIdx = mf, i
		}
	}
	require.NotNil(t, best, "the fixture segment has no fields at all")
	require.Greater(t, best.termCount, 1,
		"the fixture's widest field holds %d term(s); damaging the SECOND term of a block needs at least two", best.termCount)
	return best, bestIdx
}

// termsOf reads a field's terms in order, copying each one — the term a walk
// hands out is valid only for that call.
func termsOf(mf *mappedField) []string {
	var out []string
	mf.eachTerm(func(term string, _ []uint32, _ []uint16) {
		out = append(out, strings.Clone(term))
	})
	return out
}

func TestValidateSegment_WalkCoversFrontCodingDamageNotOnlyPostingOffsets(t *testing.T) {
	payload := buildValidatorFixture(t)

	// CONTROL. A segment the format itself just produced must pass, or a
	// validator that rejected everything would satisfy the assertion below.
	require.NoError(t, ValidateSegment("clean", payload),
		"the format's own output must pass its own reader-rule validator")

	seg, err := openSegmentV2(payload)
	require.NoError(t, err)
	require.Equal(t, dictBlocked, seg.kind,
		"this damage recipe addresses the front-coded encoding; a segment written in another dictionary kind would be damaged somewhere meaningless")

	mf, fieldIdx := widestField(t, seg)
	terms := termsOf(mf)
	require.Greater(t, len(terms), 1, "the walk must yield the terms this test looks up on the damaged copy")

	// The block index holds one uint32 per block. Block 0's offset addresses its
	// first term's ENTRY (postOff, count — 8 bytes); the block's SECOND term
	// follows as [shared uint16][suffixLen uint16][suffix...], so the shared
	// count sits 8 bytes into the block.
	blockOff := int(binary.LittleEndian.Uint32(payload[mf.blockIdxOff:]))
	sharedAt := blockOff + 8
	require.Less(t, sharedAt+2, len(payload), "the computed damage site must lie inside the payload")

	damaged := slices.Clone(payload)
	binary.LittleEndian.PutUint16(damaged[sharedAt:], 0xFFFF)

	// NOTHING ELSE MOVED. Only two bytes differ, and they are the shared-prefix
	// count — every posting offset and run length in the segment is still exactly
	// what the encoder wrote. So a raise here is attributable to the front-coding
	// check and to nothing else.
	require.Len(t, damaged, len(payload))
	differing := 0
	for i := range payload {
		if payload[i] != damaged[i] {
			differing++
		}
	}
	require.LessOrEqual(t, differing, 2, "the recipe must damage the shared-prefix count alone")

	err = ValidateSegment("damaged", damaged)
	var ce *searchengine.CorruptSegmentError
	require.ErrorAs(t, err, &ce,
		"a segment whose front-coding claims a 65535-byte shared prefix must be reported as corrupt by the walk the census runs; "+
			"if it is not, the census reads a store clean that an ordinary search would die on")
	require.Contains(t, ce.Detail, "front-coded term shares",
		"the walk must raise the FRONT-CODING invariant here, not some downstream consequence of it")
	// THE ID IS THE SEGMENT'S OWN CONTENT ADDRESS, NOT THE LABEL PASSED IN, and
	// that is a deliberate strengthening rather than a drift. Raises now carry the
	// id of the segment that made them (RaiseCorruptIn), and an attribution made
	// at the raise wins over the caller's — which is what stops a corruption
	// surfacing under another segment's boundary from naming the wrong file. Here
	// the walk is single-segment, so the two would agree in meaning; asserting the
	// TRUE address is the stronger check, because it is the name the census and
	// the quarantine both key on.
	damagedSum := sha256.Sum256(damaged)
	require.Equal(t, hex.EncodeToString(damagedSum[:]), ce.ID,
		"the corruption must name the segment's own content address, which is what a census or a quarantine resolves")

	// THE TWIN, on the same bytes. The search path reaches this block by binary
	// search rather than by walking to it, and it must reach the same verdict —
	// that is the equivalence the header claims, and asserting it here is what
	// would notice if the two readers' checks ever drifted apart on this shape.
	damagedSeg, err := openSegmentV2(damaged)
	require.NoError(t, err, "the damage is below the header, so the segment still opens")

	var searchRaised *searchengine.CorruptSegmentError
	func() {
		defer searchengine.CatchCorrupt("damaged", &searchRaised)
		//nolint:errcheck // the return is irrelevant: the raise is what is under test.
		damagedSeg.fields[fieldIdx].lookup(terms[1])
	}()
	require.NotNil(t, searchRaised,
		"an ordinary lookup of the damaged block's second term must raise too; if only the walk raises, the two readers no longer agree and one of them is serving a term it cannot reconstruct")
	require.Contains(t, searchRaised.Detail, "front-coded term shares",
		"the search path must raise the same front-coding invariant the walk did")
}
