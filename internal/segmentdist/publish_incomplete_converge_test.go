// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// missingOf returns the subset of ids the server store does NOT currently hold for
// target — the in-memory model of the agent manifest/publish HEAD-verify's
// genuine-absence report. It powers fakeSegmentSource's verifyPublishCompleteness 409.
func (f *sharedServerFake) missingOf(target *knowledgev1.GraphSelector, ids []searchengine.SegmentID) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	have := map[string]bool{}
	for _, b := range f.byKey[f.key(target)] {
		have[b.GetId()] = true
	}
	var missing []string
	for _, id := range ids {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// serverHolds reports whether the shared server-fake store currently holds a blob id
// for target — the read side of the orphan-sweep + re-upload assertions below.
func serverHolds(svc *sharedServerFake, target *knowledgev1.GraphSelector, id searchengine.SegmentID) bool {
	for _, m := range svc.listMetas(target) {
		if m.GetId() == id {
			return true
		}
	}
	return false
}

// TestShipAndPublishReuploadsAbsentBlobAndConverges is the incomplete-publish acceptance fixture:
// a stamped-but-absent-server-side blob RE-UPLOADS and the manifest CONVERGES within one
// retry cycle. It drives the real ship/publish path over a fake source whose
// PublishManifest performs the agent's genuine-absence HEAD-verify (verifyPublishCompleteness),
// so the 409 is produced by an actually-absent referenced blob rather than a canned error.
//
// RED before the un-stamp fix: the 409 leaves the victim stamped, so the retry pass's
// ship diff skips it, it is never re-uploaded (gc.shipCalls does not climb), the
// republish 409s again, and the swap never lands — the permanent wedge.
func TestShipAndPublishReuploadsAbsentBlobAndConverges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, gc := newSegmentHarness(t)
	// GCS-shaped source: the agent is the completeness authority (subset check skipped)
	// AND the publish HEAD-verifies, 409ing on any referenced blob the store lacks.
	gc.verifies = true
	gc.verifyPublishCompleteness = true
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	gt, name := kgtypes.GraphCode, "reupload-converge"

	// Seed a published corpus. Every seeded blob lands in the store and is stamped
	// shipped, and the HEAD-verifying publish succeeds because all are present.
	seedShipped(t, ctx, mgr, gt, name, vecContentDocs(2048))
	dm := mgr.managerFor(gt, name)
	require.False(t, dm.publishRetryPending(), "the seed published cleanly")
	require.NotEmpty(t, residentIDs(dm), "the seed left segments resident")

	// ORPHAN-SWEEP SIMULATION: delete one resident blob from the store while it stays
	// stamped + resident on the client. This is the exact wedge — the manifest the
	// client publishes references a blob the server no longer holds.
	var victim searchengine.SegmentID
	for id := range residentIDs(dm) {
		victim = id
		break
	}
	require.NotEmpty(t, victim)
	require.Equal(t, 1, svc.prune(gc.target, []searchengine.SegmentID{victim}),
		"the orphan sweep removed exactly the victim blob server-side")
	require.False(t, serverHolds(svc, gc.target, victim), "the victim is absent server-side after the sweep")

	// PASS 1 — a publish of the resident set now references the absent victim, so the
	// agent 409s. The ship diff still skips the victim (it is stamped), so this pass
	// does NOT re-upload it; the 409 handler un-stamps it for the next pass.
	shipsBefore := gc.shipCalls.Load()
	_, err := dm.shipAndPublish(ctx, dm.locallyShipped)
	require.NoError(t, err, "a 409 incomplete publish is a logged skip, not a hard error")
	require.True(t, dm.publishRetryPending(), "the 409 armed the retry")
	require.Equal(t, shipsBefore, gc.shipCalls.Load(), "pass 1 ships nothing new — the diff still skips the stamped victim")
	require.False(t, serverHolds(svc, gc.target, victim), "the victim is still absent after pass 1")

	// PASS 2 — CONVERGENCE. The un-stamp put the victim back in the ship diff, so this
	// pass RE-SHIPS it (the store holds it again) and the republish passes the
	// HEAD-verify: the manifest swaps and the retry bit clears. Within one retry cycle.
	swapsBefore := dm.completedSwapCount()
	_, err = dm.shipAndPublish(ctx, dm.locallyShipped)
	require.NoError(t, err)
	require.Greater(t, gc.shipCalls.Load(), shipsBefore, "the retry pass RE-SHIPPED the un-stamped victim")
	require.True(t, serverHolds(svc, gc.target, victim), "the re-upload restored the victim server-side")
	require.Greater(t, dm.completedSwapCount(), swapsBefore, "the manifest swap landed — the wedge converged")
	require.False(t, dm.publishRetryPending(), "convergence cleared the retry bit")
}

// TestPublishIncompleteEscalatesToLoudWarnWhenPersistent pins item 3: a 409 that keeps
// recurring (the re-upload is not sticking) escalates from the per-cycle transient skip
// WARN to a LOUD persistent-degradation WARN once the consecutive streak reaches
// incompletePublishWarnStreak — and, unlike the coverage-ratio bound, the retry stays
// ARMED throughout (the 409 cause must keep retrying until the blob lands).
//
// Serial (no t.Parallel): installCapturingSlog swaps the process-global default logger.
func TestPublishIncompleteEscalatesToLoudWarnWhenPersistent(t *testing.T) {
	warns := installCapturingSlog(t)

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	gt, name := kgtypes.GraphCode, "persistIncomplete"
	dm := mgr.managerFor(gt, name)

	// Below the escalation streak: each 409 is expected to self-heal via the un-stamp
	// re-upload, so it logs the TRANSIENT skip WARN — not the loud one.
	for range incompletePublishWarnStreak - 1 {
		dm.markIncompletePublish([]string{"seg-x"})
	}
	require.Empty(t, warns.warnsContaining("PERSISTENTLY incomplete"),
		"below the streak, a 409 logs the transient skip WARN, not the loud persistent one")
	require.NotEmpty(t, warns.warnsContaining("un-stamped for re-upload"),
		"each sub-streak 409 logs the transient skip WARN")
	require.True(t, dm.publishRetryPending(), "the retry stays armed below the streak (the 409 cause self-heals via re-upload)")

	// Crossing the streak escalates to the LOUD persistent-degradation WARN, carrying
	// the graph identity so an operator can alert on it.
	dm.markIncompletePublish([]string{"seg-x"})
	loud := warns.warnsContaining("PERSISTENTLY incomplete")
	require.NotEmpty(t, loud, "the persistent 409 streak escalates to a loud WARN")
	require.Contains(t, loud[0], name, "the loud WARN carries the graph identity")
	require.True(t, dm.publishRetryPending(),
		"unlike the coverage-skip bound, the 409 escalation keeps the retry ARMED — the blob must still re-upload")
}
