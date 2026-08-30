// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteStateFile writes via a temp file in the same directory then renames,
// mirroring SaveRebuildState: a crash mid-write must leave the previous record rather
// than a truncated one, which would decode as "no record" and silently disable
// whatever detector reads it.
//
// IT IS NOT SPECIFIC TO ANY ONE RECORD, and it now has a name that says so. Its
// predecessor was named for the manifest state it was written for, and its own doc
// had to warn readers that the name was wrong — the merge-horizon and repair records
// (merge_state.go, repair_state.go) called it directly rather than each carrying a
// third copy of the MkdirAll + CreateTemp + Rename sequence. The manifest record it
// was named for is gone; those two callers are the whole of its caller set, which is
// what made it a relocation rather than a deletion.
//
// The temp prefix is derived from the target filename so a stray temp file names the
// record it belonged to.
func atomicWriteStateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	// 0o750 matches newDiskSegmentCache's cache-root mode: these records live beside
	// the L2 blobs and describe them, so they take the same group-readable,
	// world-closed permissions rather than a looser mode of their own.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create state temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit state: %w", err)
	}
	return nil
}
