// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingGc wraps a fixture-backed GraphCaller and counts Execute calls. failOn
// names a ReturnMode whose Execute returns an error instead of a response, which
// is how the degradation legs below fail exactly one of AssembleSubtree's two
// wire calls without disturbing the other.
type countingGc struct {
	inner  GraphCaller
	calls  int
	modes  []knowledgev1.ReturnMode
	failOn knowledgev1.ReturnMode
}

func (c *countingGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (c *countingGc) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.calls++
	mode := req.GetQuery().GetReturnMode()
	c.modes = append(c.modes, mode)
	if c.failOn != knowledgev1.ReturnMode_RETURN_MODE_UNSPECIFIED && mode == c.failOn {
		return nil, fmt.Errorf("countingGc: forced failure on %v", mode)
	}
	return c.inner.Execute(ctx, req)
}

// subtreeFixture seeds a root with width children, each carrying one
// grandchild, so the traversal has real depth to walk rather than a flat fan.
func subtreeFixture(width int) *graphFixture {
	f := newGraphFixture()
	f.addKnowledgeNode(&knowledgev1.Node{
		Id: "root", SymbolName: "Root", Type: string(kgtypes.NodePlan), Status: "active",
	})
	for i := range width {
		child := fmt.Sprintf("child-%02d", i)
		grand := fmt.Sprintf("grand-%02d", i)
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: child, SymbolName: "Child " + child, Type: string(kgtypes.NodePhase), Status: "pending",
		})
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: grand, SymbolName: "Grand " + grand, Type: string(kgtypes.NodeStep), Status: "pending",
		})
		f.link("root", child)
		f.link(child, grand)
	}
	return f
}

// TestAssembleSubtree_TwoExecuteCallsAnyWidth pins the whole point of the
// helper: its wire cost is a constant two Execute calls — one traversal, one
// batched depends-on read — no matter how many nodes the subtree holds, and a
// failure of either call degrades the render rather than erroring it.
//
// The width legs are what make the assertion non-vacuous. A per-node walk would
// report a count that RISES with width, so comparing two widths distinguishes
// "batched" from "happened to be small"; asserting 2 at one width alone would
// not.
func TestAssembleSubtree_TwoExecuteCallsAnyWidth(t *testing.T) {
	t.Run("execute count is invariant under subtree width", func(t *testing.T) {
		counts := map[int]int{}
		for _, width := range []int{2, 50} {
			gc := &countingGc{inner: subtreeFixture(width).gc()}
			childIndex, byID, _, truncated := AssembleSubtree(context.Background(), gc, "root", 16)

			assert.False(t, truncated, "an untruncated fixture must not report truncation")
			// Known positive for the count: the walk really did reach every
			// node, so a count of 2 means "batched", not "fetched nothing".
			require.Len(t, byID, width*2, "every child and grandchild must be hydrated")
			require.Len(t, childIndex["root"], width, "every child must be indexed under the root")
			counts[width] = gc.calls
		}
		assert.Equal(t, 2, counts[2], "narrow subtree: one traversal + one depends-on read")
		assert.Equal(t, 2, counts[50], "wide subtree: still one traversal + one depends-on read")
		assert.Equal(t, counts[2], counts[50],
			"wire cost must not scale with node count; got %d at width 2 and %d at width 50",
			counts[2], counts[50])
	})

	t.Run("a failed depends-on read still renders the tree", func(t *testing.T) {
		gc := &countingGc{
			inner:  subtreeFixture(3).gc(),
			failOn: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		}
		childIndex, byID, dependsOn, _ := AssembleSubtree(context.Background(), gc, "root", 16)

		assert.Empty(t, dependsOn, "a failed depends-on read yields no ordering")
		require.Len(t, childIndex["root"], 3, "the subtree survives the ordering failure")

		var sb strings.Builder
		RenderTreeFromIndex(&sb, &knowledgev1.Node{
			Id: "root", SymbolName: "Root", Type: string(kgtypes.NodePlan),
		}, 0, 16, childIndex, dependsOn, nil)
		out := sb.String()
		assert.Contains(t, out, "ID: root")
		for id := range byID {
			assert.Contains(t, out, "ID: "+id, "child %s must still render without depends-on ordering", id)
		}
	})

	t.Run("a failed traversal degrades to a root-only tree", func(t *testing.T) {
		gc := &countingGc{
			inner:  subtreeFixture(3).gc(),
			failOn: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
		}
		childIndex, byID, dependsOn, truncated := AssembleSubtree(context.Background(), gc, "root", 16)

		assert.Empty(t, childIndex)
		assert.Empty(t, byID)
		assert.Empty(t, dependsOn)
		assert.False(t, truncated, "a failed traversal reports no verdict, not a false one")
		assert.Equal(t, 1, gc.calls, "the depends-on read is not issued after the traversal fails")

		var sb strings.Builder
		RenderTreeFromIndex(&sb, &knowledgev1.Node{
			Id: "root", SymbolName: "Root", Type: string(kgtypes.NodePlan),
		}, 0, 16, childIndex, dependsOn, nil)
		assert.Equal(t, 1, strings.Count(sb.String(), "ID: "), "root only")
	})

	t.Run("the traversal's truncation verdict reaches the caller", func(t *testing.T) {
		gc := &truncatingGc{inner: subtreeFixture(3).gc()}
		_, _, _, truncated := AssembleSubtree(context.Background(), gc, "root", 16)
		assert.True(t, truncated, "a clamped traversal must not read as a complete one")
	})
}

// truncatingGc stamps Truncated on the traversal response, which is the only
// way a fixture can express a server ceiling engaging mid-walk.
type truncatingGc struct{ inner GraphCaller }

func (c *truncatingGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (c *truncatingGc) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	resp, err := c.inner.Execute(ctx, req)
	if err == nil && req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		resp.Truncated = true
	}
	return resp, err
}
