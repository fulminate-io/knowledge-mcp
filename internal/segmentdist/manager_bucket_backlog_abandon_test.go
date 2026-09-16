// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_abandon_test.go — what an ABANDONED drain leaves behind
// (GitHub issue #172, requirement R5).
//
// The bootstrap package asserts the shutdown closure's side of this: which graph
// lands in the drain record's skipped list, and that the abandoned group published
// nothing. This asserts the manager's side, which the bootstrap fixture can only
// reach through a real deadline race: the BACKLOG SURVIVES. ReEmitDirtyBuckets
// consumes its snapshot only after both formats have drained and persisted, so a
// rebuild that returns a context error must leave every queued document and every
// sealed tail exactly where it found them.
//
// A drain that abandoned AND consumed would lose the window's writes silently —
// which is the failure the whole deferral design exists to avoid, arriving through
// a new door.

package segmentdist

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// abandonFormat is the vector engine's format name, spelled the way this package's
// other log-reading tests spell theirs.
const abandonFormat = "hnswv3"

func TestAbandonedDrainKeepsTheBacklogAndPublishesNothing(t *testing.T) {
	t.Parallel()

	gt, name := kgtypes.GraphCode, "abandonRepo"
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	docs := bothFormatDocs(64, "abandon-")

	require.NoError(t, mgr.AddAndMarkDirty(context.Background(), gt, name, docs))
	queued, _ := mgr.snapshotDirty(gt, name)
	require.Len(t, queued.pending, len(docs), "PRECONDITION: the window's documents must be queued for a re-emit")
	require.NotEmpty(t, queued.tails, "PRECONDITION: the window must have sealed at least one tail")
	residentBefore := mgr.ResidentSegmentCount(gt, name, abandonFormat)

	// A window that has already closed. The drain reaches the rebuild and abandons
	// there, which is the same arm a deadline expiring mid-rebuild takes.
	closed, cancel := context.WithCancel(context.Background())
	cancel()

	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	err := mgr.ReEmitDirtyBuckets(closed, gt, name)
	require.Error(t, err, "an abandoned rebuild must be reported, never returned as a silent success")
	require.ErrorIs(t, err, context.Canceled, "the abandon must carry the context error it came from")
	require.Contains(t, err.Error(), name, "the error must name the graph whose work was dropped")
	require.Contains(t, err.Error(), "partitions_not_rebuilt", "the error must name the work it did not do")
	// THE ABANDON PRECEDES THE EXPENSIVE GROUPING, and this is where that is
	// observable: deriving the work inputs walks every resident segment's members
	// and emits group_rebuild_begin before the engine is called at all. A drain that
	// checked its window only inside the engine would have paid that walk first.
	//
	// SCOPED TO THIS TEST'S OWN GRAPH, because the capture is the PROCESS-GLOBAL
	// slog default and seven sites in this package swap it while 78 files run in
	// parallel. A bare substring assertion over the raw buffer is racy in both
	// directions: a sibling's record reds it — main's CI failed exactly that way on
	// TestDeleteRecreateLifecycleStaysCoherent's `graph=knowledge
	// name=lifecycleCoherent` — and a sibling stealing the default between the swap
	// and the read greens it on an empty buffer. Selecting by graph identity is the
	// package's own idiom (diagRecord, group_rebuild_diag_test.go) and this is its
	// negative sibling.
	noDiagRecord(t, logs.String(), `msg="segmentdist: group_rebuild_begin"`, name, abandonFormat)

	after, _ := mgr.snapshotDirty(gt, name)
	require.Len(t, after.pending, len(queued.pending),
		"an abandoned drain must consume NOTHING; a consumed backlog is the window's writes lost without a record")
	require.Equal(t, queued.tails, after.tails, "and its sealed tails must survive with it")
	require.Equal(t, residentBefore, mgr.ResidentSegmentCount(gt, name, abandonFormat),
		"the group publishes all-or-nothing, so an abandoned rebuild must leave the resident set alone")

	// THE SAME-RUN KNOWN POSITIVE, and it now proves TWO things. The backlog must
	// drain when the window is open — without which "the backlog survived" would be
	// satisfied by a manager that cannot drain this graph at all. And the same open
	// window must emit a group_rebuild_begin record CARRYING THIS TEST'S IDENTITY,
	// which is what stops the scoped assertion above from being a matcher that
	// never matches anything: an identity selector that finds nothing reads as "the
	// abandon worked" and would pass just as happily against a key that does not
	// exist. The record is a real emission, never a planted one.
	require.NoError(t, mgr.ReEmitDirtyBuckets(context.Background(), gt, name))
	drained, _ := mgr.snapshotDirty(gt, name)
	require.Empty(t, drained.pending, "the same backlog must drain when the window is open")
	require.Empty(t, drained.tails)
	diagRecord(t, logs.String(), `msg="segmentdist: group_rebuild_begin"`, name, abandonFormat)
}

// noDiagRecord is diagRecord's negative: it asserts that NO captured line carries
// both the message and the given graph's identity.
//
// IT SELECTS THE SAME WAY diagRecord DOES, on repo= rather than name=, and that
// choice is read off the records rather than assumed: the emit site writes all
// three selector fields, and for a kgtypes.GraphCode target the selector carries
// the repository in Repo and leaves Name empty — the abandon test's own records
// read `graph=code name="" repo=abandonRepo format=hnswv3`. Matching on name=
// would find nothing and pass for the wrong reason.
func noDiagRecord(t *testing.T, logged, msg, graphName, format string) {
	t.Helper()
	var hits []string
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, msg) &&
			strings.Contains(line, "repo="+graphName) &&
			strings.Contains(line, "format="+format) {
			hits = append(hits, line)
		}
	}
	require.Emptyf(t, hits,
		"expected NO %s record for format=%s on repo=%s, found %d:\n%s",
		msg, format, graphName, len(hits), strings.Join(hits, "\n"))
}
