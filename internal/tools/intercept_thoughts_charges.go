// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_charges.go — client-side claim for
// thoughts(operation:charges_for). The legacy server-side dispatch case was
// deleted during the engine-only cutover without being re-claimed in the
// client intercept chain; this restores it as a thin parse → FetchChargesFor →
// render handler. The bulk charge fetch (ONE edges read + ONE bulk node hydrate,
// charged-by join in caller order) lives entirely in fetchChargesFor
// (cmd/knowledge/internal/thought/wire.go) — nothing is reimplemented here.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// chargesForClientArgs is the parsed thoughts(operation:charges_for) shape:
// thought_ids is the REQUIRED set of thought node IDs whose charges to fetch,
// and format selects the render arm.
type chargesForClientArgs struct {
	ThoughtIDs []string `json:"thought_ids"`
	Format     string   `json:"format"`
}

// handleChargesForClient claims thoughts(operation:charges_for). It parses the
// args, requires a non-empty thought_ids (the documented contract), nil-checks
// the graph client (a plain GraphCaller engine read — no readiness gate), and
// delegates to FetchChargesFor, returning the documented charges_by_thought
// shape ({tid: [charge_node, ...]}).
func handleChargesForClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a chargesForClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if len(a.ThoughtIDs) == 0 {
		return errorResult("charges_for: thought_ids is required (one or more thought node IDs)")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("charges_for: graph client unavailable")
	}

	chargesByThought := clientthought.FetchChargesFor(ctx, gc, a.ThoughtIDs)

	if a.Format == "json" {
		return jsonResult(map[string]any{"charges_by_thought": chargesByThought})
	}
	return textResult(formatChargesForSummary(a.ThoughtIDs, chargesByThought))
}

// formatChargesForSummary renders a compact per-thought text summary: for each
// requested thought id, the count of charges and their polarity/weight. Thought
// ids with no charges are reported as such (FetchChargesFor omits them from the
// map). Iterates in the caller-supplied id order.
func formatChargesForSummary(thoughtIDs []string, byThought map[string][]*knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Charges for %d thought(s):\n", len(thoughtIDs))
	for _, tid := range thoughtIDs {
		charges := byThought[tid]
		fmt.Fprintf(&sb, "  %s: %d charge(s)\n", tid, len(charges))
		for _, c := range charges {
			fmt.Fprintf(&sb, "    - %s polarity=%s weight=%s\n",
				c.GetId(), kgtypes.Value(c, "polarity"), kgtypes.Value(c, "weight"))
		}
	}
	return sb.String()
}
