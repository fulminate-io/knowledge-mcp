// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// admissionWiredClient builds a *client wired exactly the way constructClient
// wires the admission path: a real Router carrying the real working set, with
// the recorder attached. The Router has NO local handle and is not logged in, so
// every Execute short-circuits at pick() with ErrNoBackend and reaches no
// network. That is deliberate: admission is recorded BEFORE dispatch, and these
// tests are about what got admitted rather than what came back.
func admissionWiredClient(t *testing.T) *client {
	t.Helper()
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute)
	router := graphclient.NewRouter(nil, "http://cloud.invalid", staticTokenSource{tok: "tok"}, authState)
	c := &client{
		router:     router,
		authState:  authState,
		workingSet: workingset.New(),
	}
	router.AttachWorkingSet(c.AdmitGraph)
	return c
}

func codeTarget(repo string) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: repo}
}

// TestGraphCallerSeamsSurviveAdmissionWiring is the regression for the defect
// that a GraphCaller DECORATOR would have introduced. tools.GraphCaller is a
// one-method interface, so a struct embedding it promotes only Execute and every
// intercept that type-asserts UP to a carrier seam would get ok=false and
// degrade SILENTLY — the worst of them returns nil rows and kills the entire
// manage(status) coverage table.
//
// The seams are asserted against inline anonymous interfaces, which Go's
// structural typing makes sufficient and which needs none of the tools package's
// unexported names. A wrapper promoting only Execute fails every one of the
// first four.
func TestGraphCallerSeamsSurviveAdmissionWiring(t *testing.T) {
	t.Parallel()

	gc := admissionWiredClient(t).GraphCaller()
	require.NotNil(t, gc, "precondition: the fixture must produce a real GraphCaller")

	_, ok := gc.(interface {
		Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
	})
	assert.True(t, ok, "the Execute seam must survive")

	_, ok = gc.(interface {
		Stats(context.Context, *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error)
	})
	assert.True(t, ok, "the Stats seam must survive — losing it returns nil coverage rows "+
		"and silently empties the manage(status) coverage table")

	_, ok = gc.(interface {
		Index(context.Context, *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error)
	})
	assert.True(t, ok, "the Index seam must survive")

	_, ok = gc.(interface {
		MetadataStats(context.Context, *knowledgev1.MetadataStatsRequest) (*knowledgev1.MetadataStatsResponse, error)
	})
	assert.True(t, ok, "the MetadataStats seam must survive")
}

// TestCoverageBandSeamsAreSatisfiedByTheClient pins the two OPTIONAL deps
// capabilities the manage(status) coverage table reads its honest bands through.
//
// IT EXISTS BECAUSE THE FAILURE IS SILENT. Both are consumed by a type assertion on
// tools.ClientDeps, so a signature that drifts — a renamed method, a *string where a
// string belongs — does not break the build. The assertion simply reports ok=false,
// the table falls back to "not stalled, and in the working set", and both new bands
// stop appearing in production while every band unit test stays green.
//
// The seams are asserted against inline anonymous interfaces mirroring the ones the
// tools package declares, the same structural-typing idiom
// TestGraphCallerSeamsSurviveAdmissionWiring uses, and they are asserted through a
// tools.ClientDeps interface value so this is the SAME assertion path production
// takes rather than a direct method call that would compile either way.
func TestCoverageBandSeamsAreSatisfiedByTheClient(t *testing.T) {
	t.Parallel()

	c := admissionWiredClient(t)
	var deps tools.ClientDeps = c

	stall, ok := deps.(interface {
		SegmentStalledSince(kgtypes.GraphType, string) int64
	})
	require.True(t, ok, "the stall-stamp seam must be satisfied — without it every stuck graph "+
		"silently keeps reporting the band of an arm that has given up on it")

	member, ok := deps.(interface {
		InWorkingSet(kgtypes.GraphType, string) bool
	})
	require.True(t, ok, "the working-set seam must be satisfied — without it a graph nothing "+
		"maintains silently keeps reporting a band that names an arm servicing it")

	// And the seams answer about THIS client's state rather than a constant. A graph
	// no interaction has admitted is outside the set and not stalled; the admitted
	// graph is the known-positive that stops both readings being stuck at one value.
	assert.False(t, member.InWorkingSet(kgtypes.GraphCode, "neverTouchedRepo"))
	assert.Zero(t, stall.SegmentStalledSince(kgtypes.GraphCode, "neverTouchedRepo"),
		"a graph with no latched breaker and no suppressed publish is not stalled")

	c.AdmitGraph(kgtypes.GraphCode, "touchedRepo", "search")
	assert.True(t, member.InWorkingSet(kgtypes.GraphCode, "touchedRepo"),
		"an admitted graph reads as maintained")

	// The stall reading follows the breaker: latching one graph stalls that graph and
	// leaves the other alone, so the seam is reading real per-graph state.
	for range healBreakerTripThreshold {
		c.healBreaker.RecordNoProgress(kgtypes.GraphCode, "touchedRepo")
	}
	assert.NotZero(t, stall.SegmentStalledSince(kgtypes.GraphCode, "touchedRepo"),
		"a latched breaker is a stall the table can render an age from")
	assert.Zero(t, stall.SegmentStalledSince(kgtypes.GraphCode, "neverTouchedRepo"),
		"and it stays per-graph")
}

// recordingSink is the inner sink the admitting wrapper delegates to. It records
// every WriteResult it receives so the delegation can be asserted rather than
// assumed: a wrapper that admitted but swallowed the write would be a data-loss
// bug that a working-set assertion alone would not catch.
type recordingSink struct {
	mu      sync.Mutex
	writes  []string
	wrapErr error
}

func (s *recordingSink) WriteResult(
	_ context.Context, collectorName string, result *collectorwire.CollectResult,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, collectorName+":"+string(result.GraphType)+"/"+result.GraphName)
	return s.wrapErr
}

func (s *recordingSink) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// TestCollectSinkAdmitsEveryGraphFamily drives the sink with results from three
// different collector families. The CLOUD case is the specific catcher: deriving
// the graph name at the collect intercept instead of from the collector's own
// authored identity would be code-only by construction, so a cloud collect would
// silently never admit its own graph and would then never be enriched.
func TestCollectSinkAdmitsEveryGraphFamily(t *testing.T) {
	t.Parallel()

	c := admissionWiredClient(t)
	inner := &recordingSink{}
	sink := admittingSink{
		inner: inner,
		admit: func(gt kgtypes.GraphType, name string) { c.AdmitGraph(gt, name, "collect") },
	}

	for _, tc := range []struct {
		collector string
		gt        kgtypes.GraphType
		name      string
	}{
		{"code", kgtypes.GraphCode, "repoA"},
		{"cloud", kgtypes.GraphCloud, "acct-1"},
		{"practice", kgtypes.GraphPractice, "go"},
	} {
		require.NoError(t, sink.WriteResult(context.Background(), tc.collector,
			&collectorwire.CollectResult{GraphType: tc.gt, GraphName: tc.name}))
	}

	assert.Equal(t, []workingset.Ref{
		{GraphType: kgtypes.GraphCloud, Name: "acct-1"},
		{GraphType: kgtypes.GraphCode, Name: "repoA"},
		{GraphType: kgtypes.GraphPractice, Name: "go"},
	}, c.WorkingSet().Members(), "every collector family must admit the graph it produced")

	assert.Equal(t, []string{
		"code:code/repoA",
		"cloud:cloud/acct-1",
		"practice:practice/go",
	}, inner.recorded(), "the inner sink must still receive every write, unchanged")
}

// TestManageStatusSweepDoesNotAdmit pins the loophole the operative rule closes
// by name: "manage operations do not count towards interaction". manage(status)
// is a legitimate USER-DRIVEN read that must keep working — it enumerates the
// ACCOUNT catalog to show the user what exists, and that stays unchanged — but
// it fans a per-graph read across EVERY account graph, so if it counted, one
// status call would admit the entire account and reopen every loop this work
// closes. Those per-graph reads DO resolve concrete instances, so the structural
// instance-key gate does not stop them; the operation's membership in the
// management category is the only thing that does.
//
// FIDELITY NOTE, so a future reader does not over-read this test. Production
// does NOT reach the coverage table's Stats calls through Router.Execute: the
// collector upgrades the GraphCaller to the Stats seam and calls it directly,
// which is a different RPC on the same Router. The Stats-SHAPED Execute below
// therefore pins something STRONGER than production exercises — that even an
// Execute-carried per-graph read under the manage operation admits nothing —
// and stays correct if a future refactor routes coverage reads through Execute.
// Only the query(mode:modules) half is production-faithful to a real
// manage(status) call.
func TestManageStatusSweepDoesNotAdmit(t *testing.T) {
	t.Parallel()

	c := admissionWiredClient(t)

	// KNOWN-POSITIVE CONTROL FIRST. Without it an unchanged working set proves
	// only that the fake was never driven.
	_, _ = c.router.Execute(
		graphclient.WithOperation(context.Background(), graphclient.OpMutate),
		&knowledgev1.ExecuteRequest{
			Target: codeTarget("user-repo"),
			Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{}},
		})
	require.Equal(t,
		[]workingset.Ref{{GraphType: kgtypes.GraphCode, Name: "user-repo"}},
		c.WorkingSet().Members(),
		"control: a user mutation must admit exactly one graph")

	manageCtx := graphclient.WithOperation(context.Background(), graphclient.OpManage)

	// The per-graph fan-out, with CONCRETE instance targets. Type-only targets
	// would be refused by the instance-key gate and this test would pass without
	// ever exercising the management exclusion it exists to pin.
	for _, repo := range []string{"foreign-a", "foreign-b", "foreign-c"} {
		_, _ = c.router.Execute(manageCtx, &knowledgev1.ExecuteRequest{
			Target: codeTarget(repo),
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeTypes: []string{"graph"}},
			}},
		})
	}
	// The production-faithful half: the catalog enumeration a real coverage
	// sweep issues.
	_, _ = c.router.Execute(manageCtx, &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode)},
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{}},
	})

	assert.Equal(t,
		[]workingset.Ref{{GraphType: kgtypes.GraphCode, Name: "user-repo"}},
		c.WorkingSet().Members(),
		`"manage operations do not count towards interaction": a coverage sweep across `+
			`every account graph must leave the working set exactly as it found it`)
}
