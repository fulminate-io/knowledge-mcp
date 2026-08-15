// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// capturedCall is one observed Execute, keeping the ctx beside the request so a
// probe read can be told apart from a graph-name enumeration on the same fake.
type capturedCall struct {
	ctx context.Context //nolint:containedctx // the captured ctx IS the subject under test
	req *knowledgev1.ExecuteRequest
}

// ctxCapturingFake records the ctx of every Execute. The two fakes already in
// this package (resolverFake, fakeCaller) both declare Execute with a
// ctx-ignoring receiver signature, so neither can observe the operation stamp —
// this one exists for that single purpose. It serves BOTH legs because
// crossgraph.GraphCaller and render.Executor are both Execute-only interfaces,
// and it discriminates by request shape: the graph-name enumeration consumes
// GraphNames while the node probe consumes Nodes.
type ctxCapturingFake struct {
	mu    sync.Mutex
	calls []capturedCall

	graphNames []string // names served to the RETURN_MODE_GRAPH_NAMES leg
}

func (f *ctxCapturingFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, capturedCall{ctx: ctx, req: req})
	f.mu.Unlock()

	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		infos := make([]*knowledgev1.GraphInfo, 0, len(f.graphNames))
		for _, n := range f.graphNames {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}
	// By-id node resolution (FetchNodeIn): serve an empty typed Nodes carrier so
	// the probe loop keeps scanning every supplied graph rather than
	// short-circuiting on the first hit.
	return enginetest.ResponseWithNodes(), nil
}

// captured returns the recorded calls, split into the enumeration leg
// (RETURN_MODE_GRAPH_NAMES) and the probe leg (everything else).
func (f *ctxCapturingFake) captured() (probes, enumerations []capturedCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			enumerations = append(enumerations, c)
			continue
		}
		probes = append(probes, c)
	}
	return probes, enumerations
}

// TestLocateForeignNode_StampsNonAdmittingOperation pins that a cross-graph
// probe is attributed to the probe itself rather than to whatever user tool call
// happens to be on the stack. The working set admits a graph when an RPC's
// ctx-stamped operation is an admitting one AND its target resolves a concrete
// instance, so an unrestamped probe lets one user call — which named exactly one
// graph — admit every loaded foreign graph it happens to scan.
//
// BOTH stamp sites are covered here because they are reached by different entry
// points: LocateForeignNode takes its graph list as a parameter and so never
// reaches the enumeration, while ListForeignGraphsOfType is nothing but the
// enumeration. A test driving only the first leaves the second omissible with
// every criterion green.
//
// The assertions compare against the raw wire string rather than the
// graphclient constant so the reproduction compiles — and fails — against the
// unfixed tree, where every ctx on both legs still carries the caller's own
// operation.
func TestLocateForeignNode_StampsNonAdmittingOperation(t *testing.T) {
	const wantOperation = "crossgraph.probe"

	// The caller's stamp is an ADMITTING one, which is the whole hazard: whatever
	// this ctx carries is what the probe's RPCs are attributed to today.
	callerCtx := graphclient.WithOperation(context.Background(), graphclient.OpThoughts)

	f := &ctxCapturingFake{graphNames: []string{"knowledge", "platform"}}

	// Leg (a) — the probe loop. Two graphs, no hit, so both are probed.
	_, _, _, found := LocateForeignNode(callerCtx, f, []ForeignGraph{
		{GraphType: "code", GraphName: "knowledge"},
		{GraphType: "code", GraphName: "platform"},
	}, "absent-node")
	require.False(t, found, "the fake serves no nodes, so the scan must reach every supplied graph")

	// Leg (b) — the graph-name enumeration, driven through the SAME fake.
	graphs, err := ListForeignGraphsOfType(callerCtx, f, "code")
	require.NoError(t, err)
	require.Len(t, graphs, 2)

	probes, enumerations := f.captured()

	// Vacuity guards first: a fake that was never driven must not pass by having
	// nothing to assert over.
	require.GreaterOrEqual(t, len(probes), 2,
		"expected one probe per supplied foreign graph — a scan that never issued a read cannot prove anything about its stamp")
	require.GreaterOrEqual(t, len(enumerations), 1,
		"expected the graph-name enumeration to issue a read")

	for i, c := range probes {
		op, ok := graphclient.OperationFromContext(c.ctx)
		require.True(t, ok, "probe %d ctx carries no operation at all", i)
		assert.Equal(t, wantOperation, string(op),
			"probe %d must be attributed to the probe, not to the user call that happened to trigger it", i)
	}
	for i, c := range enumerations {
		op, ok := graphclient.OperationFromContext(c.ctx)
		require.True(t, ok, "enumeration %d ctx carries no operation at all", i)
		assert.Equal(t, wantOperation, string(op),
			"enumeration %d must be attributed to the probe, not to the user call that happened to trigger it", i)
	}
}
