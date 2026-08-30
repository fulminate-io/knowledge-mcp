// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestQuiescentBothAxes_EpochKeyed drives the predicate over its whole state
// space. The cases are not variations on one theme — each is a distinct way the
// cross-axis question can be answered wrongly:
//
//   - both running axes stamped at the CURRENT epoch → fires;
//   - one axis unstamped → does not (it has not reported a drain at all);
//   - one axis carrying a PREVIOUS epoch's stamp → does not (that is the staleness
//     the epoch-keying exists to catch, and a plain bool would fire here);
//   - one axis DISABLED, the other stamped → fires (a disabled axis runs no loop,
//     so its stamp would never move and the predicate could otherwise never fire);
//   - NO epoch factory wired → does not, even with both axes stamped.
func TestQuiescentBothAxes_EpochKeyed(t *testing.T) {
	// newC builds a collector with both axes enabled and an epoch source the test
	// controls. epoch is read through a pointer so a case can advance it between
	// stamping and asking, which is exactly the collect-lands-between sequence.
	newC := func(epoch *uint64, summaryEnabled, embedEnabled bool) *collector {
		c := &collector{
			gt: kgtypes.GraphCode, name: "repo",
			summaryEnabled: summaryEnabled, embedEnabled: embedEnabled,
		}
		if epoch != nil {
			c.collectEpoch = func() uint64 { return *epoch }
		}
		return c
	}

	t.Run("both_axes_stamped_at_current_epoch_fires", func(t *testing.T) {
		e := uint64(3)
		c := newC(&e, true, true)
		c.summaryDrainedAtEpoch.Store(e + 1)
		c.embedDrainedAtEpoch.Store(e + 1)
		require.True(t, c.quiescentBothAxes(),
			"both running axes reported a complete empty drain at the current epoch")
	})

	t.Run("one_axis_unstamped_does_not_fire", func(t *testing.T) {
		e := uint64(3)
		c := newC(&e, true, true)
		c.summaryDrainedAtEpoch.Store(e + 1)
		// embed left at its zero value: it has not reported a drain.
		require.False(t, c.quiescentBothAxes(),
			"an axis that has never reported a drain cannot be assumed drained")
	})

	t.Run("empty_but_incomplete_axis_does_not_fire", func(t *testing.T) {
		// The runLoop write site stores 0 unless setComplete held, so an axis whose
		// page was empty-but-truncated is indistinguishable here from an unstamped
		// one — which is the intent: neither is a drain.
		e := uint64(3)
		c := newC(&e, true, true)
		c.summaryDrainedAtEpoch.Store(e + 1)
		c.embedDrainedAtEpoch.Store(0)
		require.False(t, c.quiescentBothAxes(),
			"an empty page that was NOT the complete gap set is not a drain")
	})

	t.Run("previous_epoch_stamp_does_not_fire", func(t *testing.T) {
		// THE CASE A PLAIN BOOL GETS WRONG. Both axes genuinely drained — at epoch 3.
		// Then a collect landed, taking the epoch to 4. Their observations are now
		// about a corpus that has since gained rows, and must expire on their own
		// because neither loop may have iterated since.
		e := uint64(3)
		c := newC(&e, true, true)
		c.summaryDrainedAtEpoch.Store(e + 1)
		c.embedDrainedAtEpoch.Store(e + 1)
		require.True(t, c.quiescentBothAxes(), "precondition: quiescent before the collect")

		e = 4 // a collect completed
		require.False(t, c.quiescentBothAxes(),
			"stamps from BEFORE a completed collect must not read as a drain of the corpus "+
				"that collect just added to — this is the exact false-unhealthy the epoch "+
				"keying exists to prevent, and a plain per-axis bool fires here")
	})

	t.Run("disabled_axis_is_vacuously_drained", func(t *testing.T) {
		e := uint64(7)
		c := newC(&e, false, true) // no summarizer configured: run() launches no summary loop
		c.embedDrainedAtEpoch.Store(e + 1)
		// summaryDrainedAtEpoch stays 0 forever because nothing ever writes it.
		require.True(t, c.quiescentBothAxes(),
			"an axis this collector does not RUN has no work to drain, so its permanently "+
				"zero stamp must not veto quiescence — otherwise a graph with no summarizer "+
				"could never be quiescent")

		c2 := newC(&e, true, false)
		c2.summaryDrainedAtEpoch.Store(e + 1)
		require.True(t, c2.quiescentBothAxes(), "the mirror case with embed disabled")
	})

	t.Run("no_epoch_source_declines_even_when_both_stamped", func(t *testing.T) {
		c := newC(nil, true, true) // nil collectEpoch: router-less / degraded client
		c.summaryDrainedAtEpoch.Store(1)
		c.embedDrainedAtEpoch.Store(1)
		require.False(t, c.quiescentBothAxes(),
			"with NO epoch source a stamp can never expire, so absent stamps must NOT be "+
				"read as agreement — declining is a refusal to assert, and it is the correct "+
				"answer on the deployment with nobody watching")
	})
}
