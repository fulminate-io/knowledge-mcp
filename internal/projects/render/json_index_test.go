// SPDX-License-Identifier: Apache-2.0

package render

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAssemble_JSONTreeFromIndex_ShapeAndTruncatedKey pins the format=json
// contract across the switch from a per-node recursion to a prefetched index.
// The shape is what callers parse, so every part of it that could silently move
// is asserted: the children key's ABSENCE on a leaf (it is omitempty, and an
// index path that appended an empty slice would emit `"children":[]` instead),
// the ORDER children arrive in, the per-row metadata and updated_at carries, and
// the envelope's truncated key on both values.
func TestAssemble_JSONTreeFromIndex_ShapeAndTruncatedKey(t *testing.T) {
	// A plan with three phases in a known link order, the middle one carrying a
	// step. The other two are leaves, which is what makes the omitted-children
	// assertion non-vacuous — there is a node that genuinely has no children,
	// alongside one that does.
	fixture := func() *graphFixture {
		f := newGraphFixture()
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: "p", SymbolName: "Plan", Type: string(kgtypes.NodePlan), Status: "active",
			UpdatedAt: 1785548993004179000,
			Metadata:  map[string]string{"author": "someone"},
		})
		for _, id := range []string{"ph-a", "ph-b", "ph-c"} {
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: id, SymbolName: "Phase " + id, Type: string(kgtypes.NodePhase), Status: "pending",
			})
			f.link("p", id)
		}
		f.addKnowledgeNode(&knowledgev1.Node{
			Id: "st", SymbolName: "Step", Type: string(kgtypes.NodeStep), Status: "pending",
		})
		f.link("ph-b", "st")
		return f
	}

	type row struct {
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		Type      string            `json:"type"`
		UpdatedAt int64             `json:"updated_at"`
		Metadata  map[string]string `json:"metadata"`
		Children  []row             `json:"children"`
	}
	type envelope struct {
		Root      row   `json:"root"`
		Truncated *bool `json:"truncated"`
	}

	decode := func(t *testing.T, raw string) envelope {
		t.Helper()
		var env envelope
		require.NoError(t, json.Unmarshal([]byte(raw), &env), "payload: %s", raw)
		return env
	}

	t.Run("children ride the index in structure-edge order", func(t *testing.T) {
		env := decode(t, assembleJSONPayload(t, fixture(), map[string]any{"id": "p", "format": "json"}))

		require.Len(t, env.Root.Children, 3)
		var got []string
		for _, c := range env.Root.Children {
			got = append(got, c.ID)
		}
		assert.Equal(t, []string{"ph-a", "ph-b", "ph-c"}, got,
			"children keep the order their contains edges were discovered in")
		require.Len(t, env.Root.Children[1].Children, 1, "the grandchild rides the same index")
		assert.Equal(t, "st", env.Root.Children[1].Children[0].ID)
	})

	t.Run("a childless node emits no children key at all", func(t *testing.T) {
		raw := assembleJSONPayload(t, fixture(), map[string]any{"id": "p", "format": "json"})

		// Decoded into map[string]any so key ABSENCE is distinguishable from a
		// present-but-empty array; a typed struct would read both as nil.
		var loose map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &loose))
		root, ok := loose["root"].(map[string]any)
		require.True(t, ok)
		kids, ok := root["children"].([]any)
		require.True(t, ok, "the root has children, so its key must be present — the control for the assertion below")
		require.Len(t, kids, 3)

		leaf, ok := kids[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "ph-a", leaf["id"])
		assert.NotContains(t, leaf, "children", "a leaf must omit the key, not carry an empty array")
	})

	t.Run("metadata and updated_at still ride each row", func(t *testing.T) {
		env := decode(t, assembleJSONPayload(t, fixture(), map[string]any{"id": "p", "format": "json"}))
		assert.Equal(t, int64(1785548993004179000), env.Root.UpdatedAt)
		assert.Equal(t, map[string]string{"author": "someone"}, env.Root.Metadata)
	})

	t.Run("the envelope carries truncated for both values", func(t *testing.T) {
		env := decode(t, assembleJSONPayload(t, fixture(), map[string]any{"id": "p", "format": "json"}))
		require.NotNil(t, env.Truncated, "the key is emitted unconditionally; absent is indistinguishable from an old binary")
		assert.False(t, *env.Truncated)

		// The same read against a clamped traversal — the known positive that
		// makes the false above mean something.
		blocks := blocksOf(t, &truncatingGc{inner: fixture().gc()}, map[string]any{"id": "p", "format": "json"})
		require.GreaterOrEqual(t, len(blocks), 3, "payload + truncation notice + size disclosure")
		clamped := decode(t, blocks[0].Text)
		require.NotNil(t, clamped.Truncated)
		assert.True(t, *clamped.Truncated)
	})
}
