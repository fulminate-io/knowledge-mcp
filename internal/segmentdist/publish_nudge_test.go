// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCoverageSkipNudgesOnce pins the reconcile-nudge contract: a publish whose
// coverage gate cannot be satisfied asks a periodic reconcile consumer to look
// sooner, and it asks EXACTLY ONCE per suppression episode — on the streak's
// transition into suppression, not on every suppressing skip.
//
// The discriminating assertion is that ONE entry comes back after MANY skips. The
// most likely wrong implementation nudges whenever the skip is suppressed, which is
// tempting because that condition is already computed and is true on every skip past
// the bound; against that implementation the count assertion fails. The drain-then-
// empty and re-arm halves pin the rest of the contract: a consumed recording does not
// re-fire, and a fresh episode after a genuine resident rise records again.
//
// Fixture: the established restarted-writer shape — writer B ships a prior corpus so
// the coverage ratio is armed, writer A restarts with a fresh L2 and force-seals a
// sub-floor tail WITHOUT loading that corpus, so its resident sits far below the
// ratio and every re-fired publish skips.
//
// DELIBERATELY NOT PARALLEL. Shared resource: the process-global default slog
// logger, which this test swaps for a capturing handler to assert over the
// records the path emits. Concurrent peers would both install and restore that
// one global, so the handler this test reads could be a peer's, and a peer's
// unrelated records would land in this test's capture.
func TestCoverageSkipNudgesOnce(t *testing.T) {
	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	b := mgrs[1]
	gt, name := kgtypes.GraphCode, "nudgeRepo"

	// B ships a prior corpus across two force-sealed segments. The docs are kept
	// well under MinSegmentDocs and sealed with Flush so the fixture stays cheap —
	// what the ratio needs is a shipped total at or above the floor, not a large one.
	for s := range 2 {
		batch := prefixIDs(hnswVecDocs(200), fmt.Sprintf("nu-b%d-", s))
		require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, batch))
		require.NoError(t, b.Flush(ctx, gt, name))
	}

	// A restarts (same writer_id, fresh L2) and force-seals a sub-floor tail without
	// loading the prior corpus: resident 10 against a 400-doc shipped corpus, so the
	// publish is coverage-skipped and stays skipped however often it re-fires.
	aRestart := restartFleetMember(t, svc, 0, t.TempDir())
	tail := prefixIDs(hnswVecDocs(10), "nu-tail-")
	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, tail)) // force-seals, ships nothing
	require.NoError(t, aRestart.Flush(ctx, gt, name))                 // ship → skip #1
	aDM := aRestart.managerFor(gt, name)
	require.True(t, aDM.publishRetryPending(), "the first coverage skip armed the retry bit")

	// Re-fire the publish repeatedly WITHOUT healing the resident. Each re-fired
	// Flush is driven only by the pending retry bit and skips again, so the streak
	// climbs well past the bound — many suppressing skips, one transition.
	const extraFlushes = 8
	for range extraFlushes {
		require.NoError(t, aRestart.Flush(ctx, gt, name))
	}
	require.False(t, aDM.publishRetryPending(),
		"the streak crossed the suppression bound (so MANY suppressing skips occurred)")

	// THE DISCRIMINATOR: one entry for the graph, not one per suppressing skip.
	nudged := aRestart.TakeReconcileNudges()
	require.Len(t, nudged, 1,
		"a suppression episode records the graph exactly once, however many skips it spans")
	require.Equal(t, NudgedGraph{GraphType: gt, Name: name}, nudged[0],
		"the recorded entry identifies the graph that asked for the earlier look")

	// Drain-on-read: the consumed recording does not re-fire.
	require.Empty(t, aRestart.TakeReconcileNudges(),
		"the set drains on read, so a consumed recording cannot re-fire forever")

	// RE-ARM. A genuine resident rise ends the episode: the cheap re-import climbs the
	// resident above the floor and a fresh sealed segment then publishes successfully,
	// resetting the streak.
	degenerate, err := aRestart.ReconcileResidentDegenerate(ctx, gt, name)
	require.NoError(t, err)
	require.False(t, degenerate, "the re-import restored coverage")
	require.GreaterOrEqual(t, aRestart.ResidentDocCount(gt, name), residentBackstopFloor,
		"resident climbed above the floor after the re-import")
	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(200), "nu-heal-")))
	require.NoError(t, aRestart.Flush(ctx, gt, name))
	require.False(t, aDM.publishRetryPending(),
		"the healed publish landed, ending the episode")
	require.Empty(t, aRestart.TakeReconcileNudges(),
		"a landing publish records nothing — only a suppression transition does")

	// B now ships far more than A holds resident, so A's next publish is back below
	// the ratio and a SECOND episode begins.
	require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(2000), "nu-b2-")))
	require.NoError(t, b.Flush(ctx, gt, name))
	require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(100), "nu-tail2-")))
	for range 1 + extraFlushes {
		require.NoError(t, aRestart.Flush(ctx, gt, name))
	}
	require.False(t, aDM.publishRetryPending(), "the second episode crossed the bound too")

	reNudged := aRestart.TakeReconcileNudges()
	require.Len(t, reNudged, 1, "the recording RE-ARMS for a fresh episode after a resident rise")
	require.Equal(t, NudgedGraph{GraphType: gt, Name: name}, reNudged[0])

	// THE TRANSITION-vs-EVERY-SUPPRESSING-SKIP DISCRIMINATOR. The assertions above
	// cannot make it: the set is keyed by graph, so many recordings for one graph
	// collapse to one entry either way, and the publish path structurally cannot
	// deliver a second suppressing skip inside one episode — the skip that first
	// suppresses is also the one that clears the retry bit driving the re-fire, and
	// anything that re-fires the publish afterwards seals new content, which raises
	// the resident and starts a fresh episode.
	//
	// So the state is established at the gate itself: a fresh engine with a resident
	// of zero can be driven past the bound repeatedly WITHOUT the resident ever
	// rising. After the episode's transition is consumed, further suppressing skips
	// must record and wake NOTHING. That is red against a detector that fires on
	// every suppressing skip, and green on one that fires on the edge.
	t.Run("no_second_record_within_one_episode", func(t *testing.T) {
		_, gc := newSegmentHarness(t)
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		gt, name := kgtypes.GraphCode, "oneEpisodeRepo"
		dm := mgr.managerFor(gt, name)

		// Resident is 0 and never rises, so the streak climbs uninterrupted: the
		// transition lands on the skip that first passes the bound.
		for range coverageSkipMaxStreak + 1 {
			dm.markCoverageSkip()
		}
		select {
		case <-mgr.ReconcileNudge():
		default:
			t.Fatal("the suppression transition must wake the consumer")
		}
		require.Equal(t, []NudgedGraph{{GraphType: gt, Name: name}}, mgr.TakeReconcileNudges(),
			"the transition recorded the graph")

		// SAME episode, resident still not risen: every further skip is suppressing
		// but none is the transition, so none may record or wake.
		for range 5 {
			dm.markCoverageSkip()
		}
		select {
		case <-mgr.ReconcileNudge():
			t.Fatal("a suppressing skip that is not the episode's transition must not re-wake the consumer")
		default:
		}
		require.Empty(t, mgr.TakeReconcileNudges(),
			"a suppressing skip that is not the episode's transition records nothing")
	})

	// A graph has THREE engines that can reach the publish coverage gate — the embed
	// HNSW engine, the deterministic HNSW engine, and the BM25 engine — each with its
	// OWN skip streak, so each can cross its own transition independently for the SAME
	// graph. "Exactly once" is therefore a per-KEY property, not a per-engine one.
	// Without this subtest the assertions above would also be satisfied by an
	// implementation that only ever records from one engine, which would be a false
	// guarantee: two engines collapsing to one entry is the actual contract.
	t.Run("two_engines_collapse", func(t *testing.T) {
		warns := installCapturingSlog(t)
		ctx := context.Background()
		mgrs, svc := newMultiWriterFleet(t, 2)
		b := mgrs[1]
		gt, name := kgtypes.GraphCode, "twoEngineRepo"

		// B ships a prior corpus in BOTH formats so BOTH coverage ratios are armed.
		for s := range 2 {
			require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(200), fmt.Sprintf("te-h%d-", s))))
			require.NoError(t, b.AddAndMarkDirtyFields(ctx, gt, name, prefixIDs(bm25FieldDocs(200), fmt.Sprintf("te-f%d-", s))))
			require.NoError(t, b.Flush(ctx, gt, name))
		}

		// A restarts with a fresh L2 and force-seals a sub-floor tail in BOTH formats,
		// so BOTH engines skip their publish and both climb their own streaks.
		aRestart := restartFleetMember(t, svc, 0, t.TempDir())
		require.NoError(t, aRestart.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(10), "te-htail-")))
		require.NoError(t, aRestart.AddAndMarkDirtyFields(ctx, gt, name, prefixIDs(bm25FieldDocs(10), "te-ftail-")))
		for range 1 + extraFlushes {
			require.NoError(t, aRestart.Flush(ctx, gt, name))
		}

		// BOTH engines genuinely reached suppression — asserted on the per-format
		// terminal WARN, so the subtest cannot pass with only one engine suppressing
		// (which is exactly the shape it exists to rule out).
		suppressed := warns.warnsContaining("suspending coverage-skip republish")
		require.True(t, anyContains(suppressed, "format=hnsw"),
			"the HNSW engine crossed its own suppression bound")
		require.True(t, anyContains(suppressed, "format=bm25"),
			"the BM25 engine crossed its own suppression bound")

		// ...and the two collapse onto ONE entry, because the set is keyed by graph.
		nudged := aRestart.TakeReconcileNudges()
		require.Len(t, nudged, 1,
			"two engines suppressing for one graph collapse to a single recorded entry")
		require.Equal(t, NudgedGraph{GraphType: gt, Name: name}, nudged[0])
	})
}

// anyContains reports whether any of the rendered records contains substr — the
// per-format check the two-engine subtest makes over the captured suppression WARNs.
func anyContains(records []string, substr string) bool {
	for _, r := range records {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}
