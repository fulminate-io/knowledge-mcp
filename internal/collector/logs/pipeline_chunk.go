// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// chunkKey uniquely identifies a (stream, template, window) bucket
// during chunk assembly. windowStart is the floored start of the time
// window the chunk covers (see windowStart()).
type chunkKey struct {
	StreamID    string
	TemplateID  string
	WindowStart time.Time
}

// chunkEntry is the per-entry payload held by a chunk pre-compression.
// Only the timestamp and variable values are retained — the template
// itself recovers the skeleton text at decode time.
type chunkEntry struct {
	Timestamp time.Time
	Vars      []string
}

// assembleChunks groups entries by (streamID, templateID, time-window)
// and produces ZSTD-compressed LogChunks. Entries without a template
// ID or stream ID are skipped (the pre-processing pass couldn't cluster
// them). Variable values are recovered by diffing the entry's tokens
// against the matched template's pattern.
//
// The returned chunks are sorted by (StreamID, TemplateID, StartTime)
// for deterministic downstream iteration.
func assembleChunks(
	entries []wirelogs.LogEntry,
	entryStreamIDs []string,
	entryTemplateIDs []string,
	templates []*wirelogs.LogTemplate,
	window time.Duration,
) ([]*wirelogs.LogChunk, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entryStreamIDs) != len(entries) || len(entryTemplateIDs) != len(entries) {
		return nil, fmt.Errorf("logs: entry parallel-slice length mismatch")
	}
	if window <= 0 {
		window = DefaultChunkWindow
	}

	tmplByID := templatesByID(templates)
	buckets := make(map[chunkKey][]chunkEntry)

	for i, e := range entries {
		sid := entryStreamIDs[i]
		tid := entryTemplateIDs[i]
		if sid == "" || tid == "" {
			continue
		}
		tpl := tmplByID[tid]
		vars := extractEntryVars(tpl, e.Message)
		ws := windowStart(e.Timestamp, window)
		k := chunkKey{StreamID: sid, TemplateID: tid, WindowStart: ws}
		buckets[k] = append(buckets[k], chunkEntry{Timestamp: e.Timestamp, Vars: vars})
	}

	return buildChunksFromBuckets(buckets)
}

// buildChunksFromBuckets compresses each bucket into a wirelogs.LogChunk.
func buildChunksFromBuckets(buckets map[chunkKey][]chunkEntry) ([]*wirelogs.LogChunk, error) {
	chunks := make([]*wirelogs.LogChunk, 0, len(buckets))
	for k, entries := range buckets {
		data := encodeChunkData(entries)
		compressed, err := compressBytes(data)
		if err != nil {
			return nil, err
		}
		start, end := entryTimeRange(entries)
		chunks = append(chunks, &wirelogs.LogChunk{
			ID:             chunkID(k.StreamID, k.TemplateID, k.WindowStart),
			StreamID:       k.StreamID,
			TemplateID:     k.TemplateID,
			StartTime:      start,
			EndTime:        end,
			CompressedData: compressed,
			EntryCount:     len(entries),
		})
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].StreamID != chunks[j].StreamID {
			return chunks[i].StreamID < chunks[j].StreamID
		}
		if chunks[i].TemplateID != chunks[j].TemplateID {
			return chunks[i].TemplateID < chunks[j].TemplateID
		}
		return chunks[i].StartTime.Before(chunks[j].StartTime)
	})
	return chunks, nil
}

// windowStart returns the start of the time bucket containing ts,
// aligned to the UTC epoch. A zero window degrades to ts itself.
func windowStart(ts time.Time, window time.Duration) time.Time {
	if window <= 0 {
		return ts
	}
	ns := ts.UnixNano()
	w := window.Nanoseconds()
	if w == 0 {
		return ts
	}
	bucket := (ns / w) * w
	return time.Unix(0, bucket).UTC()
}

// chunkID returns a deterministic ID: sha256 of the tuple.
func chunkID(streamID, templateID string, windowStart time.Time) string {
	var b bytes.Buffer
	b.WriteString(streamID)
	b.WriteByte('|')
	b.WriteString(templateID)
	b.WriteByte('|')
	_ = binary.Write(&b, binary.BigEndian, windowStart.UnixNano())
	h := sha256.Sum256(b.Bytes())
	return fmt.Sprintf("log-chunk:%x", h[:16])
}

// entryTimeRange returns the earliest and latest timestamps in entries.
func entryTimeRange(entries []chunkEntry) (time.Time, time.Time) {
	if len(entries) == 0 {
		return time.Time{}, time.Time{}
	}
	start, end := entries[0].Timestamp, entries[0].Timestamp
	for _, e := range entries[1:] {
		if e.Timestamp.Before(start) {
			start = e.Timestamp
		}
		if e.Timestamp.After(end) {
			end = e.Timestamp
		}
	}
	return start, end
}

// extractEntryVars returns the tokens at wildcard positions of tpl that
// differ from the template in entry's message. Returns nil if tpl is
// nil or the token count does not match (a sign of consolidation; in
// that case the chunk still records the timestamp for aggregation).
func extractEntryVars(tpl *wirelogs.LogTemplate, message string) []string {
	if tpl == nil {
		return nil
	}
	tplTokens := Tokenize(tpl.Pattern)
	msgTokens := Tokenize(PreProcess(message))
	if len(tplTokens) != len(msgTokens) {
		return nil
	}
	return extractVars(tplTokens, msgTokens)
}

// encodeChunkData serializes entries to a compact binary format:
//
//	[count:uvarint]
//	for each entry:
//	  [ts_unix_nano:varint]
//	  [vars_count:uvarint]
//	  for each var: [len:uvarint][bytes]
//
// The result is NOT compressed — compressBytes handles that step so
// tests can exercise encode/decode separately.
func encodeChunkData(entries []chunkEntry) []byte {
	var buf bytes.Buffer
	tmp := make([]byte, binary.MaxVarintLen64)

	n := binary.PutUvarint(tmp, uint64(len(entries)))
	buf.Write(tmp[:n])

	for _, e := range entries {
		n = binary.PutVarint(tmp, e.Timestamp.UnixNano())
		buf.Write(tmp[:n])
		n = binary.PutUvarint(tmp, uint64(len(e.Vars)))
		buf.Write(tmp[:n])
		for _, v := range e.Vars {
			n = binary.PutUvarint(tmp, uint64(len(v)))
			buf.Write(tmp[:n])
			buf.WriteString(v)
		}
	}
	return buf.Bytes()
}

// decodeChunkData is the inverse of encodeChunkData.
func decodeChunkData(data []byte) ([]chunkEntry, error) {
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("logs: decodeChunkData: bad entry count")
	}
	pos := n
	entries := make([]chunkEntry, 0, count)
	for i := range count {
		ts, tsN := binary.Varint(data[pos:])
		if tsN <= 0 {
			return nil, fmt.Errorf("logs: decodeChunkData: bad timestamp at entry %d", i)
		}
		pos += tsN
		vc, vcN := binary.Uvarint(data[pos:])
		if vcN <= 0 {
			return nil, fmt.Errorf("logs: decodeChunkData: bad var count at entry %d", i)
		}
		pos += vcN
		vars := make([]string, 0, vc)
		for j := range vc {
			vl, vlN := binary.Uvarint(data[pos:])
			if vlN <= 0 {
				return nil, fmt.Errorf("logs: decodeChunkData: bad var length at entry %d var %d", i, j)
			}
			pos += vlN
			if pos+int(vl) > len(data) {
				return nil, fmt.Errorf("logs: decodeChunkData: var bytes overflow at entry %d var %d", i, j)
			}
			vars = append(vars, string(data[pos:pos+int(vl)]))
			pos += int(vl)
		}
		entries = append(entries, chunkEntry{
			Timestamp: time.Unix(0, ts).UTC(),
			Vars:      vars,
		})
	}
	return entries, nil
}

// compressBytes ZSTD-compresses a byte slice at default compression
// level. Returns a freshly allocated buffer; callers may retain it.
func compressBytes(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil), nil
}

// decompressBytes is the inverse of compressBytes.
func decompressBytes(data []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(data, nil)
}

// DecodeChunk is the public helper used by query tooling to recover
// the entries stored in a wirelogs.LogChunk. It ZSTD-decompresses CompressedData
// and decodes the binary entry stream.
func DecodeChunk(chunk *wirelogs.LogChunk) ([]time.Time, [][]string, error) {
	if chunk == nil {
		return nil, nil, fmt.Errorf("logs: DecodeChunk: nil chunk")
	}
	raw, err := decompressBytes(chunk.CompressedData)
	if err != nil {
		return nil, nil, err
	}
	entries, err := decodeChunkData(raw)
	if err != nil {
		return nil, nil, err
	}
	timestamps := make([]time.Time, len(entries))
	vars := make([][]string, len(entries))
	for i, e := range entries {
		timestamps[i] = e.Timestamp
		vars[i] = e.Vars
	}
	return timestamps, vars, nil
}
