// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

import (
	"fmt"
	"os"
)

// fsyncDir flushes dir's own directory entries. It is the last step of the durable
// write the practice pattern prescribes explicitly — tmp -> fsync -> rename ->
// fsync(dir) — and the one atomicWriteFile was missing.
//
// WHAT THE MISSING STEP COSTS. Syncing the temp file puts its payload on the platter;
// the rename then publishes that payload under the target name. The rename is a
// DIRECTORY metadata change, and until the directory itself is flushed a crash can lose
// it: the bytes survive with nothing naming them, which reads back as a file that was
// never written. For this cache that is not a re-Fetchable miss — post-merge the
// renamed .seg file is the only durable copy of the constituents the reclaim removed.
//
// THE ERROR IS RETURNED, not logged. The server's writer
// (cmd/knowledge-server/internal/store/atomic_write.go) warns and carries on, which is
// defensible where a lost blob can be rebuilt; it is not defensible here, where a
// swallowed durability failure is the silent data loss diskSegmentCache.Put's own
// contract was rewritten to reject.
func fsyncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the cache root the caller has already written into
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("fsync dir: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close dir after fsync: %w", err)
	}
	return nil
}
