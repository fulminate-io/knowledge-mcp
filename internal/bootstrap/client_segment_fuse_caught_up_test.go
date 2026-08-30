// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// TestFuseCaughtUp_DeclinesWhenBehindOrUnseeded drives every state of the
// comparison. It uses a REAL segment manager over a temp dir and writes the
// watermark through the manager's own SaveMergeWatermark, so the left operand is
// produced by the code that produces it in production rather than by a double
// taught to agree with the predicate.
//
// THE PREDICATE IS TESTED, NOT THE VERDICT. This condition is one conjunct of the
// exact per-arm balance verdict, which does not exist yet — asserting verdict
// behaviour here would be asserting something unbuilt.
func TestFuseCaughtUp_DeclinesWhenBehindOrUnseeded(t *testing.T) {
	ctx := context.Background()
	const gt = kgtypes.GraphCode

	// newClient builds a client with a REAL segment manager over a temp dir, so the
	// LEFT operand — the local merge watermark — is written and read by the same
	// production code that owns it in the daemon. Only the RIGHT operand (the server
	// change stamp) is injected, because it is produced on another machine and the
	// predicate's whole job is to compare against whatever value arrives.
	newClient := func(t *testing.T) *client {
		t.Helper()
		return &client{segmentMgr: segmentdist.NewManager(t.TempDir(), 0)}
	}

	t.Run("no_stamp_reader_declines_rather_than_defaulting", func(t *testing.T) {
		c := newClient(t)
		ok, reason, err := c.fuseCaughtUp(ctx, gt, "noreader")
		require.NoError(t, err)
		require.False(t, ok, "with no server stamp observable the predicate must decline")
		require.NotEmpty(t, reason, "a decline must say why")
	})

	t.Run("unsampled_graph_declines_and_is_not_a_zero_stamp", func(t *testing.T) {
		c := newClient(t)
		c.serverSegmentStamp = func(kgtypes.GraphType, string) (int64, bool) { return 0, false }
		require.NoError(t, c.segmentMgr.SaveMergeWatermark(gt, "unsampled", 5_000))

		ok, reason, err := c.fuseCaughtUp(ctx, gt, "unsampled")
		require.NoError(t, err)
		require.False(t, ok,
			"a graph the poll has not sampled yet tells us nothing about the server side, "+
				"so no comparison may be made — and it must NOT be read as a zero stamp, which "+
				"a non-zero watermark would then trivially exceed")
		require.Contains(t, reason, "not yet sampled")
	})

	// The remaining three states need a SAMPLED graph, i.e. a reader that answers
	// (stamp, true). The stamp value itself is server-produced in production, so
	// supplying it here is substituting a DEPENDENCY, not the code under test — the
	// comparison is what is being tested, and it is the client's own.
	seeded := func(t *testing.T, serverStamp int64) *client {
		t.Helper()
		c := newClient(t)
		c.serverSegmentStamp = func(kgtypes.GraphType, string) (int64, bool) { return serverStamp, true }
		return c
	}

	t.Run("zero_watermark_unseeded_graph_declines", func(t *testing.T) {
		c := seeded(t, 0)
		// The watermark is left unwritten: this graph has fused nothing.
		ok, reason, err := c.fuseCaughtUp(ctx, gt, "unseeded")
		require.NoError(t, err)
		require.False(t, ok,
			"a graph that has fused nothing must not be judged caught up — not even against a "+
				"zero server stamp, because 'neither side has anything' is not evidence that "+
				"this client is current with a corpus it has never read")
		require.Contains(t, reason, "fused nothing")
	})

	t.Run("watermark_behind_the_stamp_declines_with_a_reason", func(t *testing.T) {
		c := seeded(t, 9_000)
		require.NoError(t, c.segmentMgr.SaveMergeWatermark(gt, "behind", 4_000))

		ok, reason, err := c.fuseCaughtUp(ctx, gt, "behind")
		require.NoError(t, err)
		require.False(t, ok, "a watermark behind the server stamp is real lag")
		require.Contains(t, reason, "behind the server change stamp")
		require.Contains(t, reason, "4000")
		require.Contains(t, reason, "9000",
			"the reason must name BOTH operands, or an operator cannot tell how far behind")
	})

	t.Run("watermark_at_or_past_the_stamp_passes", func(t *testing.T) {
		// AT the stamp.
		c := seeded(t, 7_000)
		require.NoError(t, c.segmentMgr.SaveMergeWatermark(gt, "at", 7_000))
		ok, reason, err := c.fuseCaughtUp(ctx, gt, "at")
		require.NoError(t, err)
		require.True(t, ok, "equal watermark and stamp is caught up")
		require.Empty(t, reason)

		// PAST the stamp — the ordinary steady state, because the watermark advances
		// to the served safe horizon and so routinely sits ahead of the newest change.
		// An equality test would fail here, which is why the comparison is >=.
		c2 := seeded(t, 7_000)
		require.NoError(t, c2.segmentMgr.SaveMergeWatermark(gt, "past", 9_500))
		ok, reason, err = c2.fuseCaughtUp(ctx, gt, "past")
		require.NoError(t, err)
		require.True(t, ok,
			"the local watermark is the SERVED SAFE HORIZON and routinely sits PAST the newest "+
				"change stamp; an equality test would essentially never hold and the verdict "+
				"composing this would never fire")
		require.Empty(t, reason)
	})
}
