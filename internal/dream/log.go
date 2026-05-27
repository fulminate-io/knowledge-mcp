// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// WorkerLog appends one JSON-line InvocationRecord per call to a per-worker
// log file under <graphStorage>/workers/<name>.log. The writer is goroutine-
// safe: Append serializes through a mutex so concurrent invocations of the
// same worker do not interleave bytes within a single line.
//
// The file is opened with O_CREATE|O_WRONLY|O_APPEND, 0600. There is no
// rotation in v1 — operators rotate manually if size becomes a concern. The
// readers (ReadRecent, used by worker:status) tail the whole file and pick
// the last N lines.
type WorkerLog struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// OpenWorkerLog opens (or creates) <graphStorage>/workers/<name>.log in
// append-only mode with 0600 perms. Mirrors the openDreamLogger pattern at
// cmd/knowledge-server/server.go:292. The parent directory is created if
// missing. Caller is responsible for Close().
func OpenWorkerLog(graphStorage, name string) (*WorkerLog, error) {
	if graphStorage == "" {
		return nil, errors.New("dream: OpenWorkerLog: graphStorage is required")
	}
	if name == "" {
		return nil, errors.New("dream: OpenWorkerLog: name is required")
	}
	dir := filepath.Join(graphStorage, "workers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("dream: OpenWorkerLog: mkdir %q: %w", dir, err)
	}
	path := filepath.Join(dir, name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("dream: OpenWorkerLog: open %q: %w", path, err)
	}
	return &WorkerLog{path: path, f: f}, nil
}

// Append marshals rec to JSON and writes it as a single newline-terminated
// line under the writer's mutex. Producers MUST set rec.Time at the call
// site — Append does NOT timestamp.
func (w *WorkerLog) Append(rec InvocationRecord) error {
	if w == nil || w.f == nil {
		return errors.New("dream: WorkerLog.Append: nil log")
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("dream: WorkerLog.Append: marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("dream: WorkerLog.Append: write %q: %w", w.path, err)
	}
	return nil
}

// Close flushes and closes the underlying file. Idempotent — calling Close
// on an already-closed (or nil) log is a no-op.
func (w *WorkerLog) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.f.Close()
	w.f = nil
	return err
}

// ReadRecent opens <graphStorage>/workers/<name>.log read-only, scans every
// line as one InvocationRecord, and returns the last `limit` records in
// reverse-chronological order (newest first). A non-existent log file
// returns (nil, nil) — a worker that has never run is the same shape as
// "no recent invocations".
//
// Lines that fail to decode are skipped silently; the producer (Append) is
// the only writer, and we prefer surfacing the records we can parse over
// failing the entire status call on one corrupt line.
func ReadRecent(graphStorage, name string, limit int) ([]InvocationRecord, error) {
	if graphStorage == "" {
		return nil, errors.New("dream: ReadRecent: graphStorage is required")
	}
	if name == "" {
		return nil, errors.New("dream: ReadRecent: name is required")
	}
	if limit <= 0 {
		return nil, nil
	}
	path := filepath.Join(graphStorage, "workers", name+".log")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("dream: ReadRecent: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// One JSON object per line; bufio.Scanner with a generous buffer cap.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var all []InvocationRecord
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec InvocationRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Skip malformed lines — see comment above.
			continue
		}
		all = append(all, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("dream: ReadRecent: scan %q: %w", path, err)
	}

	// Return the last `limit` records, newest first.
	start := 0
	if len(all) > limit {
		start = len(all) - limit
	}
	tail := all[start:]
	out := make([]InvocationRecord, len(tail))
	for i, rec := range tail {
		out[len(tail)-1-i] = rec
	}
	return out, nil
}
