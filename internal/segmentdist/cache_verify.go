// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// cache_verify.go — the write-time content-address invariant.
//
// WHAT IT GUARDS. This store is content-addressed: a segment lives at
// <sha256-of-its-payload>.seg, and every reader trusts that name. The id is
// CALLER-SUPPLIED and was never checked against the bytes being written, so a
// caller that hashed a buffer and then wrote it after it had changed — a buffer
// still being mutated when the hash was taken — would store bytes under a name
// that did not describe them, and nothing would notice until a reader far away
// tried to use them.
//
// IT DID NOT CAUSE THE INCIDENT THAT PROMPTED IT, AND SAYING SO IS THE POINT.
// The two preserved corrupt segments were first read as hash-vs-name mismatches
// and taken as evidence of exactly the defect above. They are not. That reading
// hashed the WHOLE STORED FILE; the id names the PAYLOAD, and both files hash
// correctly on the payload:
//
//	fbc34f9566…seg   whole 64891B → aa8f6a09a9f8…   payload 64739B → fbc34f9566…  ✓
//	955f207c2f…seg   whole 121382B → 883ae305eea2…  payload 121230B → 955f207c2f… ✓
//
// Their content addressing is INTACT. The damage in those files is INTERNAL to a
// payload that hashes exactly as it should — a dictionary addressing a posting
// run at offset 327938 inside a 64739-byte payload — so the producer hashed
// precisely the bytes it wrote, and no amount of write-time hashing can see it.
// This check would have passed both files.
//
// SO IT IS DEFENSE IN DEPTH FOR A DIFFERENT, UNOBSERVED CLASS, and it is kept on
// that footing rather than on a claim it cannot support. The invariant is cheap,
// it is the one the entire store rests on, and nothing else asserts it.
//
// WHY IT REFUSES RATHER THAN REPAIRS. Writing the bytes under their TRUE hash
// would silently paper over a producer defect and leave the caller's in-memory
// set pointing at an id that was never written. Refusing turns a silent
// corruption into a loud error AT ITS SOURCE, in the goroutine that holds the
// stale buffer — the only place with enough context to say why.

// verifyContentAddress reports whether parts hash to id, which is the invariant
// the whole store rests on.
//
// THE ID NAMES THE PAYLOAD, NOT THE STORED FILE, and the two differ whenever a
// segment carries a supersession envelope — the stored file is envelope followed
// by payload while the id is the sha256 of the payload alone. Both of Put's
// shapes are handled here rather than at the call sites:
//
//   - TWO PARTS is (envelope, payload) from a producer that has them separately;
//     the payload is parts[1] and the envelope is excluded from the hash.
//   - ONE PART is a whole stored file, from a cache-to-cache copy that read it
//     back with Get; the envelope has to be parsed off before hashing.
//
// Any other arity is refused by name rather than guessed at: a three-part call
// would be a new shape whose payload boundary this function cannot know, and
// hashing the wrong span would either reject good writes or admit bad ones.
func verifyContentAddress(id searchengine.SegmentID, parts ...[]byte) error {
	var payload []byte
	switch len(parts) {
	case 1:
		_, p, err := searchengine.SplitStoredBlob(parts[0])
		if err != nil {
			return fmt.Errorf("segmentdist: segment %s: cannot split stored blob to verify its content address: %w", id, err)
		}
		payload = p
	case 2:
		payload = parts[1]
	default:
		return fmt.Errorf(
			"segmentdist: segment %s: cannot verify content address of a %d-part write (expected 1 whole stored file, or 2 as envelope+payload)",
			id, len(parts))
	}

	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if got == id {
		return nil
	}
	return fmt.Errorf(
		"segmentdist: REFUSING to write segment %s: its %d-byte payload hashes to %s, so these bytes are not the bytes that were hashed to name them. "+
			"The store is content-addressed and every reader trusts the filename; writing this would put unreadable bytes under a name another segment's readers rely on. "+
			"The producer hashed a buffer and then changed it before the write",
		id, len(payload), got)
}
