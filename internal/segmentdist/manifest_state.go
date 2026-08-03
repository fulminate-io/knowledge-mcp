// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manifestStateFile is the per-graph manifest-fingerprint record, written beside
// the rebuild-state record (same per-graph directory, same rebuildStateFormat
// root) but in its OWN file.
//
// A SEPARATE FILE RATHER THAN A FIELD ON rebuildStateRecord, deliberately. The two
// records have DIFFERENT writers: the rebuild driver writes the watermark +
// tombstones, and the publish path writes this fingerprint. Sharing one file would
// make each writer's wholesale replace clobber the other's field — a read-
// modify-write race between a rebuild finishing and a publish landing, which is
// exactly the pair that runs back to back. Two files, two writers, no overlap.
const manifestStateFile = "manifest.json"

// manifestFingerprint is the per-format summary of a LANDED manifest swap: how
// many entries it referenced, and a hash of that id set.
//
// BOTH FIELDS ARE CARRIED, and neither replaces the other.
//
//   - Count is the DIRECTIONAL signal. The reconcile's mismatch semantics are
//     asymmetric — a cache SMALLER than the manifest is a shortfall to repair, a
//     cache LARGER is the documented un-reclaimed-merge superset
//     (manager_load.go:232-240) and must be left alone. A hash cannot express that
//     asymmetry: it reports "different", never "short". Comparing hashes alone
//     would therefore fire the repair path on every legitimate superset and pay a
//     source.List every tick, destroying the zero-RPC healthy-arm property this
//     record exists to preserve.
//   - Hash is the EQUAL-COUNT DISCRIMINATOR, and it closes a real hole the count
//     alone leaves: a cache holding as many files as the manifest has entries, but
//     not the SAME ones — some manifest members missing, an equal number of
//     un-reclaimed orphans present — reads as healthy on a count compare while the
//     missing members are never fetched. That is the defect's own shape surviving
//     its own detector.
//
// Both are local reads, so consulting them costs no network either way.
type manifestFingerprint struct {
	Count int    `json:"count"`
	Hash  string `json:"hash"`
}

// manifestStateRecord is the on-disk shape: one fingerprint per segment format.
// Per format because the two formats carry SEPARATE manifests over the same nodes,
// so a shortfall in one says nothing about the other.
type manifestStateRecord struct {
	Formats map[string]manifestFingerprint `json:"formats"`
}

// manifestStatePathFor returns the record path for one graph. Rooted under the L2
// cache like the rebuild-state record, so wiping the cache also drops the
// fingerprint — the correct coupling: the fingerprint describes a cache that no
// longer exists, and an absent record means "nothing to compare", which is exactly
// right for a cold cache the load path will fill from the server anyway.
func manifestStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), manifestStateFile)
}

// fingerprintOf summarizes a segment id set. It SORTS a copy before hashing, so
// the fingerprint is a property of the SET and not of the order a caller happened
// to enumerate it in — engine.Export, cache.Keys and source.List all iterate maps
// or slices with no common order, and an order-sensitive hash would report a
// spurious difference between two identical sets on nearly every call.
func fingerprintOf(ids []searchengine.SegmentID) manifestFingerprint {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	h := sha256.New()
	for _, id := range sorted {
		h.Write([]byte(id))
		h.Write([]byte{0}) // separator: ids are hex, but never let two ids concatenate ambiguously.
	}
	return manifestFingerprint{Count: len(sorted), Hash: hex.EncodeToString(h.Sum(nil))}
}

// loadManifestFingerprint reads one format's persisted fingerprint. It reports
// ok=false for a missing record, an undecodable one, or a format with no entry —
// all of which mean the same thing to the caller: there is nothing to compare
// against, so no completeness claim can be made this tick and the reconcile must
// do nothing rather than guess. The next completed publish writes one.
//
// A Manager with no cache directory keeps no durable state, so it always reports
// ok=false and the completeness pass is inert for it.
func (m *Manager) loadManifestFingerprint(
	gt kgtypes.GraphType, name, format string,
) (manifestFingerprint, bool) {
	if m.cacheDir == "" {
		return manifestFingerprint{}, false
	}
	m.manifestStateMu.Lock()
	defer m.manifestStateMu.Unlock()
	rec, err := readManifestStateRecord(manifestStatePathFor(m.cacheDir, gt, name))
	if err != nil {
		return manifestFingerprint{}, false
	}
	fp, ok := rec.Formats[format]
	return fp, ok
}

// saveManifestFingerprint records one format's fingerprint, preserving the other
// format's entry.
//
// THE SINGLE WRITER IS publishResident (manager_prune.go) — the one function that
// completes a manifest swap, and the same place completedSwaps is incremented.
// Naming it matters: a second writer with its own notion of "the manifest landed"
// would persist a count that never described a published set, and this record
// would become a source of spurious mismatches rather than a detector of real
// ones. The completeness reconcile also refreshes it (manager_completeness.go)
// after a source read proves the cache already covers the manifest — that is a
// CORRECTION of a stale record against the authority, not a second producer of it.
//
// The read-modify-write is mutex-guarded because BOTH format arms of one graph
// write the same file, and an unguarded pair would lose whichever wrote first.
func (m *Manager) saveManifestFingerprint(
	gt kgtypes.GraphType, name, format string, fp manifestFingerprint,
) error {
	if m.cacheDir == "" {
		return nil
	}
	m.manifestStateMu.Lock()
	defer m.manifestStateMu.Unlock()

	path := manifestStatePathFor(m.cacheDir, gt, name)
	rec, err := readManifestStateRecord(path)
	if err != nil {
		// An unreadable/corrupt record is REPLACED rather than propagated: the file is
		// a cache-derived hint, and refusing to write because the old copy is damaged
		// would leave the graph permanently undetectable.
		rec = manifestStateRecord{}
	}
	if rec.Formats == nil {
		rec.Formats = map[string]manifestFingerprint{}
	}
	rec.Formats[format] = fp

	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode manifest state for %s/%s: %w", gt, name, err)
	}
	return atomicWriteManifestState(path, raw)
}

// readManifestStateRecord decodes the record at path. A missing file decodes to an
// empty record with no error — the "never published yet" state.
func readManifestStateRecord(path string) (manifestStateRecord, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is derived from the sanitized per-graph cache root.
	if err != nil {
		if os.IsNotExist(err) {
			return manifestStateRecord{}, nil
		}
		return manifestStateRecord{}, err
	}
	var rec manifestStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return manifestStateRecord{}, err
	}
	return rec, nil
}

// atomicWriteManifestState writes via a temp file in the same directory then
// renames, mirroring SaveRebuildState: a crash mid-write must leave the previous
// record rather than a truncated one, which would decode as "no record" and
// silently disable whatever detector reads it.
//
// It is NOT manifest-specific despite its name: the merge-horizon and backstop
// records (merge_state.go, repair_state.go) call it directly rather than each
// carrying a third copy of the MkdirAll + CreateTemp + Rename sequence. The temp
// prefix is derived from the target filename so a stray temp file names the record
// it belonged to.
func atomicWriteManifestState(path string, raw []byte) error {
	dir := filepath.Dir(path)
	// 0o750 matches newDiskSegmentCache's cache-root mode: this record lives beside
	// the L2 blobs and describes them, so it takes the same group-readable,
	// world-closed permissions rather than a looser mode of its own.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create manifest state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create manifest state temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write manifest state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close manifest state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit manifest state: %w", err)
	}
	return nil
}
