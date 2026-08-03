// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuildStateFormat is the reserved format tag the per-graph rebuild record is
// rooted under, so it reuses graphCacheDirFor's per-(format, graphType, name)
// layout and its name sanitizing rather than re-deriving a path. It is NOT a
// segment format: no engine ever lists or fetches under it, and no blob is written
// there. Rooting it beside the per-format blob directories rather than inside one
// is deliberate — the record describes the graph as a whole (both formats share
// one watermark and one tombstone set), so it must not live under either format's
// directory.
const rebuildStateFormat = "rebuildstate"

// rebuildStateFile is the record's filename inside its per-graph directory.
const rebuildStateFile = "state.json"

// rebuildStateRecord is the on-disk shape of the per-graph segment-rebuild record.
// The two fields are written TOGETHER, in one file, on purpose: a crash that
// advanced the watermark without persisting the tombstones would mean those ids
// are never scanned again and never learned again, silently reopening the window
// where a blob shipped before a delete resurrects the removed node on import.
type rebuildStateRecord struct {
	// WatermarkNanos is the SERVER-SERVED horizon the last landed rebuild was
	// scanned up to — never a client clock reading, which under skew lands in the
	// same instant as writes it is meant to exclude and drops exactly those rows.
	WatermarkNanos int64 `json:"watermark_nanos"`
	// Tombstoned holds the ids learned deleted whose partitions have not yet been
	// re-emitted without them, so every Import can seed them dead.
	Tombstoned []string `json:"tombstoned,omitempty"`
}

// rebuildStatePathFor returns the record path for one graph. base is the L2 cache
// root, so wiping the cache also removes the record — the correct coupling: the
// watermark describes blobs that are no longer there, and a zeroed watermark is
// exactly the full re-emit that state calls for.
func rebuildStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), rebuildStateFile)
}

// LoadRebuildState reads the persisted per-graph rebuild record: the watermark the
// next scan should be scoped by, and the tombstoned ids whose partitions still
// carry them.
//
// A MISSING RECORD IS NOT AN ERROR — it returns a zero watermark and no
// tombstones, which is the full-corpus scan the driver ran before any of this
// existed. A fresh daemon, a wiped L2 cache and an operator reset therefore all
// converge on the same safe behavior: rebuild everything once.
//
// A Manager with no cache directory keeps no durable state at all, so it reports
// the same zero. Every rebuild on such a Manager is a full rebuild.
func (m *Manager) LoadRebuildState(
	gt kgtypes.GraphType, name string,
) (watermarkNanos int64, tombstoned []searchengine.ExternalID, err error) {
	if m.cacheDir == "" {
		return 0, nil, nil
	}
	raw, err := os.ReadFile(rebuildStatePathFor(m.cacheDir, gt, name))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("read rebuild state for %s/%s: %w", gt, name, err)
	}
	var rec rebuildStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, nil, fmt.Errorf("decode rebuild state for %s/%s: %w", gt, name, err)
	}
	ids := make([]searchengine.ExternalID, 0, len(rec.Tombstoned))
	for _, id := range rec.Tombstoned {
		ids = append(ids, searchengine.ExternalID(id))
	}
	return rec.WatermarkNanos, ids, nil
}

// SaveRebuildState writes the per-graph rebuild record, replacing it wholesale.
// The caller writes the watermark and the tombstone set in ONE call because they
// are one durable fact; there is deliberately no way to advance the watermark
// alone.
//
// The write is atomic (temp file in the same directory, then rename), so a crash
// mid-write leaves the previous record rather than a truncated one that would
// decode as a zero watermark and force a full re-emit.
//
// A Manager with no cache directory has nowhere to put the record and silently
// keeps none — the graph then rebuilds in full every time, which is correct but
// not incremental.
func (m *Manager) SaveRebuildState(
	gt kgtypes.GraphType, name string, watermarkNanos int64, tombstoned []searchengine.ExternalID,
) error {
	if m.cacheDir == "" {
		return nil
	}
	ids := make([]string, 0, len(tombstoned))
	for _, id := range tombstoned {
		ids = append(ids, string(id))
	}
	raw, err := json.Marshal(rebuildStateRecord{WatermarkNanos: watermarkNanos, Tombstoned: ids})
	if err != nil {
		return fmt.Errorf("encode rebuild state for %s/%s: %w", gt, name, err)
	}

	path := rebuildStatePathFor(m.cacheDir, gt, name)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create rebuild state dir for %s/%s: %w", gt, name, err)
	}
	tmp, err := os.CreateTemp(dir, rebuildStateFile+".*")
	if err != nil {
		return fmt.Errorf("create rebuild state temp for %s/%s: %w", gt, name, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write rebuild state for %s/%s: %w", gt, name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close rebuild state for %s/%s: %w", gt, name, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit rebuild state for %s/%s: %w", gt, name, err)
	}
	return nil
}
