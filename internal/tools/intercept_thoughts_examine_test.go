// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestRenderExamine_Golden builds a ThoughtExamination fixture and
// renders it through the relocated handler. Asserts every label the
// prior server-side handleExamine produced is present.
func TestRenderExamine_Golden(t *testing.T) {
	exam := clientthought.ThoughtExamination{
		Node: &knowledgev1.Node{
			Id:         "thought-1",
			Type:       string(kgtypes.NodeThought),
			SymbolName: "my hypothesis",
			Content:    "the hypothesis body",
			Status:     kgtypes.StatusHypothesized,
			CreatedAt:  time.Date(2026, 5, 21, 12, 30, 0, 0, time.UTC).UnixNano(),
		},
		SessionName: "ful-247-session",
		Properties: clientthought.ThoughtProperties{
			Valence:        0.75,
			Magnitude:      2.5,
			Consistency:    0.9,
			SelfTrust:      0.6,
			ChargeCount:    3,
			PositiveWeight: 7.0,
			NegativeWeight: 1.0,
		},
		Charges: []clientthought.ChargeDetail{{
			Charge: &knowledgev1.Node{
				Id:      "charge-1",
				Content: "first piece of evidence",
				Metadata: map[string]string{
					"polarity": "positive",
					"weight":   "5.00",
				},
			},
		}},
		Connections: []clientthought.ConnectionDetail{{
			Node: &knowledgev1.Node{
				Id:         "decision-7",
				Type:       string(kgtypes.NodeDecision),
				SymbolName: "downstream decision",
			},
			EdgeType:  kgtypes.EdgeInformedBy,
			Direction: "outgoing",
		}},
	}
	out := renderExamine(exam)
	assert.Contains(t, out, "# my hypothesis")
	assert.Contains(t, out, "**Status:** hypothesized")
	assert.Contains(t, out, "**Session:** ful-247-session")
	assert.Contains(t, out, "**Created:** 2026-05-21 12:30")
	assert.Contains(t, out, "the hypothesis body")
	assert.Contains(t, out, "Valence: 0.750")
	assert.Contains(t, out, "Magnitude: 2.500")
	assert.Contains(t, out, "## Charges (1)")
	assert.Contains(t, out, "[+5.00] first piece of evidence")
	assert.Contains(t, out, "## Connections (1)")
	assert.Contains(t, out, "outgoing -[informed-by]-> [decision] downstream decision (decision-7)")
	// Sanity: render block is non-trivial.
	assert.Greater(t, strings.Count(out, "\n"), 10)
}
