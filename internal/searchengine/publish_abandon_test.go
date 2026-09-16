// SPDX-License-Identifier: Apache-2.0

// publish_abandon_test.go — what a GROUP SWAP does when its window closes
// (GitHub issue #172, requirement R5).
//
// The shutdown drain's own tests live in the bootstrap and segmentdist packages,
// where the deadline comes from. These are the engine's three context checks,
// each observed on its own: the one before the constituents are resolved, the one
// inside the harvest pool, and the one before the compare-and-swap. They are
// separated from the route matrix next door because they are about ABANDONING a
// rebuild rather than about what the route answers.

package searchengine

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// cancelOnBuildFormat is mockFormat with one addition: once ARMED, the next
// Build closes the caller's window. It exists to put the shutdown deadline in the
// one place a fixture cannot otherwise reach — after every partition has been
// harvested and before the group publishes.
type cancelOnBuildFormat struct {
	mockFormat
	armed  *atomic.Bool
	cancel context.CancelFunc
}

func (f cancelOnBuildFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	seg, rep, err := f.mockFormat.Build(docs)
	if f.armed.Load() {
		f.cancel()
	}
	return seg, rep, err
}

// TestGroupSwapAbandonsBeforeThePublish observes the LAST of the three context
// checks in ReplaceBucketGroup on its own.
//
// The other two are reachable from any fixture that starts with a closed window.
// This one is not: it guards the interval between the final harvest and the
// compare-and-swap, so a test has to close the window inside the last build. It is
// a single-partition group precisely so that the per-partition check in the
// harvest pool has already passed when the window closes — otherwise the abandon
// would come from there and this guard would be unobserved.
func TestGroupSwapAbandonsBeforeThePublish(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	armed := &atomic.Bool{}
	f := cancelOnBuildFormat{armed: armed, cancel: cancel}

	e := closeOnCleanup(t, New[mockQuery, mockStats](f, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	seeded, err := e.AddSealAndSupersede([]Document{doc("swap-a", "x"), doc("swap-b", "x")})
	require.NoError(t, err)
	before := e.ResidentSegmentIDs()
	require.Len(t, before, 1, "PRECONDITION: one resident constituent to rebuild")

	armed.Store(true)
	published, _, err := e.ReplaceBucketGroup(ctx, 1, []SegmentID{seeded.ID},
		[]BucketWork{{Bucket: 0, Docs: []Document{doc("swap-c", "x")}}})

	require.ErrorIs(t, err, context.Canceled,
		"a window that closed while the group was building must abandon before the swap")
	require.Empty(t, published, "an abandoned group publishes no partition")
	require.Equal(t, before, e.ResidentSegmentIDs(),
		"and it must leave the resident set exactly as it found it — publishing the partitions that finished is the data-loss shape the all-or-nothing contract exists to prevent")

	// AND THE FIRST CHECK, ON ITS OWN. A group entered with the window already
	// closed must not even RESOLVE its constituents: resolving is a walk over the
	// snapshot's entries, which at the segment counts a merge-disabled engine
	// reaches between drains is real work, and the caller that supplies a closed
	// window is a process on its way out.
	t.Run("a_group_entered_with_the_window_already_closed_resolves_nothing", func(t *testing.T) {
		closed, closeNow := context.WithCancel(t.Context())
		closeNow()
		_, stats, err := e.ReplaceBucketGroup(closed, 1, []SegmentID{seeded.ID},
			[]BucketWork{{Bucket: 0, Docs: []Document{doc("swap-d", "x")}}})
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, stats.ResolvedSegments,
			"the check must precede the resolve; a group that resolved first has already paid the walk it was meant to skip")
	})
}

// countingCancelFormat is mockFormat that COUNTS its builds and closes the
// caller's window on the first one.
type countingCancelFormat struct {
	mockFormat
	builds *atomic.Int64
	cancel context.CancelFunc
}

func (f countingCancelFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	seg, rep, err := f.mockFormat.Build(docs)
	if f.builds.Add(1) == 1 {
		f.cancel()
	}
	return seg, rep, err
}

// TestGroupHarvestStopsTakingPartitionsWhenTheWindowCloses is what the
// per-partition check inside the harvest pool BUYS, asserted as a COUNT rather
// than as a clock: a wall-clock bound on a loaded runner measures the runner.
//
// A group that checked its window only at the ends would harvest every partition
// it was given and then throw the whole result away, so the cost of abandoning
// would be the cost of the entire rebuild — which on the reported host was
// 213 seconds. With the check inside the pool, the window closing during the first
// partition's build stops the queue: the only partitions that still get built are
// the ones already in flight, one per worker.
func TestGroupHarvestStopsTakingPartitionsWhenTheWindowCloses(t *testing.T) {
	const partitions = 64

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	builds := &atomic.Int64{}
	f := countingCancelFormat{builds: builds, cancel: cancel}

	e := closeOnCleanup(t, New[mockQuery, mockStats](f, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))

	work := make([]BucketWork, 0, partitions)
	for b := range partitions {
		work = append(work, BucketWork{Bucket: b, Docs: []Document{doc(fmt.Sprintf("part%03d", b), "x")}})
	}

	_, _, err := e.ReplaceBucketGroup(ctx, partitions, nil, work)
	require.ErrorIs(t, err, context.Canceled)

	built := builds.Load()
	t.Logf("partitions offered=%d built before the abandon=%d (workers=%d)", partitions, built, runtime.NumCPU())
	require.Positive(t, built, "PRECONDITION: at least the first partition must have been built, or nothing closed the window")
	require.LessOrEqual(t, built, int64(runtime.NumCPU()),
		"a group whose window closed during its first partition must stop TAKING partitions; building all %d is the whole rebuild paid for nothing, which is what makes an abandoned shutdown cost as much as a completed one",
		partitions)
}
