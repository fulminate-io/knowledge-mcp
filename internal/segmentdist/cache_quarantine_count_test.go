// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// cache_quarantine_count_test.go — the WITHDRAWAL COUNT this store publishes, which
// is what manage(status) renders per graph and format.
//
// THE COUNT IS A LOSS READING, and every property asserted here follows from that.
// It counts DISTINCT segments rather than reports, because the engine reports one
// corruption from every concurrent query that touches a segment. It SURVIVES A
// RESTART, because the loss does: the file is moved out of the root and nothing
// re-adopts or re-fetches it, so a count that reset to zero would report a recovered
// graph while its documents were still gone.

// quarantineCountCache seeds a cache with two stored blobs and returns it with its
// root and their ids, so a row can withdraw one and read the count.
//
// THE IDS ARE THE PAYLOADS' OWN HASHES because Put verifies the content address: the
// store refuses bytes that do not hash to the name they are filed under, which is
// the check that makes the filename trustworthy to every reader.
func quarantineCountCache(t *testing.T) (c *diskSegmentCache, dir string, ids []searchengine.SegmentID) {
	t.Helper()
	dir = t.TempDir()
	c = newDiskSegmentCache(dir, 0, adviceRandom)
	for _, payload := range [][]byte{[]byte("alpha"), []byte("bravo")} {
		sum := sha256.Sum256(payload)
		id := hex.EncodeToString(sum[:])
		require.NoError(t, c.Put(id, payload))
		ids = append(ids, id)
	}
	require.Zero(t, c.quarantinedCount(), "a store that has withdrawn nothing reports zero, and this is that control")
	return c, dir, ids
}

func TestQuarantinedCount_CountsDistinctWithdrawals(t *testing.T) {
	c, _, ids := quarantineCountCache(t)

	require.NoError(t, c.Quarantine(ids[0], errors.New("injected: unreadable bytes")))
	require.Equal(t, 1, c.quarantinedCount(), "one withdrawal is one segment")

	// THE REPEAT IS THE POINT: the engine reports a corruption from every concurrent
	// query that touches the segment, so Quarantine is called many times for one id.
	// A counter incremented per call would multiply one loss by its report count.
	require.NoError(t, c.Quarantine(ids[0], errors.New("injected: reported again")))
	require.Equal(t, 1, c.quarantinedCount(), "a repeated report of one segment is still one withdrawal")

	require.NoError(t, c.Quarantine(ids[1], errors.New("injected: unreadable bytes")))
	require.Equal(t, 2, c.quarantinedCount(), "a second segment is a second withdrawal")
}

func TestQuarantinedCount_SurvivesARestart(t *testing.T) {
	c, dir, ids := quarantineCountCache(t)
	require.NoError(t, c.Quarantine(ids[0], errors.New("injected: unreadable bytes")))
	require.Equal(t, 1, c.quarantinedCount())

	// A SECOND CACHE OVER THE SAME ROOT is what a restart is: the quarantine
	// directory is the durable record, and the withdrawal is still in force because
	// scanExisting does not re-adopt a file that was moved into it.
	restarted := newDiskSegmentCache(dir, 0, adviceRandom)
	require.Equal(t, 1, restarted.quarantinedCount(),
		"the documents are still unreachable after a restart, so the reading must still say so")
	require.NotContains(t, restarted.Keys(), ids[0], "and the withdrawn segment is not served again")

	// THE KNOWN NEGATIVE, in the same instrument: a fresh root has nothing withdrawn.
	require.Zero(t, newDiskSegmentCache(t.TempDir(), 0, adviceRandom).quarantinedCount())
}

// TestQuarantinedCount_CountsAWithdrawalThatCouldNotBeMovedAside pins the harder
// half of the rule: what the reading reports is DOCUMENTS THIS PROCESS NO LONGER
// SERVES. A rename that fails still drops the index entry — the segment is withdrawn
// either way — so counting only the clean path would report a graph as whole while
// it was short.
func TestQuarantinedCount_CountsAWithdrawalThatCouldNotBeMovedAside(t *testing.T) {
	c, dir, ids := quarantineCountCache(t)

	// A FILE where the quarantine DIRECTORY must go: MkdirAll then fails, which is
	// the failure arm without needing a permission trick that root would defeat.
	require.NoError(t, os.WriteFile(filepath.Join(dir, quarantineDirName), []byte("not a directory"), 0o600))

	err := c.Quarantine(ids[0], errors.New("injected: unreadable bytes"))
	require.Error(t, err, "a quarantine that could not move the file aside reports its failure")
	require.Equal(t, 1, c.quarantinedCount(),
		"and still counts the withdrawal: the index entry is dropped, so those documents are gone from service")
	require.NotContains(t, c.Keys(), ids[0])
}

// TestQuarantinedCount_CountsAKnownIdWhoseFileHasVanished is the FOURTH drop-index
// path, and the one the count used to miss.
//
// IT IS A WITHDRAWAL BY THE SAME RULE AS THE OTHER THREE. The id is one this cache
// indexed, its file is gone from the root, and it was never moved into quarantine —
// so this call drops the index entry and this process stops serving those documents.
// A count that skipped it would report the graph as whole while it was short, which
// is the exact failure the other paths were written to avoid.
func TestQuarantinedCount_CountsAKnownIdWhoseFileHasVanished(t *testing.T) {
	c, dir, ids := quarantineCountCache(t)

	// The file vanishes underneath the cache — a damaged or externally-removed blob —
	// while the index entry remains.
	require.NoError(t, os.Remove(filepath.Join(dir, ids[0]+".seg")))
	require.Contains(t, c.Keys(), ids[0], "precondition: the index still holds the id whose file is gone")

	require.NoError(t, c.Quarantine(ids[0], errors.New("injected: unreadable bytes")),
		"withdrawing a known id whose file has already vanished is the idempotent path, not an error")
	require.Equal(t, 1, c.quarantinedCount(),
		"the index entry was dropped, so those documents are gone from service and the reading must say so")
	require.NotContains(t, c.Keys(), ids[0])
}
