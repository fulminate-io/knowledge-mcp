// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mergeStateFile is the per-graph delta-merge horizon record, written beside the
// rebuild-state and manifest-fingerprint records (same per-graph directory, same
// rebuildStateFormat root) but in its OWN file.
//
// A SEPARATE FILE FOR THE SAME REASON manifest_state.go gives: the records have
// DIFFERENT writers. The rebuild driver replaces {watermark, tombstones}
// wholesale, the publish path writes the fingerprint, and this horizon is
// advanced by the delta-merge arm — so sharing one file would make each writer's
// wholesale replace clobber the others' fields.
const mergeStateFile = "merge.json"

// mergeStateRecord is the on-disk shape of the per-graph delta-merge horizon.
type mergeStateRecord struct {
	// MergeHorizonNanos is the SERVER-SERVED horizon the last landed delta merge
	// was scanned up to. Server-served for the reason rebuild_state.go gives for
	// its own watermark: a client clock can read the same instant as the writes it
	// is meant to exclude, and the server's strict after-comparison would then drop
	// exactly those rows.
	MergeHorizonNanos int64 `json:"merge_horizon_nanos"`
}

// mergeStatePathFor returns the record path for one graph. Rooted under the L2
// cache like its two siblings, so wiping the cache drops the horizon too — the
// correct coupling: a cache with no blobs left has nothing a bounded delta window
// could usefully extend.
func mergeStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), mergeStateFile)
}

// LoadMergeWatermark reads the graph's persisted delta-merge horizon.
//
// A MISSING RECORD IS NOT AN ERROR — it returns a zero horizon, which callers
// read as "this graph has no merge horizon yet". That is deliberately NOT the
// same as "scan from zero": a zero-watermark scan is the whole vectored corpus,
// which is the read the merge architecture exists to avoid, so the caller's rule
// is to pull nothing until a horizon is seeded.
//
// A Manager with no cache directory keeps no durable state at all and reports the
// same zero with a nil error.
func (m *Manager) LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error) {
	if m.cacheDir == "" {
		return 0, nil
	}
	m.mergeStateMu.Lock()
	defer m.mergeStateMu.Unlock()

	raw, err := os.ReadFile(mergeStatePathFor(m.cacheDir, gt, name)) //nolint:gosec // path is derived from the sanitized per-graph cache root.
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read merge state for %s/%s: %w", gt, name, err)
	}
	var rec mergeStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, fmt.Errorf("decode merge state for %s/%s: %w", gt, name, err)
	}
	return rec.MergeHorizonNanos, nil
}

// SaveMergeWatermark persists the graph's delta-merge horizon, replacing it
// wholesale. The write is atomic (temp file in the same directory, then rename),
// so a crash mid-write leaves the previous horizon rather than a truncated record
// — which would decode as no horizon and cost one re-read of one window.
//
// A Manager with no cache directory has nowhere to put the record and silently
// keeps none.
func (m *Manager) SaveMergeWatermark(gt kgtypes.GraphType, name string, horizonNanos int64) error {
	if m.cacheDir == "" {
		return nil
	}
	raw, err := json.Marshal(mergeStateRecord{MergeHorizonNanos: horizonNanos})
	if err != nil {
		return fmt.Errorf("encode merge state for %s/%s: %w", gt, name, err)
	}

	m.mergeStateMu.Lock()
	defer m.mergeStateMu.Unlock()
	return atomicWriteStateFile(mergeStatePathFor(m.cacheDir, gt, name), raw)
}
