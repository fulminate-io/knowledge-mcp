// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// bm25DegradeStateFile is the per-graph record of input the BM25 builds DROPPED
// — work the format contained rather than indexed.
//
// A SEPARATE FILE FOR THE SAME REASON bm25_delta_state.go gives for its own: the
// records have DIFFERENT WRITERS. The cursors are advanced by the BM25 arm; this
// one is written by the ENGINE'S BUILD HOOK, on a different goroutine and at a
// different moment, so sharing one file would make each writer's wholesale
// replace clobber the other's fields.
const bm25DegradeStateFile = "bm25_degrade.json"

// bm25DegradeStateRecord is the on-disk shape of the per-graph drop census.
type bm25DegradeStateRecord struct {
	// Degraded is the fixed-vocabulary class → count census, accumulated across
	// builds. STORE, DO NOT INTERPRET: this package keys nothing on the class name
	// and reads no value, so a future degrade class needs no change here. The
	// vocabulary belongs to the format that produced it.
	Degraded map[string]int `json:"degraded"`
}

// bm25DegradeStatePathFor returns the record path for one graph. Rooted under the
// L2 cache like its siblings, and THE COUPLING IS INHERITED DELIBERATELY: wiping
// the cache drops this record too, and a census that survived a blob wipe would
// describe a corpus that no longer exists.
func bm25DegradeStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), bm25DegradeStateFile)
}

// RecordBuildDegrade ACCUMULATES one build's census into the graph's durable
// record, per class. It is the engine's OnBuildDegrade hook's landing point.
//
// AN EMPTY CENSUS OR AN UNSET CACHE DIRECTORY TOUCHES NO DISK, so a clean build
// pays one length check and nothing else.
//
// The whole read-modify-write is under the mutex because the caller is the
// engine's build hook, which may run on one of ReplaceBucketGroup's harvest
// workers — several at once, against the same graph.
//
// IT REPORTS A WRITE FAILURE LOUDLY AND RETURNS NOTHING. The hook's signature
// carries no error back to the engine (a contained drop is not the build's
// error), so a failure here can only be reported where it happens — naming the
// condition and exactly what was lost, rather than degrading silently.
func (m *Manager) RecordBuildDegrade(gt kgtypes.GraphType, name string, rep searchengine.BuildReport) {
	if len(rep.Degraded) == 0 || m.cacheDir == "" {
		return
	}
	m.bm25DegradeStateMu.Lock()
	defer m.bm25DegradeStateMu.Unlock()

	path := bm25DegradeStatePathFor(m.cacheDir, gt, name)
	rec, err := readBM25DegradeRecord(path)
	if err != nil {
		slog.Error("segmentdist: the BM25 drop census could NOT be read, so this build's drops are not accumulated onto it",
			"graph_type", gt, "name", name, "error", err, "dropped", rep.Degraded)
		return
	}
	if rec.Degraded == nil {
		rec.Degraded = make(map[string]int, len(rep.Degraded))
	}
	for class, n := range rep.Degraded {
		rec.Degraded[class] += n
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		slog.Error("segmentdist: the BM25 drop census could NOT be encoded, so this build's drops go unrecorded",
			"graph_type", gt, "name", name, "error", err, "dropped", rep.Degraded)
		return
	}
	if err := atomicWriteStateFile(path, raw); err != nil {
		slog.Error("segmentdist: the BM25 drop census could NOT be written, so this build's drops go unrecorded",
			"graph_type", gt, "name", name, "error", err, "dropped", rep.Degraded)
	}
}

// BM25DegradeCounts reads the graph's accumulated drop census.
//
// IT RETURNS A COPY, so a caller cannot mutate the record through it, and nil for
// an empty or absent record — the same empty-and-absent-are-one-state contract
// the census carries everywhere else. A read failure is reported as an empty
// census and a logged error rather than a partial one.
func (m *Manager) BM25DegradeCounts(gt kgtypes.GraphType, name string) map[string]int {
	if m.cacheDir == "" {
		return nil
	}
	m.bm25DegradeStateMu.Lock()
	defer m.bm25DegradeStateMu.Unlock()

	rec, err := readBM25DegradeRecord(bm25DegradeStatePathFor(m.cacheDir, gt, name))
	if err != nil {
		slog.Error("segmentdist: the BM25 drop census could NOT be read; reporting no drops for this graph",
			"graph_type", gt, "name", name, "error", err)
		return nil
	}
	if len(rec.Degraded) == 0 {
		return nil
	}
	out := make(map[string]int, len(rec.Degraded))
	maps.Copy(out, rec.Degraded)
	return out
}

// ResetBM25DegradeCounts returns the graph to the nothing-dropped state by
// REMOVING the record, the same first-class-operation disposition
// ResetBM25Cursors documents for itself: a reader must not have to tell "reset"
// apart from "never written".
//
// A NOT-EXIST ERROR IS SUCCESS — an absent record is the intended end state.
func (m *Manager) ResetBM25DegradeCounts(gt kgtypes.GraphType, name string) error {
	if m.cacheDir == "" {
		return nil
	}
	m.bm25DegradeStateMu.Lock()
	defer m.bm25DegradeStateMu.Unlock()

	if err := os.Remove(bm25DegradeStatePathFor(m.cacheDir, gt, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset bm25 degrade state for %s/%s: %w", gt, name, err)
	}
	return nil
}

// readBM25DegradeRecord loads one graph's record. A MISSING RECORD IS NOT AN
// ERROR: it decodes as the zero record, which is "nothing has been dropped".
// Callers hold bm25DegradeStateMu.
func readBM25DegradeRecord(path string) (bm25DegradeStateRecord, error) {
	var rec bm25DegradeStateRecord
	raw, err := os.ReadFile(path) //nolint:gosec // path is derived from the sanitized per-graph cache root.
	if err != nil {
		if os.IsNotExist(err) {
			return rec, nil
		}
		return rec, fmt.Errorf("read bm25 degrade state: %w", err)
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return bm25DegradeStateRecord{}, fmt.Errorf("decode bm25 degrade state: %w", err)
	}
	return rec, nil
}
