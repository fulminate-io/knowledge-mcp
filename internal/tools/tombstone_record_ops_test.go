// SPDX-License-Identifier: Apache-2.0

// tombstone_record_ops_test.go — the subtractive direction of the persisted tombstone
// record: an id a write has re-created must leave the record, the durability watermark
// must not move while it does, and the engines must be re-seeded even when the record
// held nothing to remove.

package tools

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

func TestUntombstoneWrittenIDs(t *testing.T) {
	const name = "untombstone"
	gt := kgtypes.GraphCode

	t.Run("removes_only_written_ids", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.watermark = 4242
		shipper.tombstoned = []searchengine.ExternalID{"a", "b", "c"}

		cleared, err := UntombstoneWrittenIDs(shipper, gt, name, []searchengine.ExternalID{"b"})
		require.NoError(t, err)
		require.Equal(t, 1, cleared)
		require.Equal(t, []searchengine.ExternalID{"a", "c"}, shipper.tombstoned,
			"only the written id may leave, and the survivors keep their order")
	})

	t.Run("watermark_is_not_advanced", func(t *testing.T) {
		// THE VIOLATING-INPUT GATE. The watermark is the rebuild's durability contract
		// and may move only when a publish landed; this call is not a publish.
		shipper := &fakeRebuildShipper{}
		shipper.watermark = 99887766
		shipper.tombstoned = []searchengine.ExternalID{"x", "y"}

		_, err := UntombstoneWrittenIDs(shipper, gt, name, []searchengine.ExternalID{"x"})
		require.NoError(t, err)
		require.Equal(t, int64(99887766), shipper.savedWatermark(),
			"the watermark must be written back exactly as it was read")
	})

	t.Run("no_intersection_reseeds_the_engine", func(t *testing.T) {
		// THE CATCHER for the engine-exceeds-record divergence. The record holds none of
		// the written ids — the state a rebuild leaves, because it seeds the engines with
		// the full carried union while the finalize persists only the retained subset. An
		// implementation that returned early here would leave the engines still filtering
		// the re-created document, which is the defect this whole path exists to fix.
		shipper := &fakeRebuildShipper{}
		shipper.watermark = 7
		shipper.tombstoned = []searchengine.ExternalID{"unrelated"}

		cleared, err := UntombstoneWrittenIDs(shipper, gt, name, []searchengine.ExternalID{"recreated"})
		require.NoError(t, err)
		require.Equal(t, 0, cleared)
		require.Equal(t, 0, shipper.saveCount(),
			"nothing left the record, so no record write is warranted")
		require.Len(t, shipper.seeded, 1,
			"the engines must be re-seeded even with no record intersection")
		require.Equal(t, []searchengine.ExternalID{"unrelated"}, shipper.seeded[0],
			"the re-seed carries the surviving set, converging the engines down to the record")
	})

	t.Run("cleared_count_excludes_unmatched_ids", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.tombstoned = []searchengine.ExternalID{"p", "q"}

		cleared, err := UntombstoneWrittenIDs(shipper, gt, name,
			[]searchengine.ExternalID{"p", "never-was-tombstoned"})
		require.NoError(t, err)
		require.Equal(t, 1, cleared,
			"the count reports what actually left the record, not what the caller asked about")
	})

	t.Run("unreadable_record_declines", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.loadErr = errors.New("record unreadable")

		cleared, err := UntombstoneWrittenIDs(shipper, gt, name, []searchengine.ExternalID{"z"})
		require.Error(t, err)
		require.Equal(t, 0, cleared)
		require.Equal(t, 0, shipper.saveCount(),
			"rewriting a set we could not read would drop the ids it held")
		require.Empty(t, shipper.seeded,
			"a declined pass must not touch the engines either")
	})
}
