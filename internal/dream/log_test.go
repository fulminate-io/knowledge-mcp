// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestOpenWorkerLog_AppendRoundTrip exercises the full open → append →
// readRecent loop a Runner uses for one invocation.
func TestOpenWorkerLog_AppendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	wl, err := OpenWorkerLog(dir, "smoke-hello")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	defer func() { _ = wl.Close() }()

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	records := []InvocationRecord{
		{Time: now, Kind: "start", Trigger: "manual"},
		{Time: now.Add(time.Second), Kind: "tool-call", Tool: "search", Args: json.RawMessage(`{"q":"hi"}`)},
		{Time: now.Add(2 * time.Second), Kind: "tool-result", Tool: "search", Status: "ok", DurationMs: 7},
		{Time: now.Add(3 * time.Second), Kind: "end", Status: "ok", DurationMs: 3000},
	}
	for _, rec := range records {
		if err := wl.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := wl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadRecent(dir, "smoke-hello", 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != len(records) {
		t.Fatalf("ReadRecent len = %d, want %d", len(got), len(records))
	}
	// Newest first: end, tool-result, tool-call, start.
	wantKinds := []string{"end", "tool-result", "tool-call", "start"}
	for i, rec := range got {
		if rec.Kind != wantKinds[i] {
			t.Errorf("got[%d].Kind = %q, want %q", i, rec.Kind, wantKinds[i])
		}
	}
}

// TestReadRecent_LimitClamps verifies Limit returns at most N records and
// always picks the tail (newest writes).
func TestReadRecent_LimitClamps(t *testing.T) {
	dir := t.TempDir()
	wl, err := OpenWorkerLog(dir, "tail-limit")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	for i := range 7 {
		if err := wl.Append(InvocationRecord{
			Time: time.Unix(int64(i), 0).UTC(),
			Kind: "end",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	_ = wl.Close()

	got, err := ReadRecent(dir, "tail-limit", 3)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	// Newest first: should be t=6,5,4.
	wantSec := []int64{6, 5, 4}
	for i, rec := range got {
		if rec.Time.Unix() != wantSec[i] {
			t.Errorf("got[%d].Time = %d, want %d", i, rec.Time.Unix(), wantSec[i])
		}
	}
}

// TestReadRecent_MissingLogReturnsNil hits the never-ran-yet branch.
func TestReadRecent_MissingLogReturnsNil(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadRecent(dir, "no-such-worker", 5)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %#v, want nil", got)
	}
}

// TestReadRecent_NonZeroLimitGate guards the limit<=0 fast-path.
func TestReadRecent_NonZeroLimitGate(t *testing.T) {
	dir := t.TempDir()
	wl, err := OpenWorkerLog(dir, "zero-limit")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	if err := wl.Append(InvocationRecord{Time: time.Now(), Kind: "end"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = wl.Close()

	got, err := ReadRecent(dir, "zero-limit", 0)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %#v, want nil with limit=0", got)
	}
}

// TestWorkerLog_AppendIsConcurrentSafe writes from many goroutines and
// asserts every line round-trips intact.
func TestWorkerLog_AppendIsConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	wl, err := OpenWorkerLog(dir, "concurrent")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	defer func() { _ = wl.Close() }()

	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				_ = wl.Append(InvocationRecord{
					Time:   time.Unix(int64(w*perWriter+i), 0).UTC(),
					Kind:   "tool-call",
					Tool:   "search",
					Status: "ok",
				})
			}
		}(w)
	}
	wg.Wait()
	_ = wl.Close()

	got, err := ReadRecent(dir, "concurrent", 10*writers*perWriter)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != writers*perWriter {
		t.Fatalf("len(got) = %d, want %d", len(got), writers*perWriter)
	}
}

// TestReadRecent_SkipsMalformedLines verifies a partially-corrupt log still
// returns the parseable lines instead of failing the whole read.
func TestReadRecent_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	wl, err := OpenWorkerLog(dir, "corrupt")
	if err != nil {
		t.Fatalf("OpenWorkerLog: %v", err)
	}
	if err := wl.Append(InvocationRecord{Time: time.Unix(1, 0).UTC(), Kind: "start"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = wl.Close()

	// Hand-write a corrupt line + a real one.
	path := filepath.Join(dir, "workers", "corrupt.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("{ this is not json }\n"); err != nil {
		t.Fatalf("Write garbage: %v", err)
	}
	if _, err := f.WriteString(`{"time":"2026-01-02T03:04:05Z","kind":"end"}` + "\n"); err != nil {
		t.Fatalf("Write good: %v", err)
	}
	_ = f.Close()

	got, err := ReadRecent(dir, "corrupt", 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	// The corrupt line is silently skipped; we expect 2 valid records.
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
}

// TestOpenWorkerLog_Validation guards the require-graph-storage and
// require-name branches.
func TestOpenWorkerLog_Validation(t *testing.T) {
	if _, err := OpenWorkerLog("", "smoke"); err == nil {
		t.Errorf("OpenWorkerLog with empty graphStorage: want error, got nil")
	}
	if _, err := OpenWorkerLog(t.TempDir(), ""); err == nil {
		t.Errorf("OpenWorkerLog with empty name: want error, got nil")
	}
}
