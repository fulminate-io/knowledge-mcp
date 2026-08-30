// SPDX-License-Identifier: Apache-2.0

// Package render — the closed allowed-difference list, pinned.
//
// The repoint from a per-node tree walk to a prefetched index is an equivalence
// claim with a short list of sanctioned exceptions. Each test below pins one
// entry on that list, so a future change cannot move any of them quietly.

package render

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAssemble_DiamondContains_RendersOnce pins the diamond-dedup difference: a
// node reached by contains edges from TWO parents renders ONCE, under the first
// edge that reaches it, where the per-node walk rendered it under both.
//
// THE COUNT IS THE ASSERTION. A "contains the id" check passes for one
// occurrence and for two alike, which is exactly the distinction this test
// exists to make — so it counts the node's ID line instead.
func TestAssemble_DiamondContains_RendersOnce(t *testing.T) {
	f := newGraphFixture().
		addKnowledgeNode(&knowledgev1.Node{
			Id: "d-plan", Type: string(kgtypes.NodePlan), SymbolName: "diamond-plan", Status: kgtypes.StatusActive,
		}).
		addKnowledgeNode(&knowledgev1.Node{
			Id: "d-ph-1", Type: string(kgtypes.NodePhase), SymbolName: "phase-one", Status: kgtypes.StatusActive,
		}).
		addKnowledgeNode(&knowledgev1.Node{
			Id: "d-ph-2", Type: string(kgtypes.NodePhase), SymbolName: "phase-two", Status: kgtypes.StatusActive,
		}).
		addKnowledgeNode(&knowledgev1.Node{
			Id: "d-shared", Type: string(kgtypes.NodeStep), SymbolName: "shared-step", Status: kgtypes.StatusPending,
		}).
		link("d-plan", "d-ph-1").
		link("d-plan", "d-ph-2").
		link("d-ph-1", "d-shared").
		link("d-ph-2", "d-shared")

	out, err := callRender(context.Background(), f, map[string]any{"id": "d-plan"})
	require.NoError(t, err)

	// Control: both parents render, so a render that lost a whole branch cannot
	// satisfy the single-occurrence assertion by accident.
	require.Equal(t, 1, strings.Count(out, "ID: d-ph-1"))
	require.Equal(t, 1, strings.Count(out, "ID: d-ph-2"))

	assert.Equal(t, 1, strings.Count(out, "ID: d-shared"),
		"a node contained by two parents renders once, under the first contains edge that reaches it")
}

// TestAssemble_TombstonedDescendant_AbsentFromTree is a CHARACTERIZATION GUARD,
// green before and after the repoint. A tombstoned child is a NON-difference:
// the structure-edge carrier drops any edge whose peer is tombstoned regardless
// of the request's IncludeTombstones flag, so the per-node walk this replaced
// already dropped the same edges.
//
// WHAT THIS TEST DOES AND DOES NOT ADJUDICATE. It pins the CLIENT's rendering
// given a fixture that models the server's documented drop — the fixture simply
// omits the contains edge, which is what the server's carrier does. It cannot
// adjudicate the server behaviour itself. That is pinned server-side by
// TestIterEdges_TombstoneExclusion_Equivalence and
// TestCompositeIterEdges_SingleLayerBaseTombstoneSuppressesEdge, and holds
// because the composite edge iterator's tombstoned-endpoint filter never
// consults the request flag. Do not read a green here as proof of that.
func TestAssemble_TombstonedDescendant_AbsentFromTree(t *testing.T) {
	// The tombstoned node EXISTS in the fixture; only its inbound contains edge
	// is absent, which is precisely the shape the server produces.
	f := newGraphFixture().
		addKnowledgeNode(&knowledgev1.Node{
			Id: "tb-plan", Type: string(kgtypes.NodePlan), SymbolName: "tombstone-plan", Status: kgtypes.StatusActive,
		}).
		addKnowledgeNode(&knowledgev1.Node{
			Id: "tb-live", Type: string(kgtypes.NodePhase), SymbolName: "live-phase", Status: kgtypes.StatusActive,
		}).
		addKnowledgeNode(&knowledgev1.Node{
			Id: "tb-dead", Type: string(kgtypes.NodePhase), SymbolName: "tombstoned-phase", Status: kgtypes.StatusActive,
		}).
		link("tb-plan", "tb-live")
		// No link from tb-plan to tb-dead: the server drops that edge.

	t.Run("text", func(t *testing.T) {
		out, err := callRender(context.Background(), f, map[string]any{"id": "tb-plan"})
		require.NoError(t, err)
		// Control: the live sibling renders, so "the dead one is absent" is not
		// satisfied by an empty tree.
		require.Contains(t, out, "ID: tb-live")
		assert.NotContains(t, out, "tb-dead", "a tombstone-dropped edge yields no child row")
	})

	t.Run("json", func(t *testing.T) {
		payload := assembleJSONPayload(t, f, map[string]any{"id": "tb-plan", "format": "json"})
		require.Contains(t, payload, "tb-live")
		assert.NotContains(t, payload, "tb-dead")
	})
}

// TestAssemble_TruncatedTraversal_AppendsNotice pins the third allowed
// difference: a clamped traversal now produces a notice the per-node walk could
// not detect at all.
//
// THE SECOND HALF IS THE ANTI-VACUITY LEG. A test that only checks the notice
// appears when the traversal is truncated cannot distinguish a working verdict
// from a notice appended unconditionally, so the non-truncated case is asserted
// in the same run.
func TestAssemble_TruncatedTraversal_AppendsNotice(t *testing.T) {
	fixture := func() *graphFixture {
		return newGraphFixture().
			addKnowledgeNode(&knowledgev1.Node{
				Id: "tr-plan", Type: string(kgtypes.NodePlan), SymbolName: "truncated-plan", Status: kgtypes.StatusActive,
			}).
			addKnowledgeNode(&knowledgev1.Node{
				Id: "tr-ph", Type: string(kgtypes.NodePhase), SymbolName: "a-phase", Status: kgtypes.StatusActive,
			}).
			link("tr-plan", "tr-ph")
	}
	const notice = "server row ceiling engaged"

	t.Run("a clamped traversal appends the notice as its own block", func(t *testing.T) {
		blocks := blocksOf(t, &truncatingGc{inner: fixture().gc()}, map[string]any{"id": "tr-plan"})

		treeAt, noticeAt := -1, -1
		for i, b := range blocks {
			if strings.Contains(b.Text, "ID: tr-plan") {
				treeAt = i
			}
			if strings.Contains(b.Text, notice) {
				noticeAt = i
			}
		}
		require.NotEqual(t, -1, treeAt, "the tree must render")
		require.NotEqual(t, -1, noticeAt, "a clamped traversal must produce a notice")
		assert.NotEqual(t, treeAt, noticeAt,
			"the notice is a SEPARATE block, never concatenated into the tree text — that separation "+
				"is what keeps a format=json payload independently parseable")
		assert.NotContains(t, blocks[treeAt].Text, notice)
	})

	t.Run("a complete traversal appends nothing", func(t *testing.T) {
		blocks := blocksOf(t, fixture().gc(), map[string]any{"id": "tr-plan"})
		var joined strings.Builder
		for _, b := range blocks {
			joined.WriteString(b.Text)
		}
		require.Contains(t, joined.String(), "ID: tr-plan", "the tree must render — an empty result trivially lacks the notice")
		assert.NotContains(t, joined.String(), notice,
			"an unclamped read must append no notice; otherwise the assertion above proves nothing")
	})

	t.Run("the json envelope's truncated key tracks the verdict", func(t *testing.T) {
		read := func(gc GraphCaller) bool {
			blocks := blocksOf(t, gc, map[string]any{"id": "tr-plan", "format": "json"})
			var env struct {
				Truncated *bool `json:"truncated"`
			}
			require.NoError(t, json.Unmarshal([]byte(blocks[0].Text), &env))
			require.NotNil(t, env.Truncated, "the key is emitted unconditionally")
			return *env.Truncated
		}
		assert.True(t, read(&truncatingGc{inner: fixture().gc()}))
		assert.False(t, read(fixture().gc()))
	})
}
