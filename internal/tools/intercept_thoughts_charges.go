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
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"

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

	// Resolve BEFORE reading, through the same resolution point the charge write
	// uses. The resolved slice then feeds ALL THREE consumers below — the fetch,
	// the json map and the text summary. Rewiring only the fetch would leave the
	// DEFAULT (text) arm printing the caller's prefix against a zero count, which
	// is this defect reproduced inside its own fix.
	resolved, rerr := resolveChargesForIDs(ctx, gc, a.ThoughtIDs)
	if rerr != "" {
		return errorResult(rerr)
	}

	chargesByThought := clientthought.FetchChargesFor(ctx, gc, resolved)

	if a.Format == "json" {
		return jsonResult(map[string]any{"charges_by_thought": chargesByThought})
	}
	return textResult(formatChargesForSummary(resolved, chargesByThought))
}

// resolveChargesForIDs maps each caller-supplied thought id to its canonical
// full id, returning the resolved slice and an error message ("" when ok).
//
// BOUNDED: an id already 32+ chars is taken verbatim with NO read, mirroring the
// server write path's verbatim gate, so the common bulk case pays nothing. Only
// a TRUNCATED id costs one ById probe, and there is no batch alternative — the
// plural-ids carrier is deliberately not a prefix carrier server-side.
//
// The output is keyed by RESOLVED ids so charges_for(prefix) and
// charges_for(full id) return identical output, and so the key matches the
// "Charged: <id>" line the charge response prints — one canonical id across the
// whole charge-then-read-back loop.
//
// DELIBERATE ASYMMETRY: a TRUNCATED id that does not resolve is a LOUD error,
// because there is no usable key to report it under. A FULL id that does not
// exist stays silently absent from the map — unchanged behavior, and the
// documented bulk-hydrate contract (missing ids are skipped, not failed). Do not
// "fix" the second; existing behavior depends on it.
func resolveChargesForIDs(ctx context.Context, gc GraphCaller, ids []string) ([]string, string) {
	if len(ids) == 0 {
		return nil, ""
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		full := id
		if len(id) < 32 {
			node, ferr := render.FetchNode(ctx, gc, id)
			if ferr != nil {
				return nil, fmt.Sprintf(
					"charges_for: thought id %s did not resolve - no charges were read: %s", id, ferr.Error())
			}
			if node == nil || node.Id == "" {
				return nil, fmt.Sprintf("charges_for: thought id %s not found - no charges were read", id)
			}
			full = node.Id
		}
		// De-duplicate so a caller passing both a prefix and the full id of one
		// thought does not produce a duplicated key and a duplicated hydrate.
		if seen[full] {
			continue
		}
		seen[full] = true
		out = append(out, full)
	}
	return out, ""
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
