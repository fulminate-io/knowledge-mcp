// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_delta_watermark_log_test.go — the delta pass's instrument.
//
// WHAT IT IS FOR. The pass used to log one operand, `since`, which does not scope
// the scan, and it logged NOTHING AT ALL on the branch that merged nothing — the
// converged steady state, which is exactly the state an operator has to diagnose.
// Two investigations chased an advancing-horizon puzzle against that instrument.
// The two wire values are now carried back from the pass that SENT them and printed
// beside `since` on BOTH branches.
//
// THE CAPTURE REPLACES THE PROCESS-GLOBAL DEFAULT LOGGER, so this test must never
// call t.Parallel().

// deltaLogFixture is the two watermarks one graph's fixture pins: a rebuild
// position deliberately BELOW the merge horizon, so the floor the pass reports and
// the bound it scans from are different numbers and a log line printing one of them
// twice is visible.
type deltaLogFixture struct {
	repo         string
	rebuildPos   int64
	mergeHorizon int64
}

// driveDeltaPass builds a client for one graph, seeds both durable positions, points
// the fake at corpus rows stamped above the horizon (or at none), and runs one real
// consumeSegmentDelta.
func driveDeltaPass(t *testing.T, fx deltaLogFixture, rows int) mergePending {
	t.Helper()
	c, eng := buildSeedRebuildClient(t, fx.repo)
	require.NoError(t, c.segmentMgr.SaveRebuildState(kgtypes.GraphCode, fx.repo, fx.rebuildPos, nil))
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, fx.repo, fx.mergeHorizon))

	corpus := make([]stampedNode, 0, rows)
	for i := range rows {
		corpus = append(corpus, stampedNode{
			id:        fx.repo + "-node-" + string(rune('a'+i)),
			stampedAt: fx.mergeHorizon + int64(i) + 1,
			vector:    seedFixtureVector(byte('a' + i)),
		})
	}
	eng.mu.Lock()
	eng.corpus = corpus
	eng.servedHorizon = fx.mergeHorizon + int64(rows) + 10
	eng.mu.Unlock()

	return c.consumeSegmentDelta(context.Background(), segmentGraphRef{gt: kgtypes.GraphCode, name: fx.repo}, nil)
}

// TestSegmentDeltaLogsBothWireWatermarks pins E4: BOTH branches of the pass emit a
// record, and each carries the two values the request actually went out with.
func TestSegmentDeltaLogsBothWireWatermarks(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	merging := deltaLogFixture{repo: "deltalogmerging", rebuildPos: 1_000_000_000, mergeHorizon: 2_000_000_000}
	quiet := deltaLogFixture{repo: "deltalogquiet", rebuildPos: 3_000_000_000, mergeHorizon: 4_000_000_000}

	// FIXTURE CONTROL FOR THE WHOLE TEST: the reported floor and the scan bound must
	// be DIFFERENT numbers on each graph, or a line printing one value twice would
	// satisfy every assertion below.
	for _, fx := range []deltaLogFixture{merging, quiet} {
		require.NotEqual(t, fx.rebuildPos, fx.mergeHorizon,
			"fixture control: %s must pin a rebuild position below its merge horizon", fx.repo)
	}

	merged := driveDeltaPass(t, merging, 3)
	require.Positive(t, merged.Merged,
		"fixture control: the merging graph must actually merge rows, or the merged-rows branch never ran")

	none := driveDeltaPass(t, quiet, 0)
	require.True(t, none.Pull,
		"fixture control: the quiet graph must have PULLED — a graph that never pulled takes the "+
			"no-horizon path and would exercise neither branch")
	require.Zero(t, none.Merged, "fixture control: the quiet graph must merge nothing")

	logged := buf.String()

	// THE MERGED-ROWS BRANCH.
	mergedLine := lineContaining(t, logged, "merged co-worker updates")
	require.Contains(t, mergedLine, "local_slowest_consumer_floor_nanos=1000000000",
		"the merged-rows record must carry the FLOOR the request reported, which is the rebuild position "+
			"here because it lags the merge horizon")
	require.Contains(t, mergedLine, "scan_from_nanos=2000000000",
		"and the BOUND the scan actually read from, which is this consumer's own horizon — printing the "+
			"floor twice is the defect this line replaces")

	// THE MERGED-NOTHING BRANCH. Its existence is the assertion: an implementation
	// that only extended the existing Info line leaves the converged steady state
	// logging nothing at all, and goes red here.
	quietLine := lineContaining(t, logged, "segment delta pass merged nothing")
	require.Contains(t, quietLine, "local_slowest_consumer_floor_nanos=3000000000")
	require.Contains(t, quietLine, "scan_from_nanos=4000000000")

	// AND BOTH KEEP `since` BESIDE THE WIRE VALUES. The whole diagnostic value is
	// seeing the caller's horizon and what was sent side by side.
	require.Contains(t, mergedLine, "since=")
	require.Contains(t, quietLine, "since=")
}

// lineContaining returns the single logged record containing needle, failing the
// test when there is none — so a missing record reads as a missing record rather
// than as an empty-string assertion that passes by accident.
func lineContaining(t *testing.T, logged, needle string) string {
	t.Helper()
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	require.FailNowf(t, "no log record matched", "no record containing %q in:\n%s", needle, logged)
	return ""
}
