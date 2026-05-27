// SPDX-License-Identifier: Apache-2.0

package logs

import "testing"

func TestStreamIDMap_Empty(t *testing.T) {
	m := NewStreamIDMap()
	if m.Len() != 0 {
		t.Fatalf("expected len 0, got %d", m.Len())
	}
	if s := m.Resolve(0); s != "" {
		t.Fatalf("expected empty string for Resolve(0) on empty map, got %q", s)
	}
	if _, ok := m.Get("nonexistent"); ok {
		t.Fatal("expected Get on empty map to return false")
	}
}

func TestStreamIDMap_SequentialAssignment(t *testing.T) {
	m := NewStreamIDMap()
	ids := []string{"aaa", "bbb", "ccc", "ddd"}
	for i, id := range ids {
		uid := m.Add(id)
		if uid != uint32(i) {
			t.Fatalf("Add(%q) = %d, want %d", id, uid, i)
		}
	}
	if m.Len() != len(ids) {
		t.Fatalf("Len() = %d, want %d", m.Len(), len(ids))
	}
}

func TestStreamIDMap_RoundTrip(t *testing.T) {
	m := NewStreamIDMap()
	ids := []string{"stream-alpha", "stream-beta", "stream-gamma"}
	for _, id := range ids {
		m.Add(id)
	}
	for _, id := range ids {
		uid, ok := m.Get(id)
		if !ok {
			t.Fatalf("Get(%q) returned false after Add", id)
		}
		resolved := m.Resolve(uid)
		if resolved != id {
			t.Fatalf("Resolve(%d) = %q, want %q", uid, resolved, id)
		}
	}
}

func TestStreamIDMap_DuplicateAdd(t *testing.T) {
	m := NewStreamIDMap()
	first := m.Add("dup")
	second := m.Add("dup")
	if first != second {
		t.Fatalf("duplicate Add returned different IDs: %d vs %d", first, second)
	}
	if m.Len() != 1 {
		t.Fatalf("Len() = %d after duplicate Add, want 1", m.Len())
	}
}

func TestStreamIDMap_UnknownGet(t *testing.T) {
	m := NewStreamIDMap()
	m.Add("known")
	if _, ok := m.Get("unknown"); ok {
		t.Fatal("Get returned true for unknown ID")
	}
}

func TestStreamIDMap_OutOfRangeResolve(t *testing.T) {
	m := NewStreamIDMap()
	m.Add("only-one")
	if s := m.Resolve(1); s != "" {
		t.Fatalf("Resolve(1) = %q, want empty for out-of-range", s)
	}
	if s := m.Resolve(999); s != "" {
		t.Fatalf("Resolve(999) = %q, want empty for out-of-range", s)
	}
}
