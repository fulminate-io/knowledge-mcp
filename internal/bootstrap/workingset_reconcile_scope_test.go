// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// workingset_reconcile_scope_test.go pins the whole point of the working set at the
// reconcile pass: a graph this client never interacted with receives NOTHING from
// the client's background machinery, and an interaction is what changes that.
//
// THE WALK IS THE CHEAPEST OF THE FOUR BEHAVIORS AND THE ONE A NAIVE FIX ADDRESSES
// ALONE, so no-walk is asserted alongside no-publish, no-writeback and no-heal-probe.
// Every zero here is paired with a non-zero on the ADMITTED graph in the same
// fixture, because a four-way zero with no control is indistinguishable from a
// fixture that never wired its backend.

// perGraphPublishes reports how many manifest swaps the backend recorded for one
// graph — the observable the live evidence recorded as repeated manifest swaps for
// graphs this machine had no business publishing for.
func (b *fakeSegBackend) perGraphPublishes(gt kgtypes.GraphType, name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishByGraph[string(gt)+"/"+name]
}

// perGraphManifestReads reports the manifest reads for one graph. The heal probe's
// shipped-manifest snapshot is a manifest read, so a graph with zero of them was
// never heal-probed.
func (b *fakeSegBackend) perGraphManifestReads(gt kgtypes.GraphType, name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readByGraph[string(gt)+"/"+name]
}

// executeCount / mutationCount report the routed calls that named one graph.
func (e *reconcileEngine) executeCount(gt kgtypes.GraphType, name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.execByGraph[string(gt)+"/"+name]
}

func (e *reconcileEngine) mutationCount(gt kgtypes.GraphType, name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mutByGraph[string(gt)+"/"+name]
}

// armDegenerate puts a graph in the state that makes every one of this pass's arms
// fire for it: a shipped corpus above the resident floor but genuinely incomplete
// against the fixture's embedded count, plus a scan page for the rebuild to read. A
// graph in this state that is WALKED will scan, rebuild and publish; one that is not
// walked leaves every counter at zero. That symmetry is what lets the same recipe
// serve as both the foreign graph's trap and the admitted graph's control.
func armDegenerate(t *testing.T, eng *reconcileEngine, backend *fakeSegBackend, repo string) {
	t.Helper()
	shipHNSW(t, backend, repo, 64, 64)
	eng.mu.Lock()
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)
	eng.mu.Unlock()
}

// TestForeignGraph_NoWalkNoPublishNoWriteback is the regression for the failure the
// ticket recorded: a graph in the account that this client never searched, collected
// or wrote to was walked, probed, rebuilt and published for on every tick, forever.
//
// Both graphs are armed identically degenerate. The ONLY difference between them is
// that one was interacted with.
func TestForeignGraph_NoWalkNoPublishNoWriteback(t *testing.T) {
	const (
		admitted = "admittedRepo"
		foreign  = "foreignRepo"
	)
	ctx := opCtx()

	c, eng, backend, _ := buildReconcileClientWithSegDir(t, 300, admitted)

	armDegenerate(t, eng, backend, admitted)
	armDegenerate(t, eng, backend, foreign)

	// The foreign graph is present in the BACKEND'S CATALOG — the synthetic stand-in
	// for the account graph this machine had no local codebase for. A pass that
	// enumerated the backend would discover it here.
	eng.namesByType[string(kgtypes.GraphCode)] = []string{admitted, foreign}

	c.reconcileSegmentCoverage(ctx)

	// (a) NO WALK. Neither the per-graph body nor the enumeration that used to find it.
	require.Equal(t, 0, eng.scanCallCount(foreign),
		"a never-interacted graph is not walked — the per-graph reconcile body never ran for it")
	require.Equal(t, 0, eng.deltaScanCallCount(foreign),
		"and no bounded delta window was pulled for it either")
	require.Equal(t, 0, eng.horizonSeedCallCount(foreign),
		"and no horizon was seeded for it — the seed is part of the body it never entered")
	require.Equal(t, 0, eng.graphNameCallCount(),
		"and the pass issued NO enumeration RPC at all: the enumeration is scoped, not the walk that follows it")

	// (b) NO PUBLISH — the manifest swaps the live evidence recorded for graphs this
	// machine never worked with.
	require.Equal(t, 0, backend.perGraphPublishes(kgtypes.GraphCode, foreign),
		"a never-interacted graph has nothing published for it")

	// (c) NO WRITEBACK — nothing routed named it, and nothing routed WROTE to it.
	require.Equal(t, 0, eng.executeCount(kgtypes.GraphCode, foreign),
		"no routed call named the foreign graph")
	require.Equal(t, 0, eng.mutationCount(kgtypes.GraphCode, foreign),
		"and no write was addressed to it")

	// (d) NO HEAL PROBE — the shipped-manifest snapshot the heal decision reads.
	require.Equal(t, 0, backend.perGraphManifestReads(kgtypes.GraphCode, foreign),
		"a never-interacted graph is not heal-probed — its shipped manifest is never even read")

	// THE CONTROLS, on the admitted graph, in this same fixture and this same pass.
	require.GreaterOrEqual(t, eng.scanCallCount(admitted), 1,
		"CONTROL: the admitted graph IS walked and rebuilt — so the zeros above are about admission, not a dead fixture")
	require.GreaterOrEqual(t, backend.perGraphPublishes(kgtypes.GraphCode, admitted), 1,
		"CONTROL: the admitted graph's rebuild publishes a manifest — so the publish counter can move")
	require.GreaterOrEqual(t, backend.perGraphManifestReads(kgtypes.GraphCode, admitted), 1,
		"CONTROL: the admitted graph IS heal-probed — so the manifest-read counter can move")

	// THE RECORDER CONTROL for (c). This pass writes nothing through the routed seam
	// for any graph, so a zero mutation count proves nothing until the recorder is
	// shown to fire. One routed write against the ADMITTED graph does that: the
	// counter moves for a call the client really made, which is what makes the
	// foreign graph's zero a statement about traffic rather than about the fake.
	_, err := c.router.Execute(ctx, &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: admitted},
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, eng.mutationCount(kgtypes.GraphCode, admitted),
		"RECORDER CONTROL: a routed write IS observed per graph — the foreign graph's zero means no write was sent")
	require.GreaterOrEqual(t, eng.executeCount(kgtypes.GraphCode, admitted), 1,
		"RECORDER CONTROL: routed calls ARE observed per graph")
}

// TestSearchAdmitsThenGraphIsWalked drives the transition the rule turns on: the
// client owes a foreign graph nothing until a direct interaction earns it a place,
// and from that moment the ordinary background maintenance applies to it like any
// other member. The two tests together pin both directions of the gate.
//
// The admission is recorded through the PRODUCTION SEAM — the Manager's graph
// admitter, wired here exactly as ensureSegmentManager wires it — rather than by
// calling AdmitGraph directly, so a search that stopped admitting would fail this
// test instead of passing it.
func TestSearchAdmitsThenGraphIsWalked(t *testing.T) {
	const foreign = "searchAdmitsRepo"
	ctx := opCtx()

	c, eng, backend, dir := buildReconcileClientWithSegDir(t, 300)
	c.segmentMgr = segmentdist.NewManager(c.router, dir, 0,
		segmentdist.WithSegmentTransport(backend.transportBuilder()),
		segmentdist.WithGraphAdmitter(func(gt kgtypes.GraphType, name string) {
			c.AdmitGraph(gt, name, "search")
		}))

	armDegenerate(t, eng, backend, foreign)
	eng.namesByType[string(kgtypes.GraphCode)] = []string{foreign}

	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 0, eng.scanCallCount(foreign),
		"PRE: an un-interacted graph is not walked, however visible it is in the account")

	// THE INTERACTION. A user search against that graph — the one admission that does
	// not pass through the routed call recorder.
	_, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, foreign, "anything", nil, 5)
	require.NoError(t, err)
	require.True(t, c.WorkingSet().Has(kgtypes.GraphCode, foreign),
		"the search admitted the graph through the production admitter")

	c.reconcileSegmentCoverage(ctx)
	require.GreaterOrEqual(t, eng.scanCallCount(foreign), 1,
		"POST: the admitted graph is walked and healed on the next pass — admission is what changed, nothing else")
}
