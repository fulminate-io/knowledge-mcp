// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"runtime"
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

	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 64), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

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
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 8), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)
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
	g := buildBinaryHNSWSerialDeterministic(randomBuildItems(t, 32), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)
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

// goldenEncoderFixture is the graph TestEncodeGraphV3Golden pins. It is built
// through buildBinaryHNSWSerialDeterministic — the SAME constructor Build and
// Merge both use — so the bytes pinned are the bytes production writes.
//
// The node count is chosen to make every section encodeGraphV3 emits
// non-degenerate; the test asserts that rather than trusting it, because a
// golden over a blob with an empty neighbor arena or a single layer would sit
// there looking like a byte pin while covering almost none of the emitter.
//
// 256 IS THE MEASURED FLOOR FOR A MULTI-LAYER GRAPH UNDER THIS SEED, not a
// round number. Level assignment is drawn from the builder's fixed PCG seed and
// is independent of the vectors, so node count is the only lever: at 64 nodes
// this fixture builds maxLevel 0 with exactly one neighbor run per node, which
// would pin a layer-offset array carrying no second layer at all. At 256 it
// builds maxLevel 1 with nine nodes reaching the upper layer.
func goldenEncoderFixture(t *testing.T) *binaryGraph {
	t.Helper()
	return buildBinaryHNSWSerialDeterministic(
		randomBuildItems(t, 256), defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)
}

// TestEncodeGraphV3Golden pins the v3 encoder's output byte-for-byte against a
// checked-in blob captured before the MergeSink restructure.
//
// WHY A NEW FIXTURE RATHER THAN THE TWO ALREADY IN testdata. Both of those are
// DECODE fixtures making claims about bytes already on disk: hnsw_v2_segment.seg
// exists to be REFUSED, and hnsw_v3_ubinary_segment.seg pins how a pre-dtype-tag
// blob reads under the current reader. Neither says anything about what the
// CURRENT encoder emits, which is the only thing an emitter restructure can
// break.
//
// The sixteen existing encodeGraphV3 call sites in this package all encode and
// then DECODE, asserting round-trip behaviour. A round trip is preserved by any
// self-consistent pair of emitter and reader, so it cannot detect a change to
// the stored layout — both halves move together and the test stays green.
func TestEncodeGraphV3Golden(t *testing.T) {
	h := goldenEncoderFixture(t)

	// PRECONDITIONS. Each one names a section that would otherwise be pinned
	// empty, which is the way a golden goes quietly worthless.
	if h.vecBytes == 0 {
		t.Fatal("fixture has vecBytes 0, so the vector block is empty and the golden does not cover it")
	}
	if len(h.nodes) == 0 {
		t.Fatal("fixture has no nodes, so the node directory, id bytes and id directory are all empty")
	}
	if h.maxLevel < 1 {
		t.Fatalf("fixture graph has maxLevel %d — a single-layer graph leaves the layer-offset array degenerate", h.maxLevel)
	}
	runs := 0
	for i := range h.nodes {
		runs += len(h.nodes[i].neighbors)
	}
	if runs <= len(h.nodes) {
		t.Fatalf("fixture has %d neighbor runs over %d nodes — at most one run per node means no node reaches a second layer", runs, len(h.nodes))
	}

	blob, err := encodeGraphV3(h)
	if err != nil {
		t.Fatalf("encodeGraphV3: %v", err)
	}

	want, err := os.ReadFile(filepath.Join("testdata", "hnsw_v3_encoder_golden.seg"))
	if err != nil {
		t.Fatalf("reading the checked-in golden: %v — it must be recovered from history, never regenerated from this tree (see testdata/README.md)", err)
	}
	if !bytes.Equal(blob, want) {
		t.Fatalf("the v3 encoder's bytes moved: wrote %d bytes, golden is %d bytes", len(blob), len(want))
	}
}

// TestEncodeGraphV3ToAllocatesNoOutputBuffer proves the emitter does not
// materialize its output when it writes to a file: allocations stay flat while
// the blob grows several-fold.
//
// THIS MEASURES THE ENCODER, AND NOTHING ELSE. It is NOT a zero-heap claim about
// an hnsw MERGE, and reading it as one gets the format wrong: hnsw's merge
// re-inserts every survivor into a fresh binaryGraph whose vector block is
// output-sized by construction and must be fully resident before a byte can be
// emitted. What this changeset removes from hnsw is the encoder's second
// output-sized buffer, not the insertion structure. The whole-merge allocation
// proof lives in segmentdist as TestMergeAllocatesNoOutputSizedBuffer, and it is
// measured on the BM25 pool, which is the only format where the property holds.
//
// THE INSTRUMENT IS TotalAlloc, NOT A SAMPLED HeapAlloc PEAK, for the reason the
// bm25 writer bound already records: TotalAlloc is cumulative and exact, and a
// peak can never exceed the total, so a bound on the total bounds the peak with
// no sampling window that could miss a spike.
//
// TWO FIXTURES AT THE SAME NODE COUNT, differing only in vector width. That is
// what makes this a statement about the property rather than about a ratio: the
// per-node structures the encoder walks — the node directory, the id bytes, the
// layer offsets, the neighbor arena, the id-directory sort — are identical
// between them, so the blob grows while the work does not. An emitter that still
// buffered its output would track the blob.
func TestEncodeGraphV3ToAllocatesNoOutputBuffer(t *testing.T) {
	narrowAlloc, narrowBlob := measureEncodeToFile(t, encodeFixtureNodes, encodeFixtureNarrowVec)
	t.Logf("narrow vectors: allocated %d bytes writing a %d-byte blob", narrowAlloc, narrowBlob)

	wideAlloc, wideBlob := measureEncodeToFile(t, encodeFixtureNodes, encodeFixtureWideVec)
	t.Logf("wide vectors: allocated %d bytes writing a %d-byte blob", wideAlloc, wideBlob)

	// The vacuity control, asserted before the bound it guards: without it, two
	// fixtures that produced the same blob would satisfy the allocation leg
	// trivially.
	if wideBlob < narrowBlob*2 {
		t.Fatalf("the wide fixture's blob (%d bytes) is not at least twice the narrow one's (%d bytes) — the bound below is vacuous",
			wideBlob, narrowBlob)
	}
	if wideAlloc >= narrowAlloc*2 {
		t.Fatalf("allocations grew with the output (%d then %d bytes, for blobs of %d then %d bytes) — that is what buffering looks like",
			narrowAlloc, wideAlloc, narrowBlob, wideBlob)
	}
}

// measureEncodeToFile encodes one graph into a caller-owned file and reports the
// bytes it allocated and the bytes it wrote.
//
// The file is the point: a sliceSink destination would carry the output in the
// caller's own allocation and the measurement would be about the caller.
func measureEncodeToFile(t *testing.T, nodes, vecBytes int) (allocated, blobSize int64) {
	t.Helper()
	h := buildBinaryHNSWSerialDeterministic(
		sizedBuildItems(t, nodes, vecBytes), vecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

	f, err := os.Create(filepath.Join(t.TempDir(), "encoded.seg")) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("creating the destination: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("closing the destination: %v", err)
		}
	})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := encodeGraphV3To(f, h)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("encodeGraphV3To: %v", err)
	}

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != n {
		t.Fatalf("the emitter reported %d bytes but the file holds %d", n, info.Size())
	}
	if n == 0 {
		t.Fatal("the emitter wrote nothing, so any bound over its output is vacuous")
	}
	return int64(after.TotalAlloc - before.TotalAlloc), n
}

const (
	// encodeFixtureNodes is held EQUAL across both fixtures: they differ only in
	// vector width, so every per-node structure the encoder walks is identical
	// and the blob size is the only thing that moved.
	encodeFixtureNodes = 256
	// encodeFixtureNarrowVec and encodeFixtureWideVec are the two vector widths.
	// The wide one is chosen so the vector block dominates the blob and the size
	// ratio clears the 2x vacuity control.
	encodeFixtureNarrowVec = defaultVecBytes
	encodeFixtureWideVec   = 512
)

// sizedBuildItems builds n deterministic items at a chosen vector width. It is
// randomBuildItems with the width lifted into a parameter, which is what lets
// one fixture grow the output without touching the node count.
func sizedBuildItems(t *testing.T, n, vecBytes int) []binaryBuildItem {
	t.Helper()
	items := make([]binaryBuildItem, n)
	for i := range items {
		vec := make([]byte, vecBytes)
		for b := range vec {
			vec[b] = byte((i*31 + b*7) % 251)
		}
		items[i] = binaryBuildItem{id: idForOrdinal(i), vec: vec}
	}
	return items
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
