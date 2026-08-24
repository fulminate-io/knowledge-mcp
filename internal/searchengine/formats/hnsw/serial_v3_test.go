// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"
)

// v3EmptyBlobLen is the size of a serialVersion-3 blob over a zero-node graph:
// the 64-byte header, the single layer-offset sentinel that closes a zero-run
// arena, and the 4-byte footer CRC. Every other section is zero-length at
// nodeCount=0. Written as a derived constant rather than a bare 72 so a layout
// change shows up here as an arithmetic disagreement rather than a mystery.
const v3EmptyBlobLen = v3HeaderSize + 4 + 4

// THESE THREE ARE GUARD CATCHERS, NOT RED-FIRST REPRODUCTIONS, and the label
// matters. They are written against symbols this phase introduces, so there is
// no prior tree in which they could have been observed to fail for the reason
// they exist. Each drives a guard that would otherwise be unwitnessed.

// TestEncodeV3RejectsOversizeBlob drives the u32 size ceiling through the REAL
// encoder.
//
// WHY THE CEILING IS A VAR AND THIS TEST LOWERS IT: a blob that genuinely
// crosses 4 GiB is not constructible in a test — at roughly 367 bytes per node
// it needs on the order of 11 million nodes — so a guard gated only on the
// shipped value is a guard nobody can drive, and nobody would know if it were
// silently unwired. Lowering the seam runs the real check over the real encode
// path. The SHIPPED VALUE IS ASSERTED FIRST, before the lowering, so a ceiling
// left permanently lowered by a careless edit cannot hide behind this test.
func TestEncodeV3RejectsOversizeBlob(t *testing.T) {
	if maxBlobBytes != int64(math.MaxUint32) {
		t.Fatalf("shipped maxBlobBytes = %d, want math.MaxUint32 (%d) — the ceiling has been left lowered",
			maxBlobBytes, int64(math.MaxUint32))
	}
	restore := maxBlobBytes
	t.Cleanup(func() { maxBlobBytes = restore })

	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 64), defaultVecBytes, defaultM, defaultEfConstruction)

	// KNOWN-POSITIVE CONTROL: at the shipped ceiling this same graph encodes
	// cleanly. Without it, an encoder that errored unconditionally would pass the
	// assertion below for entirely the wrong reason.
	if _, err := encodeGraphV3(g); err != nil {
		t.Fatalf("control: the fixture must encode cleanly at the shipped ceiling, got %v", err)
	}

	maxBlobBytes = 128
	_, err := encodeGraphV3(g)
	if err == nil {
		t.Fatal("encodeGraphV3 accepted a blob past the ceiling; the size guard is not wired")
	}
	if !containsAll(err.Error(), "ceiling") {
		t.Fatalf("size-guard error must name the ceiling it tripped, got %q", err)
	}
}

// TestEncodeV3RejectsDuplicateIDs is the ascending-id guard's catcher.
//
// IT REACHES THE CONDITION THROUGH THE PACKAGE-INTERNAL h.nodes SLICE ON
// PURPOSE. Insert dedupes, so duplicate ids are unreachable through any public
// path — which is exactly why the guard exists and exactly why it needs an
// internal poke to witness. A guard whose only driver is an internal poke is
// still a wired guard; a guard nobody can drive at all is not. If the builder
// ever regresses and admits a duplicate, this is the assertion that catches the
// writer handing the reader a binary search that silently answers "not indexed".
func TestEncodeV3RejectsDuplicateIDs(t *testing.T) {
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 8), defaultVecBytes, defaultM, defaultEfConstruction)
	if len(g.nodes) < 2 {
		t.Fatalf("fixture must hold at least two nodes, got %d", len(g.nodes))
	}

	// CONTROL: the untouched graph encodes cleanly, so the failure below is
	// attributable to the duplicate and not to the fixture.
	if _, err := encodeGraphV3(g); err != nil {
		t.Fatalf("control: the untouched fixture must encode cleanly, got %v", err)
	}

	g.nodes[1].externalID = g.nodes[0].externalID
	_, err := encodeGraphV3(g)
	if err == nil {
		t.Fatal("encodeGraphV3 accepted a duplicate id; the ascending-id guard is not wired")
	}
	if !containsAll(err.Error(), "ascending", g.nodes[0].externalID) {
		t.Fatalf("guard error must name the violation and the offending id, got %q", err)
	}
}

// TestEncodeV3WritesAFooterCRC is the WRITER half of the corruption guard. The
// reader half (TestOpenGraphV3RejectsCorruptedFooterCRC) needs openGraphV3 and
// therefore runs at the publish flip.
//
// The CRC exists because diskSegmentCache never re-verifies a blob's content
// hash, so a bit flip INSIDE an already-validated section would otherwise sail
// past every structural check and surface as a panic in a bounds guard or as
// silently wrong neighbors.
func TestEncodeV3WritesAFooterCRC(t *testing.T) {
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 32), defaultVecBytes, defaultM, defaultEfConstruction)
	blob, err := encodeGraphV3(g)
	if err != nil {
		t.Fatalf("encodeGraphV3: %v", err)
	}

	crcOff := int(binary.LittleEndian.Uint32(blob[v3HdrCRC:]))
	if crcOff != len(blob)-4 {
		t.Fatalf("crcOff = %d, want %d (the trailing four bytes)", crcOff, len(blob)-4)
	}
	want := crc32.Checksum(blob[:crcOff], crc32.MakeTable(crc32.Castagnoli))
	got := binary.LittleEndian.Uint32(blob[crcOff:])
	if got != want {
		t.Fatalf("footer CRC = %#08x, want %#08x", got, want)
	}

	// KNOWN-POSITIVE CONTROL: the checksum must actually depend on the bytes it
	// covers. A CRC computed over the wrong span, or a constant, would match
	// above and still be worthless.
	flipped := make([]byte, len(blob))
	copy(flipped, blob)
	flipped[v3HeaderSize]++
	if crc32.Checksum(flipped[:crcOff], crc32.MakeTable(crc32.Castagnoli)) == want {
		t.Fatal("control: flipping a covered byte did not change the checksum")
	}
}

// randomBuildItems builds n deterministic build items. Deterministic on purpose:
// these tests assert on encoder behaviour, and a random fixture would make a
// failure unreproducible.
func randomBuildItems(t *testing.T, n int) []binaryBuildItem {
	t.Helper()
	items := make([]binaryBuildItem, n)
	for i := range items {
		vec := make([]byte, defaultVecBytes)
		for b := range vec {
			vec[b] = byte((i*31 + b*7) % 251)
		}
		items[i] = binaryBuildItem{id: idForOrdinal(i), vec: vec}
	}
	return items
}

// idForOrdinal produces a fixed-width, strictly-ordered id so the fixture's id
// directory is unambiguous.
func idForOrdinal(i int) string {
	const digits = "0123456789"
	return "v3item-" + string([]byte{
		digits[(i/100)%10], digits[(i/10)%10], digits[i%10],
	})
}

// containsAll reports whether s contains every substring.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
