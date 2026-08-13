// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// workingset_scope_test.go pins the rule that the LLM pipeline works only on
// graphs this client has directly interacted with: "the pipeline shouldnt be
// working on graphs the client hasnt worked with either". Registration is the
// resource being denied — a non-admitted graph gets no collector AT ALL, not a
// registered-but-idle one — so every assertion here is about what the catalog
// pass registers and what the wire client is consequently asked.

// executeCallCount returns how many Execute RPCs the fake observed, read under
// its mutex. The catalog pass used to issue one graph_type_def browse plus one
// per-type read on every pass; deriving the wanted set from the working set is
// only a real scoping fix if that count is now ZERO, so the count is asserted
// directly rather than inferred from the registered set.
func executeCallCount(f *fakeWireClient) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execRequests)
}

// TestRefreshOnce_WantedSetIsTheWorkingSet proves the catalog pass takes its
// wanted set from the working set and reads nothing remote to do it.
//
// The zeros in part (a) are the point, so part (b) is their KNOWN-POSITIVE
// CONTROL against the same pipeline and the same fake: it drives the identical
// pass to a non-zero registered set, which an inert pass could not do. Without
// that control, "registers nothing" would be indistinguishable from a harness
// that never ran the pass at all.
func TestRefreshOnce_WantedSetIsTheWorkingSet(t *testing.T) {
	ctx := context.Background()
	fake := newFakeWireClient()

	// The backend HAS graphs to offer. Seeding them is what makes the zeros below
	// meaningful: the pass declines an account that is demonstrably non-empty,
	// rather than reporting nothing because there was nothing to report.
	fake.seedGraphNames(kgtypes.GraphCode, "foreign-repo", "another-foreign")
	fake.seedGraphNames(kgtypes.GraphPractice, "go")

	p := New(Config{}, fake, nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	// (a) EMPTY WORKING SET: no collector, and no catalog RPC of any kind.
	p.refreshOnce(ctx)
	assert.Empty(t, registeredKeys(p),
		"an empty working set registers NO collector, even though the backend holds graphs")
	assert.Zero(t, executeCallCount(fake),
		"the pass issues ZERO Execute RPCs — the wanted set is a local read, not an enumeration")

	// (b) ONE ADMISSION — the known-positive control for both zeros above.
	require.True(t, ws.Admit(kgtypes.GraphCode, "foreign-repo", "search"),
		"first admission of the graph")
	p.refreshOnce(ctx)

	assert.Equal(t,
		map[graphKey]struct{}{{GraphType: kgtypes.GraphCode, GraphName: "foreign-repo"}: {}},
		registeredKeys(p),
		"exactly the admitted graph is registered — not the other graphs the backend offers")
	assert.Zero(t, executeCallCount(fake),
		"registering a graph still costs no catalog RPC")

	require.NoError(t, p.Stop(ctx))
}

// TestAdmissionWakesCatalogRefresh proves an ADMISSION is what drives the
// catalog loop now. Under a working-set-derived wanted set, catalog-gen movement
// and login flips no longer describe what the pipeline should drain — an
// interaction does — so without this wake a freshly-admitted graph would sit
// unregistered waiting for an unrelated event that may never arrive.
//
// The admission is the ONLY signal delivered here: catalogWake is deliberately
// never touched, and the assertion below confirms it stayed empty, so the
// registration can only have come from the admission itself.
func TestAdmissionWakesCatalogRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := newFakeWireClient()
	p := New(Config{}, fake, nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RefreshLoadedGraphs(ctx)
	}()

	// The loop's entry pass runs against an empty working set: nothing registers.
	// This is also the known-negative baseline for the admission below — a
	// registration appearing later cannot be left over from startup.
	require.Never(t, func() bool { return len(registeredKeys(p)) > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"an empty working set registers nothing, and the loop does not invent graphs while it waits")

	// The interaction. Nothing else is signaled.
	require.True(t, ws.Admit(kgtypes.GraphCode, "repoA", "search"), "first admission")

	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[graphKey{GraphType: kgtypes.GraphCode, GraphName: "repoA"}]
		return ok
	}, 2*time.Second, 10*time.Millisecond,
		"the admission alone must wake the catalog loop and register that graph's collector")

	assert.Equal(t,
		map[graphKey]struct{}{{GraphType: kgtypes.GraphCode, GraphName: "repoA"}: {}},
		registeredKeys(p),
		"exactly the admitted graph — the admission registers its own graph and nothing else")
	assert.Zero(t, executeCallCount(fake), "waking and registering still costs no catalog RPC")

	cancel()
	<-done
	require.NoError(t, p.Stop(context.Background()))
}

// TestWantedGraphs_FiltersToDrainableTypes pins the eligibility filter that sits
// between the working set and the wanted set. A member of a type this pipeline
// does not enrich must not produce a collector even though it IS admitted —
// admission governs WHICH graphs, the type filter governs WHICH KINDS, and the
// two are independent gates.
func TestWantedGraphs_FiltersToDrainableTypes(t *testing.T) {
	p := New(Config{}, newFakeWireClient(), nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	// Drainable: an eligible builtin, and a registered CUSTOM type (which needs no
	// registry browse — the member carries its own GraphType).
	require.True(t, ws.Admit(kgtypes.GraphCode, "repoA", "collect"))
	require.True(t, ws.Admit(kgtypes.GraphType("hellograph"), "demo", "collect"))
	// Not drainable: a builtin type the pipeline does not enrich.
	require.True(t, ws.Admit(kgtypes.GraphLogs, "some-query", "search"))

	got := map[string]bool{}
	for _, ref := range p.wantedGraphs() {
		got[string(ref.GraphType)+"/"+ref.GraphName] = true
	}

	assert.True(t, got["code/repoA"], "an eligible builtin type is drained")
	assert.True(t, got["hellograph/demo"], "a registered custom type is drained")
	assert.False(t, got["logs/some-query"], "a builtin type the pipeline does not enrich is filtered out")
	assert.Len(t, got, 2, "exactly the two drainable members")
}

// scanCountForGraph returns how many PipelineScan RPCs named the given graph.
func scanCountForGraph(f *fakeWireClient, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scansByGraph[name]
}

// genPollGraphNames returns every graph name the fake ever RECEIVED in a
// PipelineGenPollRequest, across all polls.
func genPollGraphNames(f *fakeWireClient) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, req := range f.genPollRequests {
		for _, g := range req.GetGraphs() {
			out = append(out, g.GetGraphName())
		}
	}
	return out
}

// writebackTargetRepos returns the code-graph repo each captured update_batch
// Execute was routed to, in call order.
func writebackTargetRepos(f *fakeWireClient) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, req := range f.execRequests {
		m := req.GetMutation()
		if m == nil || m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS {
			continue
		}
		out = append(out, req.GetTarget().GetRepo())
	}
	return out
}

// TestForeignGraph_NoCollectorNoScanNoWriteback is the pipeline half of the
// ticket's requirement that a never-interacted graph gets NO PUBLISH and NO
// WRITEBACK, not merely no walk. All four pipeline surfaces the rule names are
// asserted, because scoping only the scan would leave the graph holding a
// collector, a gen-poll entry and a writeback path.
//
// EVERY ZERO HERE HAS A NON-ZERO CONTROL in the same fixture: the backend holds
// BOTH graphs, and the admitted one is driven through the very same measurement
// so each counter is shown capable of moving. Four zeros with no control would
// prove only that the fake was never driven.
func TestForeignGraph_NoCollectorNoScanNoWriteback(t *testing.T) {
	const mine = "mine"     // interacted with — the known-positive control
	const theirs = "theirs" // never interacted with — the graph under test

	ctx := context.Background()
	fake := newFakeWireClient()
	// The backend catalog holds BOTH. Under the old enumerate-the-catalog
	// behavior this is exactly the shape that put a collector, a gen-poll entry
	// and an LLM writeback behind a repo this machine never had.
	fake.seedGraphNames(kgtypes.GraphCode, mine, theirs)
	// Non-empty scan results on both axes so a registered collector has real work
	// to drain — without this the writeback control below could not fire.
	fake.seedSummaryScan(&knowledgev1.PipelineScanItem{NodeId: "n1", SummarizeText: `{"name":"n1"}`})

	cfg := Config{
		SummaryChannelSize: 4, SummaryBatchSize: 1, SummaryWorkers: 1,
		EmbedChannelSize: 4, EmbedBatchSize: 1, EmbedWorkers: 1,
		Tick: 5 * time.Millisecond,
	}
	fs := &fakeSummarizer{results: map[string]llmproviders.SummarizeResult{"n1": {Summary: "s1"}}}
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(3)}}
	p := New(cfg, fake, fs.call, fe.call)

	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, mine, "collect"), "only ONE of the two graphs is ever interacted with")
	p.AttachWorkingSet(ws)
	require.NoError(t, p.Start(ctx))

	// The BOOT surface, asserted explicitly because the ticket names it separately:
	// RefreshOnceForBoot registers only what the working set holds. It DELEGATES to
	// refreshOnce, so this is a wiring assertion rather than a second mechanism —
	// there is no separate boot discovery path to go looking for.
	p.RefreshOnceForBoot(ctx)

	// (a) NO COLLECTOR REGISTERED AT ALL — registration is the resource being
	// denied, not merely the scan.
	have := registeredKeys(p)
	require.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: mine},
		"CONTROL: the interacted-with graph IS registered")
	assert.NotContains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: theirs},
		"the never-interacted graph gets NO collector at all — not a registered-but-idle one")

	// (c) NO SCAN naming the foreign graph. Let the registered collector tick.
	require.Eventually(t, func() bool { return scanCountForGraph(fake, mine) > 0 },
		5*time.Second, 5*time.Millisecond, "CONTROL: the admitted graph IS scanned")
	assert.Zero(t, scanCountForGraph(fake, theirs), "the never-interacted graph is never scanned")

	// (b) NO GEN-POLL ENTRY. Asserted on the REQUEST the client BUILT from its
	// collector registry, not on the response: the request set is the scoping.
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	polled := genPollGraphNames(fake)
	require.Contains(t, polled, mine, "CONTROL: the admitted graph IS polled for")
	assert.NotContains(t, polled, theirs, "the never-interacted graph appears in no gen-poll request")

	// (d) NO WRITEBACK targeting the foreign graph. Drive a real embed writeback
	// for the admitted graph so the counter is shown to move.
	runEmbedWorkerBatch(ctx, p, []EmbedWork{{
		GraphType: kgtypes.GraphCode, GraphName: mine, NodeID: "n1",
		EmbedText: "hello",
	}})
	repos := writebackTargetRepos(fake)
	require.Contains(t, repos, mine, "CONTROL: a writeback DOES land for the admitted graph")
	assert.NotContains(t, repos, theirs, "no writeback ever targets the never-interacted graph")

	require.NoError(t, p.Stop(ctx))
}

// TestForeignGraph_EmptyWorkingSetIssuesNoGenPollRPC pins the other half of the
// gen-poll scoping the ticket names: with NOTHING admitted there are no
// collectors, and the poll issues no PipelineGenPoll RPC at all rather than an
// empty-set request — which the server would read as "all eligible graphs".
func TestForeignGraph_EmptyWorkingSetIssuesNoGenPollRPC(t *testing.T) {
	ctx := context.Background()
	fake := newFakeWireClient()
	fake.seedGraphNames(kgtypes.GraphCode, "theirs")

	p := New(Config{}, fake, nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	p.RefreshOnceForBoot(ctx)
	require.Empty(t, registeredKeys(p), "nothing admitted, nothing registered")

	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Zero(t, fake.genPollCallCount(),
		"zero collectors means ZERO gen-poll RPCs — never an empty request the server would widen")

	// KNOWN-POSITIVE CONTROL: the same poll, on the same fake, DOES issue an RPC
	// once a graph is admitted. Without this the zero above could be a poll that
	// never ran.
	require.True(t, ws.Admit(kgtypes.GraphCode, "mine", "search"))
	p.refreshOnce(ctx)
	_, throttled = p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Equal(t, 1, fake.genPollCallCount(), "an admitted graph makes the poll fire")
	assert.Equal(t, []string{"mine"}, genPollGraphNames(fake), "and it asks for exactly that graph")

	require.NoError(t, p.Stop(ctx))
}

// TestWantedGraphs_NilWorkingSetIsEmptyNotUnrestricted pins the default-deny
// direction at the seam where a wiring mistake would land. An unattached working
// set must yield NOTHING, so a missed AttachWorkingSet under-admits — a pipeline
// that enriches nothing, which is visible — rather than silently restoring the
// account-wide draining this gate exists to remove.
func TestWantedGraphs_NilWorkingSetIsEmptyNotUnrestricted(t *testing.T) {
	ctx := context.Background()
	fake := newFakeWireClient()
	fake.seedGraphNames(kgtypes.GraphCode, "repoA")

	p := New(Config{}, fake, nil, nil) // AttachWorkingSet deliberately not called

	assert.Empty(t, p.wantedGraphs(), "a nil working set wants nothing")
	p.refreshOnce(ctx)
	assert.Empty(t, registeredKeys(p), "and therefore registers nothing")
	assert.Zero(t, executeCallCount(fake), "and asks the backend nothing")
}
