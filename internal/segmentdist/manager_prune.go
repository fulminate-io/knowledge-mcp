// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// writeNewBlobsToL2 writes each supplied blob into the L2 disk cache. It is the
// DURABILITY leg of a build: the engine has sealed these segments in memory, and
// this is what makes them survive the process.
//
// It is named for what it does. Its predecessor byte-packed the blobs into ≤64 MiB
// requests, uploaded them, stamped the returned ids into two diff-suppression sets
// and advanced a generation cursor. All of that was in service of an upload that no
// longer happens; the cache write was the one part with a surviving purpose, so it is
// the whole function now.
//
// An empty slice is a NO-OP. A failed Put ABORTS and returns — a blob that did not
// reach disk is a segment the next process cannot load, and continuing past it
// would leave the engine reporting a resident set the cache cannot re-materialize.
func (m *distManager[Q, S]) writeNewBlobsToL2(blobs []searchengine.SegmentBlob) error {
	for _, b := range blobs {
		if err := m.cache.Put(b.ID, b.Envelope, b.Bytes); err != nil {
			return err
		}
	}
	return nil
}
