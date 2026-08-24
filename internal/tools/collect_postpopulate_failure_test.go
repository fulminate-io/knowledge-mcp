// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// ppFailureSentinel is the hook error text the assertion greps for. A distinctive
// string, so a match cannot come from some other error on the path.
const ppFailureSentinel = "postpopulate-hook-exploded-sentinel"

// TestPostCollectPostPopulate_HookFailureSurfacesInCollectResult pins ticket item
// 4's second clause: a post-collect enrichment hook that FAILS must make the
// collect tool result an error naming the graph it failed on, rather than being
// swallowed by a warn log while the collect reports success.
//
// It asserts BEHAVIOR AT THE TOOL BOUNDARY, never the fanout's signature: it
// reads the ToolResult InterceptCollect returns and never touches
// runPostCollectPostPopulate's return value. That is what makes it a real gate
// rather than a compile-time tautology — it builds against a tree where the
// fanout still returns nothing, and fails there on its assertion. A compile
// error would be indistinguishable from a broken test.
//
// THE CATCHER: there is no half of this change that makes the test green. If the
// fanout returns the error but builtinCollectWork drops it, the ToolResult is
// still a success and this fails. If the fanout keeps warning and swallowing, it
// also fails.
//
// The fixture graph is a CLOUD graph deliberately. The failure-surfacing rule is
// uniform across collector families — a cloud or CI/CD collect fails visibly
// where it used to warn and succeed — so driving it through a cloud-mapped
// collector type exercises exactly that breadth. Do not "simplify" this to a
// code graph.
func TestPostCollectPostPopulate_HookFailureSurfacesInCollectResult(t *testing.T) {
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

	// A hook that FAILS. Registration overwrites by design (last wins), and the
	// sibling test that shares this collector type registers its own hook at its
	// own start, so the two do not interfere in either order.
	//
	// BreadthFamilyBroad is load-bearing, not incidental: it is what every cloud
	// and CI/CD hook declares, and it is the arm that enumerates the family's
	// graphs — which is where the "aws-acct-1" the assertion greps for comes
	// from. A BreadthScoped registration would take the scoped arm, find no
	// collected graph name (CollectGateGraphName yields "" for every non-code
	// collector type) and skip without ever firing the hook.
	postpopulate.Register(detachFullPathType, postpopulate.BreadthFamilyBroad, func(_ context.Context, _ postpopulate.GraphCaller, _ string) error {
		return errors.New(ppFailureSentinel)
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

	args := json.RawMessage(`{"type":"` + detachFullPathType + `","id":"pp-failure-id","force":true}`)
	handled, res := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{Name: "collect", Arguments: args})
	require.True(t, handled, "InterceptCollect must handle the collect call")

	body := resultText(res)
	assert.True(t, res.IsError,
		"a failing post-collect enrichment hook must make the collect tool result an ERROR, not a success with a warn in the log; got: %s", body)
	assert.Contains(t, body, ppFailureSentinel,
		"the collect result must carry the hook's own error text")
	assert.Contains(t, body, "aws-acct-1",
		"the collect result must name the graph the hook failed on, so one bad graph is identifiable")
}
