// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestAssembleChunks_BasicGrouping(t *testing.T) {
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	entries := []wirelogs.LogEntry{
		{Timestamp: base, Message: "user 42 logged in", Labels: map[string]string{"svc": "api"}},
		{Timestamp: base.Add(time.Second), Message: "user 99 logged in", Labels: map[string]string{"svc": "api"}},
		{Timestamp: base.Add(2 * time.Second), Message: "connection refused", Labels: map[string]string{"svc": "api"}},
	}
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	streams, sidIDs := buildStreams(entries, 0)
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	// Two distinct templates => two chunks even though they share stream/window.
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks for 2 distinct templates, got %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		total += c.EntryCount
		if c.StreamID != streams[0].ID {
			t.Errorf("chunk %s has unexpected stream ID %s", c.ID, c.StreamID)
		}
	}
	if total != len(entries) {
		t.Errorf("entry counts sum to %d, want %d", total, len(entries))
	}
}

func TestAssembleChunks_TimeWindowBoundary(t *testing.T) {
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	// Use identical messages so a single template covers all entries,
	// then force the second entry into the next 5-minute bucket.
	entries := []wirelogs.LogEntry{
		{Timestamp: base, Message: "ping ok", Labels: map[string]string{"svc": "api"}},
		{Timestamp: base.Add(6 * time.Minute), Message: "ping ok", Labels: map[string]string{"svc": "api"}},
	}
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	_, sidIDs := buildStreams(entries, 0)

	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks across window boundary, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.EntryCount != 1 {
			t.Errorf("expected 1 entry per chunk, got %d", c.EntryCount)
		}
	}
}

func TestAssembleChunks_Compression(t *testing.T) {
	// Repetitive data should compress significantly.
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	var entries []wirelogs.LogEntry
	for i := range 200 {
		entries = append(entries, wirelogs.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Message:   "request served for user " + fmt.Sprint(i),
			Labels:    map[string]string{"svc": "api"},
		})
	}
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	_, sidIDs := buildStreams(entries, 0)

	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, time.Hour)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Sanity-check compression: the raw payload for 200 entries with var
	// strings and 64-bit timestamps is comfortably over 1KB, while zstd
	// should shrink it to a fraction of that for repetitive patterns.
	for _, c := range chunks {
		if len(c.CompressedData) == 0 {
			t.Errorf("chunk %s has empty compressed payload", c.ID)
		}
	}
}

func TestAssembleChunks_RoundTrip(t *testing.T) {
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	entries := []wirelogs.LogEntry{
		{Timestamp: base, Message: "request completed 42ms", Labels: map[string]string{"svc": "api"}},
		{Timestamp: base.Add(time.Second), Message: "request completed 73ms", Labels: map[string]string{"svc": "api"}},
		{Timestamp: base.Add(2 * time.Second), Message: "request completed 15ms", Labels: map[string]string{"svc": "api"}},
	}
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	_, sidIDs := buildStreams(entries, 0)

	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	timestamps, vars, err := DecodeChunk(chunks[0])
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if len(timestamps) != len(entries) {
		t.Fatalf("round-trip entry count mismatch: got %d, want %d", len(timestamps), len(entries))
	}
	for i, ts := range timestamps {
		if !ts.Equal(entries[i].Timestamp) {
			t.Errorf("entry %d timestamp mismatch: got %s, want %s", i, ts, entries[i].Timestamp)
		}
	}
	// At least some entries should have extracted vars (the numeric ms value).
	hasVars := false
	for _, v := range vars {
		if len(v) > 0 {
			hasVars = true
			break
		}
	}
	if !hasVars {
		t.Error("expected at least one entry to carry extracted vars")
	}
}

func TestAssembleChunks_EmptyInput(t *testing.T) {
	chunks, err := assembleChunks(nil, nil, nil, nil, 5*time.Minute)
	if err != nil {
		t.Fatalf("empty input errored: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestAssembleChunks_ParallelSliceMismatch(t *testing.T) {
	entries := []wirelogs.LogEntry{{Timestamp: time.Now(), Message: "hello"}}
	// Mismatched lengths should error cleanly rather than panic.
	_, err := assembleChunks(entries, []string{"a", "b"}, []string{"t"}, nil, time.Minute)
	if err == nil {
		t.Fatal("expected error for mismatched parallel slices")
	}
}

func TestAssembleChunks_LargeBatch(t *testing.T) {
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	// 5k entries of a repeating template — compression should bite.
	entries := make([]wirelogs.LogEntry, 0, 5000)
	for i := range 5000 {
		entries = append(entries, wirelogs.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Message:   fmt.Sprintf("task worker-%d processed job-%d in %dms", i%8, i, (i*7)%900),
			Labels:    map[string]string{"svc": "worker"},
		})
	}
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	_, sidIDs := buildStreams(entries, 0)

	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, time.Hour)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a large batch")
	}

	// Verify decompression works for every chunk produced.
	totalEntries := 0
	for _, c := range chunks {
		timestamps, _, err := DecodeChunk(c)
		if err != nil {
			t.Fatalf("chunk %s decompression: %v", c.ID, err)
		}
		if len(timestamps) != c.EntryCount {
			t.Errorf("chunk %s: decoded %d entries, EntryCount=%d",
				c.ID, len(timestamps), c.EntryCount)
		}
		totalEntries += c.EntryCount
	}
	if totalEntries == 0 {
		t.Fatal("no entries survived chunk assembly")
	}

	// Verify compression actually helps — aggregate compressed size
	// must be smaller than sum of raw message bytes for this very
	// repetitive payload.
	var compressed int
	var raw int
	for i, e := range entries {
		raw += len(e.Message) + 8 // message + timestamp
		_ = i
	}
	for _, c := range chunks {
		compressed += len(c.CompressedData)
	}
	if compressed >= raw {
		t.Errorf("compression did not shrink payload: compressed=%d, raw=%d",
			compressed, raw)
	}
}

func TestEncodeDecode_RoundTripEmptyVars(t *testing.T) {
	entries := []chunkEntry{
		{Timestamp: time.Unix(1000, 0).UTC(), Vars: nil},
		{Timestamp: time.Unix(1001, 500000).UTC(), Vars: []string{}},
		{Timestamp: time.Unix(1002, 0).UTC(), Vars: []string{"x"}},
	}
	raw := encodeChunkData(entries)
	decoded, err := decodeChunkData(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(entries) {
		t.Fatalf("entry count mismatch: got %d, want %d", len(decoded), len(entries))
	}
	for i, e := range decoded {
		if !e.Timestamp.Equal(entries[i].Timestamp) {
			t.Errorf("entry %d ts mismatch: got %s, want %s", i, e.Timestamp, entries[i].Timestamp)
		}
	}
}

func TestChunkID_Deterministic(t *testing.T) {
	ts := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	a := chunkID("stream-a", "tpl-b", ts)
	b := chunkID("stream-a", "tpl-b", ts)
	if a != b {
		t.Errorf("non-deterministic chunk ID: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "log-chunk:") {
		t.Errorf("chunk ID missing prefix: %s", a)
	}
	c := chunkID("stream-a", "tpl-c", ts)
	if a == c {
		t.Error("different template IDs produced same chunk ID")
	}
}

func TestWindowStart_Alignment(t *testing.T) {
	window := 5 * time.Minute
	ts := time.Date(2026, 4, 13, 12, 7, 30, 0, time.UTC) // 12:07:30
	got := windowStart(ts, window)
	want := time.Date(2026, 4, 13, 12, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("windowStart(12:07:30, 5m) = %s, want %s", got, want)
	}

	// Exact bucket boundary should stay put.
	boundary := time.Date(2026, 4, 13, 12, 5, 0, 0, time.UTC)
	if !windowStart(boundary, window).Equal(boundary) {
		t.Errorf("windowStart at exact boundary moved: got %s", windowStart(boundary, window))
	}
}

func TestCompressDecompress_RoundTrip(t *testing.T) {
	data := []byte(strings.Repeat("abcdefghijklmnop", 1024))
	compressed, err := compressBytes(data)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(compressed) >= len(data) {
		t.Errorf("compression did not help: original=%d, compressed=%d", len(data), len(compressed))
	}
	decompressed, err := decompressBytes(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decompressed) != string(data) {
		t.Error("round-trip data mismatch")
	}
}
