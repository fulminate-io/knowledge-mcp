// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// collect_postpopulate_breadth_test.go drives runPostCollectPostPopulate's
// breadth dispatch (collect_postpopulate.go) through the existing fakeGraphCaller
// + tailRoutingDeps harness. "Did the orchestrator enumerate?" is answerable
// without a new recorder: the fake already appends every ExecuteRequest to
// execRequests, and a family enumeration is exactly a request carrying
// RETURN_MODE_GRAPH_NAMES.

// mapGraphTypeForTest points a test-only collector type at a graph type for the
// duration of the test, restoring the previous entry afterwards. Mirrors the
// save/restore idiom in collect_tail_routing_test.go.
func mapGraphTypeForTest(t *testing.T, collectorType string, gt kgtypes.GraphType) {
	t.Helper()
	prev, had := postPopulateGraphType[collectorType]
	postPopulateGraphType[collectorType] = gt
	t.Cleanup(func() {
		if had {
			postPopulateGraphType[collectorType] = prev
		} else {
			delete(postPopulateGraphType, collectorType)
		}
	})
}

// seededBreadthDeps returns a ClientDeps whose routed caller enumerates the
// given (graphType, names) pairs for a RETURN_MODE_GRAPH_NAMES read, plus the
// routed recorder itself so a test can inspect execRequests.
func seededBreadthDeps(graphType string, names ...string) (*tailRoutingDeps, *fakeGraphCaller) {
	entries := make([]string, 0, len(names))
	for _, n := range names {
		entries = append(entries, `{"graph_type":"`+graphType+`","graph_name":"`+n+`"}`)
	}
	body := `{"graphs":[` + strings.Join(entries, ",") + `]}`
	seed := func() *fakeGraphCaller {
		return &fakeGraphCaller{
			listGraphsResult: &kgtools.ToolResult{
				Content: []kgtools.ContentBlock{{Type: "text", Text: body}},
			},
		}
	}
	routed := seed()
	return &tailRoutingDeps{routed: routed, local: seed()}, routed
}

// enumerated reports whether any recorded request asked for a family graph-name
// enumeration.
func enumerated(f *fakeGraphCaller) bool {
	for _, r := range f.execRequests {
		if r.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			return true
		}
	}
	return false
}

// TestPostPopulateBreadth_ScopedFiresOnce is the ticket's central assertion: a
// hook declaring BreadthScoped is fired once, against the graph that was just
// collected, and the orchestrator never enumerates the family. The fixture seeds
// THREE graphs on purpose — against a single-graph fixture "fired once" cannot
// distinguish scoping from there being nothing else to fire against — and the
// collected name is deliberately absent from those three, so a regression to the
// broad arm fails on both the count and the identity.
func TestPostPopulateBreadth_ScopedFiresOnce(t *testing.T) {
	const stubType = "breadth-scoped-stub"
	mapGraphTypeForTest(t, stubType, kgtypes.GraphCode)

	var mu sync.Mutex
	var got []string
	postpopulate.Register(stubType, postpopulate.BreadthScoped, func(_ context.Context, _ postpopulate.GraphCaller, name string) error {
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
		return nil
	})

	deps, routed := seededBreadthDeps("code", "repo-a", "repo-b", "repo-c")

	err := runPostCollectPostPopulate(context.Background(), deps, stubType, "repo-collected")

	mu.Lock()
	fired := append([]string(nil), got...)
	mu.Unlock()

	require.NoError(t, err, "a well-wired scoped dispatch must not error — the known-negative for the empty-name refusal")
	require.Len(t, fired, 1, "a scoped hook must fire exactly once, not once per graph of the family")
	assert.Equal(t, "repo-collected", fired[0], "a scoped hook must fire against the COLLECTED graph")
	assert.False(t, enumerated(routed), "a scoped hook must not trigger a family graph-name enumeration")
}

// TestPostPopulateBreadth_BroadEnumerates is the KNOWN-POSITIVE CONTROL for the
// scoped test's zero-enumerations assertion, which a recorder that sees nothing
// at all would otherwise satisfy. It also proves the broad arm's breadth is
// preserved verbatim: a non-empty collected name is passed on purpose and must
// be ignored.
func TestPostPopulateBreadth_BroadEnumerates(t *testing.T) {
	const stubType = "breadth-broad-stub"
	mapGraphTypeForTest(t, stubType, kgtypes.GraphCloud)

	var mu sync.Mutex
	var got []string
	postpopulate.Register(stubType, postpopulate.BreadthFamilyBroad, func(_ context.Context, _ postpopulate.GraphCaller, name string) error {
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
		return nil
	})

	deps, routed := seededBreadthDeps("cloud", "acct-a", "acct-b", "acct-c")

	err := runPostCollectPostPopulate(context.Background(), deps, stubType, "repo-collected")

	mu.Lock()
	fired := append([]string(nil), got...)
	mu.Unlock()

	require.NoError(t, err, "a well-wired broad dispatch must not error")
	assert.Equal(t, []string{"acct-a", "acct-b", "acct-c"}, fired,
		"a family-broad hook must still fire once per enumerated graph of its family, ignoring the collected name")
	assert.True(t, enumerated(routed), "the broad arm must issue the family graph-name enumeration")
}

// TestPostPopulateBreadth_ScopedEmptyNameRefuses gates the refusal path the
// scoped arm introduces. An unexercised refusal is indistinguishable from one
// that silently falls through to enumeration, and falling through would restore
// the very fan-out the declaration removes. The fixture deliberately HAS graphs
// available: against an empty fixture "fired zero times" is true of every
// implementation. The enumeration assertion is the load-bearing half — it is
// what separates a refusal from a fall-through that enumerated first.
//
// The RETURNED ERROR is the other load-bearing half, and it is what separates
// this refusal from the warn-and-skip it replaced: "did not fire" and "did not
// enumerate" are both true of a silent skip too, so without the error assertion
// this test passes against either disposition. A scoped hook reached with no
// collected graph name is a wiring defect, and a wiring defect fails the collect.
func TestPostPopulateBreadth_ScopedEmptyNameRefuses(t *testing.T) {
	const stubType = "breadth-scoped-empty-stub"
	mapGraphTypeForTest(t, stubType, kgtypes.GraphCode)

	var mu sync.Mutex
	var got []string
	postpopulate.Register(stubType, postpopulate.BreadthScoped, func(_ context.Context, _ postpopulate.GraphCaller, name string) error {
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
		return nil
	})

	deps, routed := seededBreadthDeps("code", "repo-a", "repo-b", "repo-c")

	err := runPostCollectPostPopulate(context.Background(), deps, stubType, "")

	mu.Lock()
	fired := append([]string(nil), got...)
	mu.Unlock()

	require.Error(t, err, "a scoped hook with no collected graph name must FAIL the collect, not skip with a warn")
	assert.Contains(t, err.Error(), stubType, "the error must name the collector whose wiring is defective")
	assert.Empty(t, fired, "a scoped hook with no collected graph name must not fire at all")
	assert.False(t, enumerated(routed), "the refusal must not fall through to a family enumeration")
}

// TestPostPopulateBreadth_ScopedEmptyNameFailsCollectResult is the sibling test
// above's other half, asserted at the TOOL BOUNDARY rather than on the fanout's
// return value: the refusal must reach the caller as an ERROR collect result, not
// merely as a non-nil error some intermediate step swallows. It reads the
// ToolResult InterceptCollect returns and never touches
// runPostCollectPostPopulate's signature, which is what makes it a behavior gate
// instead of a compile-time tautology.
//
// The empty collected graph name is produced by production code, not injected: a
// scoped hook is registered against a NON-code collector type, and
// CollectGateGraphName yields "" for every collector type but "code". That is the
// exact wiring defect the arm exists to catch — a hook declaring scoped breadth in
// a family whose collects carry no graph name.
func TestPostPopulateBreadth_ScopedEmptyNameFailsCollectResult(t *testing.T) {
	registerDetachStub()

	// Map the stub collector type into the postpopulate gate so the tail fires;
	// restore afterwards.
	prevPP, hadPP := postPopulateGraphType[detachFullPathType]
	postPopulateGraphType[detachFullPathType] = kgtypes.GraphCloud
	t.Cleanup(func() {
		if hadPP {
			postPopulateGraphType[detachFullPathType] = prevPP
		} else {
			delete(postPopulateGraphType, detachFullPathType)
		}
	})

	// A hook that would SUCCEED if it were ever fired, so a green result cannot be
	// mistaken for the hook-failure path this test is not about. Registration
	// overwrites by design (last wins) and every test sharing this collector type
	// registers its own hook at its own start, so the order they run in is free.
	var fired atomic.Int32
	postpopulate.Register(detachFullPathType, postpopulate.BreadthScoped, func(_ context.Context, _ postpopulate.GraphCaller, _ string) error {
		fired.Add(1)
		return nil
	})

	// Take the SYNCHRONOUS arm: the stub blocks in Collect until release, so
	// pre-closing the release channel lets the collect finish immediately and win
	// the race against the runtime's default detach timer. A detached run would
	// return the STILL-RUNNING message and never surface the work's error.
	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})
	close(detachStubRelease)

	rt := NewCollectRuntime()
	fc := &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"cloud","graph_name":"aws-acct-1"}]}`}},
		},
	}
	deps := &detachFullDeps{rt: rt, gc: fc}

	args := json.RawMessage(`{"type":"` + detachFullPathType + `","id":"pp-scoped-empty-id","force":true}`)
	handled, res := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{Name: "collect", Arguments: args})
	require.True(t, handled, "InterceptCollect must handle the collect call")

	body := resultText(res)
	assert.True(t, res.IsError,
		"a scoped hook reached with no collected graph name must make the collect tool result an ERROR, not a success with a warn in the log; got: %s", body)
	assert.Contains(t, body, detachFullPathType,
		"the collect result must name the collector whose wiring is defective")
	assert.Equal(t, int32(0), fired.Load(), "the refusal must not fire the hook")
}
