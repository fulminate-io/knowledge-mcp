// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"bytes"
	"testing"
)

// TestVectorByIDPresentAndAbsent builds an hnswSegment from known (id,vector)
// docs and asserts VectorByID(presentID) returns the inserted vector byte-equal
// (the idMap lookup + nodeVector offset read), while VectorByID(absentID) returns
// (nil,false). Fails-when-absent: without the idMap lookup the present-id case
// returns wrong/empty bytes or false; without the (nil,false) guard the absent
// case would read a wrong vector or panic.
func TestVectorByIDPresentAndAbsent(t *testing.T) {
	docs := vecDocs(64)
	seg, _, err := Format{}.Build(docs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	hs, ok := seg.(*hnswSegment)
	if !ok {
		t.Fatalf("Build returned %T, want *hnswSegment", seg)
	}

	for _, d := range docs {
		got, ok := hs.VectorByID(d.ID)
		if !ok {
			t.Fatalf("VectorByID(%s) ok=false, want true for a present id", d.ID)
		}
		if !bytes.Equal(got, d.Vector) {
			t.Fatalf("VectorByID(%s) = %x, want stored vector %x", d.ID, got, d.Vector)
		}
	}

	if got, ok := hs.VectorByID("no-such-id"); ok || got != nil {
		t.Fatalf("VectorByID(absent) = (%v, %v), want (nil, false)", got, ok)
	}
}

// TestVectorByIDSurvivesEncodeDecode asserts the by-id stored-vector read still
// resolves byte-equal after an Encode→Decode round trip (inline vectors survive
// the v2 blob), since a decoded segment is the shipped/persisted shape that the
// similar-search resolve actually reads from in practice.
func TestVectorByIDSurvivesEncodeDecode(t *testing.T) {
	docs := vecDocs(32)
	seg, _, err := Format{}.Build(docs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	blob, err := seg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Format{}.Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	hs, ok := decoded.(*hnswSegment)
	if !ok {
		t.Fatalf("Decode returned %T, want *hnswSegment", decoded)
	}
	for _, d := range docs {
		got, ok := hs.VectorByID(d.ID)
		if !ok || !bytes.Equal(got, d.Vector) {
			t.Fatalf("after round trip VectorByID(%s) = (%x, %v), want (%x, true)", d.ID, got, ok, d.Vector)
		}
	}
}
