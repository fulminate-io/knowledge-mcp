// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// suffixFixtureTS is the seeded UpdatedAt for the suffix tests. Any
// fixed nonzero nanosecond value works; the expected string is always
// derived from it through the same Format call the production helper
// uses, never hard-coded, so the assertions hold in any timezone.
const suffixFixtureTS = int64(1785548993004179000)

// TestAssembleRenders_CarryUpdatedSuffix pins read-time provenance on
// the line carrying each node's own `ID:` across all nine assemble
// dispatch arms (assemble.go:83-102). The arms reach five distinct
// renderers — RenderTree (plan/test_plan/research/agent), the two
// headers (ticket/project), assembleDecision, assemblePatternIn, and
// assembleFallback — so a suffix missed at any one of them fails here.
//
// The zero-timestamp row is the control that keeps the suffix
// conditional: without it an unconditional suffix would pass every
// positive row while silently re-baselining all fifteen render
// goldens. The json subtest is the catcher for render/json.go, which
// the text assertions cannot reach.
func TestAssembleRenders_CarryUpdatedSuffix(t *testing.T) {
	want := time.Unix(0, suffixFixtureTS).Format("2006-01-02 15:04")

	arms := []struct {
		name string
		typ  kgtypes.NodeType
	}{
		{"project", kgtypes.NodeProject},
		{"ticket", kgtypes.NodeTicket},
		{"plan", kgtypes.NodePlan},
		{"test_plan", kgtypes.NodeTestPlan},
		{"research", kgtypes.NodeResearch},
		{"agent", kgtypes.NodeAgent},
		{"decision", kgtypes.NodeDecision},
		{"pattern", kgtypes.NodePattern},
		{"rule_via_fallback", kgtypes.NodeRule},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			id := "root-" + arm.name
			f := newGraphFixture().addKnowledgeNode(&knowledgev1.Node{
				Id:          id,
				SymbolName:  "Root " + arm.name,
				Type:        string(arm.typ),
				Status:      "active",
				Description: "seeded root for the " + arm.name + " arm",
				UpdatedAt:   suffixFixtureTS,
			})

			out, err := callRender(context.Background(), f, map[string]any{"id": id})
			require.NoError(t, err)
			require.Contains(t, out, "ID: "+id+"  (updated "+want+")",
				"the %s arm must carry the updated suffix on the node's own ID line", arm.name)
		})
	}

	t.Run("zero_timestamp_renders_no_suffix", func(t *testing.T) {
		for _, arm := range arms {
			id := "bare-" + arm.name
			f := newGraphFixture().addKnowledgeNode(&knowledgev1.Node{
				Id:         id,
				SymbolName: "Bare " + arm.name,
				Type:       string(arm.typ),
				Status:     "active",
			})

			out, err := callRender(context.Background(), f, map[string]any{"id": id})
			require.NoError(t, err)
			require.NotContains(t, out, "(updated ",
				"the %s arm must emit no suffix when UpdatedAt is unset", arm.name)
		}
	})

	t.Run("json", func(t *testing.T) {
		const id = "root-json"
		f := newGraphFixture().addKnowledgeNode(&knowledgev1.Node{
			Id:         id,
			SymbolName: "Root json",
			Type:       string(kgtypes.NodePlan),
			Status:     "active",
			UpdatedAt:  suffixFixtureTS,
		})

		// The payload BLOCK, not the concatenation of every block: a json result
		// trails a rendered-size disclosure in its own block, so flattening the
		// result and unmarshalling that would fail on the disclosure's text.
		out := assembleJSONPayload(t, f, map[string]any{"id": id, "format": "json"})

		var payload struct {
			Root struct {
				ID        string `json:"id"`
				UpdatedAt int64  `json:"updated_at"`
			} `json:"root"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &payload), "assemble json: %s", out)
		require.Equal(t, id, payload.Root.ID)
		require.Equal(t, suffixFixtureTS, payload.Root.UpdatedAt)
	})

	t.Run("json_zero_timestamp_omits_key", func(t *testing.T) {
		const id = "bare-json"
		f := newGraphFixture().addKnowledgeNode(&knowledgev1.Node{
			Id:         id,
			SymbolName: "Bare json",
			Type:       string(kgtypes.NodePlan),
			Status:     "active",
		})

		out := assembleJSONPayload(t, f, map[string]any{"id": id, "format": "json"})

		var payload struct {
			Root map[string]any `json:"root"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &payload), "assemble json: %s", out)
		require.NotContains(t, payload.Root, "updated_at")
	})
}
