// SPDX-License-Identifier: Apache-2.0

package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sizeToken is the locked two-word literal the rider's format string carries.
// The census criterion greps for this same token in both directions, so the
// test and the gate measure one string rather than two loosely related ones.
const sizeToken = "rendered bytes"

// sizeFixture is a small plan tree: a plan with two phases, each with a step.
func sizeFixture() *graphFixture {
	f := newGraphFixture()
	f.addKnowledgeNode(&knowledgev1.Node{
		Id: "plan-1", SymbolName: "A Plan", Type: string(kgtypes.NodePlan), Status: "active",
	})
	for _, p := range []struct{ id, step string }{{"phase-1", "step-1"}, {"phase-2", "step-2"}} {
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: p.id, SymbolName: "Phase " + p.id, Type: string(kgtypes.NodePhase), Status: "pending",
		})
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: p.step, SymbolName: "Step " + p.step, Type: string(kgtypes.NodeStep), Status: "pending",
		})
		f.link("plan-1", p.id)
		f.link(p.id, p.step)
	}
	return f
}

// TestAssemble_SizeDisclosure_SeparateTrailingBlock pins the rider's three
// load-bearing properties: it rides EVERY arm (text and json alike), it is a
// SEPARATE trailing block rather than text spliced into the payload — which is
// what keeps a format=json result independently parseable — and when a
// truncation notice is also present the notice comes first, because the notice
// is about the completeness of the content and the size line is about the
// content's cost.
func TestAssemble_SizeDisclosure_SeparateTrailingBlock(t *testing.T) {
	t.Run("a text arm carries the rider as its own trailing block", func(t *testing.T) {
		blocks := blocksOf(t, sizeFixture().gc(), map[string]any{"id": "plan-1"})
		require.GreaterOrEqual(t, len(blocks), 2, "the tree and the rider are separate blocks")

		last := blocks[len(blocks)-1]
		assert.Contains(t, last.Text, sizeToken, "the last block is the size disclosure")
		// Known positive for separateness: the tree block must be non-empty and
		// must NOT itself carry the token. Without this leg a single
		// concatenated block would satisfy "the last block contains the token".
		body := blocks[len(blocks)-2]
		assert.NotEmpty(t, body.Text, "the rendered tree block must not be empty")
		assert.NotContains(t, body.Text, sizeToken, "the rider must not be spliced into the payload")
	})

	t.Run("the json arm's payload block is untouched and still unmarshals", func(t *testing.T) {
		blocks := blocksOf(t, sizeFixture().gc(), map[string]any{"id": "plan-1", "format": "json"})
		require.GreaterOrEqual(t, len(blocks), 2)

		payload := blocks[len(blocks)-2]
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload.Text), &decoded),
			"the payload block must remain independently parseable")
		assert.NotContains(t, payload.Text, sizeToken, "the rider must not be inside the json payload")
		assert.Contains(t, blocks[len(blocks)-1].Text, sizeToken)
	})

	t.Run("the truncation notice precedes the size line", func(t *testing.T) {
		gc := &truncatingGc{inner: sizeFixture().gc()}
		blocks := blocksOf(t, gc, map[string]any{"id": "plan-1"})

		noticeAt, sizeAt := -1, -1
		for i, b := range blocks {
			if strings.Contains(b.Text, "server row ceiling engaged") {
				noticeAt = i
			}
			if strings.Contains(b.Text, sizeToken) {
				sizeAt = i
			}
		}
		require.NotEqual(t, -1, noticeAt,
			"a clamped traversal must produce a truncation notice — without one this ordering assertion is vacuous")
		require.NotEqual(t, -1, sizeAt)
		assert.Less(t, noticeAt, sizeAt, "the truncation notice comes before the size line")
	})
}
