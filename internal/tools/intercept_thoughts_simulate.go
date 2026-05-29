// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_simulate.go — client-side claim for the thought-simulation
// surface (query(mode:simulate, ...)). This is the only path that produces a real
// response.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// simulateClientArgs is the parsed simulate shape.
type simulateClientArgs struct {
	Action   string  `json:"action"`
	Target   string  `json:"target"`
	Polarity string  `json:"polarity"`
	Weight   float64 `json:"weight"`
	Format   string  `json:"format"`
}

// handleSimulateClient claims simulate. Applies a 10-second timeout and renders
// the markdown body.
func handleSimulateClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a simulateClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("simulate: graph client unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := clientthought.RunSimulation(ctx, gc, a.Action, a.Target, a.Polarity, a.Weight)
	if err != nil {
		if errors.Is(err, graphclient.ErrNotFound) {
			return errorResult(fmt.Sprintf("simulate: %s %s not found", a.Action, a.Target))
		}
		return errorResult("simulation failed: " + err.Error())
	}

	if a.Format == "json" {
		return jsonResult(result)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Simulation: %s\n\n", result.Description)

	for _, change := range result.AffectedThoughts {
		node, ok := clientthought.FetchNode(ctx, gc, change.ThoughtID)
		if !ok {
			fmt.Fprintf(&sb, "## (missing) (%s)\n", change.ThoughtID)
			continue
		}
		fmt.Fprintf(&sb, "## %s (%s)\n", node.SymbolName, change.ThoughtID)
		fmt.Fprintf(&sb, "| property | Before | After | Delta |\n")
		fmt.Fprintf(&sb, "|---|---|---|---|\n")
		fmt.Fprintf(&sb, "| Valence | %.3f | %.3f | %+.3f |\n", change.Before.Valence, change.After.Valence, change.After.Valence-change.Before.Valence)
		fmt.Fprintf(&sb, "| Magnitude | %.3f | %.3f | %+.3f |\n", change.Before.Magnitude, change.After.Magnitude, change.After.Magnitude-change.Before.Magnitude)
		fmt.Fprintf(&sb, "| Consistency | %.3f | %.3f | %+.3f |\n", change.Before.Consistency, change.After.Consistency, change.After.Consistency-change.Before.Consistency)
		fmt.Fprintf(&sb, "| Self-trust | %.3f | %.3f | %+.3f |\n", change.Before.SelfTrust, change.After.SelfTrust, change.After.SelfTrust-change.Before.SelfTrust)
		fmt.Fprintf(&sb, "| Charges | %d | %d | %+d |\n\n", change.Before.ChargeCount, change.After.ChargeCount, change.After.ChargeCount-change.Before.ChargeCount)
	}

	return textResult(sb.String())
}
