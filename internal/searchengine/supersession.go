package searchengine

import (
	"encoding/binary"
	"fmt"
	"slices"
)

// supersession.go carries the DURABLE SUPERSESSION RECORD: what a published segment
// replaced, stored in the blob itself.
//
// WHY IT HAS TO BE IN THE BLOB. The stored corpus used to record a content-hash id and
// an encoded payload and NOTHING ELSE — not even the generation survived a reload — so
// nothing on disk said "superseded by". A restarted process therefore cannot tell a live
// segment from one a consolidation replaced, and an import of the stored set brings both
// back: the consolidated blob AND the constituents it consumed, with whatever documents
// those constituents still carry. External state cannot close that, because the state a
// cold start has IS the stored corpus.
//
// WHY AN ENVELOPE RATHER THAN A FORMAT VERSION BUMP. The record is format-agnostic —
// both shipped formats need it and neither wants to know about it — and the engine owns
// the choke points where a blob becomes a segment and a segment becomes a blob. A
// per-format bump would express one fact in two layouts, and would make every new blob
// unreadable to an already-released binary over a property that has nothing to do with
// how an index is laid out.
//
// THE VERSIONING DIRECTION IS THE FORMATS' OWN, inherited rather than invented. Both put
// a small version integer at byte 0 (bm25 v2 = 2, hnsw = 3 for ubinary and 4 for
// float32) and both REFUSE a version they do not recognize, loudly and with a remedy —
// hnsw's serial.go argues that refusal at length as the reason its float32 flavor took a
// whole version number instead of riding a header tag. So the supported direction is: a
// NEW reader accepts OLD bytes; an OLD reader refuses NEW bytes rather than misreading
// them. This envelope satisfies both halves — a blob with no envelope decodes
// byte-identically, and the magic's leading 0x00 is a version integer neither format
// accepts, so an old binary handed an enveloped blob fails its own version check instead
// of reading a header as an index.
//
// THE ID IS THE HASH OF THE PAYLOAD, NOT OF THE ENVELOPE, and that is deliberate. A
// segment id names the INDEX it identifies; two encodings of the same index are the same
// segment however they were arrived at. Hashing the record too would make a re-emit that
// changed nothing produce a fresh id on every pass — the partition would be rewritten and
// its predecessor reclaimed forever, churn with no content behind it.

// supMagic is the envelope's leading marker. The first byte is 0x00 DELIBERATELY: byte 0
// of every format payload is that format's own version integer and neither accepts 0, so
// this marker can never be mistaken for a payload nor a payload for this marker. The
// trailing byte is the ENVELOPE's own version.
var supMagic = [supMagicLen]byte{0x00, 'S', 'E', 'G', 'S', 'U', 'P', 0x01}

const (
	// supMagicLen is the marker width; supHeaderLen adds the payload offset and the
	// two counts that follow it.
	supMagicLen  = 8
	supHeaderLen = supMagicLen + 12
	// supPayloadAlign is what every envelope's length is rounded up to, and it is a
	// CORRECTNESS requirement rather than tidiness. hnsw's mapped reader validates a
	// section's BLOB-RELATIVE offset against the alignment its typed view needs and
	// then casts at &blob[off] — the ABSOLUTE address is base+off, so a payload that
	// began at an odd offset would satisfy that check and still produce a misaligned
	// float32 view. Rounding to 8 keeps every payload-internal offset as aligned in
	// memory as it is in the file.
	supPayloadAlign = 8
)

// supersessionRecord is what a published blob says about the swap that produced it.
//
// THE COHORT IS THE HALF THAT MAKES DECLINING SAFE, and without it this record would be
// a data-loss instrument. A consolidation publishes its outputs as a SET — a group swap
// harvests several partitions at once, a layer swap replaces the whole corpus — and it is
// the set, not any single blob, that carries the superseded constituents' members
// forward. A reader that dropped Superseded on the strength of one output could therefore
// retire segments whose documents live in a sibling output that never reached disk. So a
// record names every id published in its own swap, and a reader honors it only when all
// of them are present.
type supersessionRecord struct {
	// Superseded is what this swap replaced: exactly the set its owner is told to
	// reclaim, so the durable record and the reclaim cannot disagree about which
	// blobs the swap made redundant.
	Superseded []SegmentID
	// Cohort is every id the same swap published, this blob included.
	Cohort []SegmentID
}

// empty reports a record that says nothing and is therefore not written at all.
func (r supersessionRecord) empty() bool { return len(r.Superseded) == 0 }

// encodeSupersessionPrefix returns the ENVELOPE ALONE for rec — the bytes that
// precede a payload in a stored blob, and nothing of the payload itself. An empty
// record returns nil, which is what keeps every non-consolidating blob
// byte-identical to what this engine wrote before the record existed.
//
// IT RETURNS A PREFIX RATHER THAN A CONCATENATION, and that is the whole change.
// The previous form allocated payloadOff+len(payload) and copied the payload into
// it, so every consolidation paid a second full copy of the merged segment on the
// heap — on the same path that had just produced the segment. A prefix is tens of
// bytes to a few kilobytes and does not scale with the corpus; the payload is
// written after it, never through it.
//
// THE BYTES ARE UNCHANGED. What it emits is exactly the first payloadOff bytes
// the concatenating form emitted: the same magic, the same three u32 header
// fields, the same id block, the same padding to supPayloadAlign.
//
// IT SORTS BOTH LISTS so the bytes depend on the SETS rather than on the order a
// caller happened to collect them in — two runs of the same swap write the same
// file.
func encodeSupersessionPrefix(rec supersessionRecord) []byte {
	if rec.empty() {
		return nil
	}
	superseded := sortedUnique(rec.Superseded)
	cohort := sortedUnique(rec.Cohort)

	body := supHeaderLen
	for _, id := range superseded {
		body += 2 + len(id)
	}
	for _, id := range cohort {
		body += 2 + len(id)
	}
	payloadOff := (body + supPayloadAlign - 1) &^ (supPayloadAlign - 1)

	out := make([]byte, payloadOff)
	copy(out, supMagic[:])
	binary.LittleEndian.PutUint32(out[supMagicLen:], uint32(payloadOff))
	binary.LittleEndian.PutUint32(out[supMagicLen+4:], uint32(len(superseded)))
	binary.LittleEndian.PutUint32(out[supMagicLen+8:], uint32(len(cohort)))
	cur := supHeaderLen
	for _, list := range [][]SegmentID{superseded, cohort} {
		for _, id := range list {
			binary.LittleEndian.PutUint16(out[cur:], uint16(len(id)))
			cur += 2
			cur += copy(out[cur:], id)
		}
	}
	return out
}

// splitStoredBlob splits stored bytes into their envelope and their format
// payload. A blob with no envelope returns a nil envelope and the SAME slice.
//
// BOTH HALVES ARE ZERO-COPY SUBSLICES of the input, which is what lets a mapped
// blob stay a mapping on both sides of the split rather than becoming a heap copy
// the moment anything asks where the payload starts.
//
// A DAMAGED ENVELOPE IS AN ERROR, never a passthrough, for the reason
// decodeSupersession records: handing a format an envelope header to decode as an
// index is a corrupt read dressed up as a compatible one.
func splitStoredBlob(blob []byte) (envelope, payload []byte, err error) {
	off, err := supersessionPayloadOff(blob)
	if err != nil {
		return nil, nil, err
	}
	if off == 0 {
		return nil, blob, nil
	}
	return blob[:off], blob[off:], nil
}

// supersessionPayloadOff reports where the format payload begins in a stored
// blob: 0 when there is no envelope at all.
//
// IT IS THE ONE HEADER PARSE, shared by splitStoredBlob and decodeSupersession.
// A second parser beside the first is two readings of the same bytes that must
// agree forever, and the way that fails is silent: the two would disagree only on
// a malformed blob, which is exactly the case nobody exercises.
func supersessionPayloadOff(blob []byte) (int, error) {
	if len(blob) < supMagicLen || [supMagicLen]byte(blob[:supMagicLen]) != supMagic {
		return 0, nil
	}
	if len(blob) < supHeaderLen {
		return 0, fmt.Errorf(
			"searchengine: supersession envelope is %d bytes, shorter than its %d-byte header",
			len(blob), supHeaderLen)
	}
	off := int(binary.LittleEndian.Uint32(blob[supMagicLen:]))
	if off < supHeaderLen || off > len(blob) {
		return 0, fmt.Errorf(
			"searchengine: supersession envelope names a payload at offset %d, outside a %d-byte blob",
			off, len(blob))
	}
	return off, nil
}

// decodeSupersession splits a stored blob into its record and the format payload beneath
// it. A blob with no envelope returns an empty record and the SAME slice, so a mapped
// blob stays a mapping and a record-less blob reaches the format exactly as it was
// stored.
//
// A DAMAGED ENVELOPE IS AN ERROR, NEVER A PASSTHROUGH. Handing the format an envelope
// header to decode as an index is a corrupt read dressed up as a compatible one —
// precisely the silent mis-decode the formats' version bytes exist to prevent.
func decodeSupersession(blob []byte) (supersessionRecord, []byte, error) {
	var rec supersessionRecord
	payloadOff, err := supersessionPayloadOff(blob)
	if err != nil {
		return rec, nil, err
	}
	if payloadOff == 0 {
		return rec, blob, nil
	}
	supersededCount := int(binary.LittleEndian.Uint32(blob[supMagicLen+4:]))
	cohortCount := int(binary.LittleEndian.Uint32(blob[supMagicLen+8:]))
	cur := supHeaderLen
	if rec.Superseded, cur, err = readIDs(blob, cur, payloadOff, supersededCount); err != nil {
		return supersessionRecord{}, nil, err
	}
	if rec.Cohort, _, err = readIDs(blob, cur, payloadOff, cohortCount); err != nil {
		return supersessionRecord{}, nil, err
	}
	return rec, blob[payloadOff:], nil
}

// decodeSupersessionEnvelope reads the record out of an envelope that has ALREADY
// been separated from its payload. A nil or empty envelope is an empty record and
// not an error — that is what a blob recording no supersession looks like.
//
// IT IS THE SAME PARSE, not a second one. An envelope's own length IS its
// payloadOff, so decodeSupersession over the envelope alone reads exactly the same
// header and the same id block and hands back an empty payload. Keeping this a
// wrapper rather than a parallel reader is the same rule supersessionPayloadOff
// exists for: one parse of this layout, so two readings cannot drift apart on a
// malformed blob nobody exercises.
func decodeSupersessionEnvelope(envelope []byte) (supersessionRecord, error) {
	rec, _, err := decodeSupersession(envelope)
	return rec, err
}

// readIDs reads count length-prefixed ids from blob starting at cur, refusing anything
// that would read past the id block's end.
func readIDs(blob []byte, cur, end, count int) ([]SegmentID, int, error) {
	ids := make([]SegmentID, 0, count)
	for range count {
		if cur+2 > end {
			return nil, 0, fmt.Errorf(
				"searchengine: supersession envelope claims %d ids but its id block ends at %d",
				count, end)
		}
		n := int(binary.LittleEndian.Uint16(blob[cur:]))
		cur += 2
		if cur+n > end {
			return nil, 0, fmt.Errorf(
				"searchengine: supersession envelope's id of %d bytes overruns its id block at %d",
				n, cur)
		}
		ids = append(ids, SegmentID(blob[cur:cur+n]))
		cur += n
	}
	return ids, cur, nil
}

// SupersededBy reports what a stored blob records itself as superseding, and which ids
// were published alongside it, without decoding the index beneath.
//
// It is exported for the distribution layer, which owns questions the engine cannot
// answer for it — which mappings it may release, and which stored files a converged pool
// no longer needs.
//
// IT ACCEPTS EITHER THE WHOLE STORED BLOB OR THE ENVELOPE ALONE, and needs no flag
// to tell them apart: an envelope's own length IS the payload offset its header
// names, so the parse terminates identically either way. A caller holding a blob
// read back from disk passes the whole thing; a caller holding a SegmentBlob passes
// its Envelope, because that struct's Bytes is the payload alone.
func SupersededBy(blob []byte) (superseded, cohort []SegmentID, err error) {
	rec, _, err := decodeSupersession(blob)
	return rec.Superseded, rec.Cohort, err
}

// SplitStoredBlob splits stored bytes into their supersession envelope and their
// format payload, as ZERO-COPY SUBSLICES of the input.
//
// It is the exported face of the same split the engine performs internally, for
// the distribution layer, which reads whole stored files off disk and has to fill
// a SegmentBlob's two fields without copying either half off the mapping. Like
// SupersededBy and SegmentPayload, it is a thin export over the ONE parse of this
// layout rather than a second reader of it.
//
// A blob with no envelope returns a nil envelope and the SAME slice, so a mapping
// stays a mapping and a record-less blob is passed through byte-identically.
func SplitStoredBlob(blob []byte) (envelope, payload []byte, err error) {
	return splitStoredBlob(blob)
}

// SegmentPayload returns the format payload beneath a stored blob's optional
// supersession envelope — the exact bytes Import hands to the format's Decode, and
// the COMPLEMENT of SupersededBy, which reads the record and discards the payload.
//
// It exists because those two halves are separable only in here. The layout
// readers are unexported, so a caller outside this package that wanted the index
// beneath an enveloped blob had to re-derive the payload offset itself — a second
// implementation of this layout, in a package that does not own it, which would
// keep reading old bytes correctly right up until the envelope changed.
//
// A blob with no envelope returns the SAME slice, so a mapping stays a mapping and a
// record-less blob is passed through byte-identically. A damaged envelope is an
// error here exactly as it is on the import path.
func SegmentPayload(blob []byte) ([]byte, error) {
	_, payload, err := splitStoredBlob(blob)
	return payload, err
}

// sortedUnique returns the ids in a stable, duplicate-free order.
func sortedUnique(ids []SegmentID) []SegmentID {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// sortedSegmentIDs flattens a supersession set into a stable slice. The ORDER matters
// twice: it reaches the encoder, whose output is a stored file, and it reaches the owner
// through MergeResult.Removed, where a map's iteration order would make two identical
// merges reclaim in different orders.
func sortedSegmentIDs(set map[SegmentID]bool) []SegmentID {
	out := make([]SegmentID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// stampSupersession records on entry what its swap replaced and what that swap published
// alongside it.
//
// IT MUST BE CALLED BEFORE THE ENTRY IS PUBLISHED. A segmentSet is an immutable snapshot
// once swapped in, so writing to a published entry would mutate a snapshot readers may
// already be walking — the one documented exception is liveDocs, and this is not it.
//
// A record naming nothing is not written, so an ordinary seal stays record-less and its
// blob stays byte-identical to what this engine wrote before records existed.
func stampSupersession[Q, S any](entry *segmentEntry[Q, S], superseded, cohort []SegmentID) {
	if entry == nil || len(superseded) == 0 {
		return
	}
	entry.record = supersessionRecord{Superseded: superseded, Cohort: cohort}
}

// blobParts is how a resident entry becomes stored bytes: its supersession
// envelope and its format payload, SEPARATELY. The stored file is the two
// concatenated, and every writer writes them in that order.
//
// EVERY SITE THAT SHIPS AN ENTRY'S BYTES GOES THROUGH HERE. A site that encoded
// the payload directly would write a file that says nothing about what it
// replaced, and the record would be present or absent depending on which code
// path happened to store the segment — which is indistinguishable, on disk, from
// a corpus that never had one.
//
// IT RETURNS TWO SLICES RATHER THAN ONE, and that is the point rather than a
// convenience. Concatenating here allocated a buffer the size of the whole
// segment and copied the payload into it, on every produced blob — Export pays
// that per resident segment, and a consolidation pays it having just produced the
// payload. Handing back both parts lets the writer place them in sequence and
// lets the payload stay whatever it already was, mapping included.
//
// The envelope is nil for an entry with no record, so a non-consolidating blob's
// stored bytes are its payload alone, byte-identical to what this engine wrote
// before records existed.
func (entry *segmentEntry[Q, S]) blobParts() (envelope, payload []byte, err error) {
	payload, err = entry.payload.Encode()
	if err != nil {
		return nil, nil, err
	}
	return encodeSupersessionPrefix(entry.record), payload, nil
}
