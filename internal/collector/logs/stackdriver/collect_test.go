// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"google.golang.org/api/iterator"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeNexter replays a canned slice of entries followed by iterator.Done.
// Implements entryNexter so tests can drive drainEntries without a real
// logadmin client.
type fakeNexter struct {
	entries []*logging.Entry
	idx     int
}

func (f *fakeNexter) Next() (*logging.Entry, error) {
	if f.idx >= len(f.entries) {
		return nil, iterator.Done
	}
	e := f.entries[f.idx]
	f.idx++
	return e, nil
}

// makeEntries produces n synthetic logging.Entries with sequential timestamps.
func makeEntries(n int) []*logging.Entry {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	out := make([]*logging.Entry, 0, n)
	for i := range n {
		out = append(out, &logging.Entry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  logging.Info,
			Payload:   "entry",
			LogName:   "projects/p/logs/stderr",
		})
	}
	return out
}

// TestDrainEntries_UnboundedCollectsAll verifies MaxEntries=0 drains ALL
// entries from the iterator — well above the old 500 default cap.
func TestDrainEntries_UnboundedCollectsAll(t *testing.T) {
	const n = 1500
	nexter := &fakeNexter{entries: makeEntries(n)}

	total := 0
	emit := func(batch []logwire.LogEntry) error {
		total += len(batch)
		return nil
	}

	err := drainEntries(nexter, "p", logwire.Query{MaxEntries: 0}, 0, emit)
	if err != nil {
		t.Fatalf("drainEntries: %v", err)
	}
	if total != n {
		t.Errorf("collected %d entries, want %d (unbounded)", total, n)
	}
}

// TestDrainEntries_NegativeTreatedAsUnbounded confirms -1 means no cap.
func TestDrainEntries_NegativeTreatedAsUnbounded(t *testing.T) {
	const n = 700
	nexter := &fakeNexter{entries: makeEntries(n)}

	total := 0
	emit := func(batch []logwire.LogEntry) error {
		total += len(batch)
		return nil
	}

	err := drainEntries(nexter, "p", logwire.Query{MaxEntries: -1}, -1, emit)
	if err != nil {
		t.Fatalf("drainEntries: %v", err)
	}
	if total != n {
		t.Errorf("collected %d entries, want %d", total, n)
	}
}

// TestDrainEntries_BoundedStopsAtCap verifies MaxEntries > 0 still caps total.
func TestDrainEntries_BoundedStopsAtCap(t *testing.T) {
	const n = 1500
	nexter := &fakeNexter{entries: makeEntries(n)}

	total := 0
	emit := func(batch []logwire.LogEntry) error {
		total += len(batch)
		return nil
	}

	err := drainEntries(nexter, "p", logwire.Query{MaxEntries: 600}, 600, emit)
	if err != nil {
		t.Fatalf("drainEntries: %v", err)
	}
	if total != 600 {
		t.Errorf("collected %d entries, want 600 (bounded cap)", total)
	}
}

// TestDrainEntries_FlushesInBatches verifies the streaming emitBatchSize
// keeps memory bounded: emit is called multiple times for a >emitBatchSize
// stream regardless of MaxEntries being unbounded.
func TestDrainEntries_FlushesInBatches(t *testing.T) {
	// 2.5 * emitBatchSize to force at least 2 mid-stream flushes + tail.
	const n = 2*emitBatchSize + emitBatchSize/2
	nexter := &fakeNexter{entries: makeEntries(n)}

	calls := 0
	total := 0
	emit := func(batch []logwire.LogEntry) error {
		calls++
		total += len(batch)
		return nil
	}

	err := drainEntries(nexter, "p", logwire.Query{MaxEntries: 0}, 0, emit)
	if err != nil {
		t.Fatalf("drainEntries: %v", err)
	}
	if total != n {
		t.Errorf("collected %d entries, want %d", total, n)
	}
	if calls < 3 {
		t.Errorf("emit calls = %d, expected at least 3 for %d entries (batch=%d)",
			calls, n, emitBatchSize)
	}
}
