// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_examine.go — FUL-247 client-side claim for the
// thought-examination surface. The server-side handleExamine body
// is gone post-Phase-5; this is the only path that
// produces a real response.
//
// Reached either through query(mode:examine, id:<thought>) (the canonical
// surface for an examine; routed by interceptQueryReflect after Phase 4)
// or as a future legacy thoughts(operation:examine) entry. The render
// shape matches tools_thought_query.go:159-209 byte-for-byte so smoke
// parity holds.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// examineClientArgs is the parsed examine shape. Either `thought` or
// `id` carries the target — both spellings appear in the wild, with
// query(mode:examine) using `id` and thoughts(operation:examine) using
// `thought`.
type examineClientArgs struct {
	Thought string `json:"thought"`
	ID      string `json:"id"`
	Format  string `json:"format"`
}

// handleExamineClient claims the examine surface. Renders the
// ThoughtExamination via the byte-identical block from
// tools_thought_query.go:159-209 pre-FUL-247.
func handleExamineClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a examineClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	target := a.Thought
	if target == "" {
		target = a.ID
	}
	if target == "" {
		return errorResult("examine: 'thought' (or 'id') is required")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("examine: graph client unavailable")
	}

	exam, err := clientthought.ExamineThought(ctx, gc, target)
	if err != nil {
		return errorResult("examine failed: " + err.Error())
	}

	if a.Format == "json" {
		return jsonResult(exam)
	}

	return textResult(renderExamine(exam))
}

// renderExamine produces the markdown body. Verbatim from
// tools_thought_query.go:159-209 — same field ordering, same field
// labels, same conditional sections.
func renderExamine(exam clientthought.ThoughtExamination) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", exam.Node.SymbolName)
	fmt.Fprintf(&sb, "**Status:** %s\n", exam.Node.Status)
	if exam.SessionName != "" {
		fmt.Fprintf(&sb, "**Session:** %s\n", exam.SessionName)
	}
	if cid := kgtypes.Value(exam.Node, "cluster_id"); cid != "" {
		fmt.Fprintf(&sb, "**Cluster:** %s\n", cid)
	}
	fmt.Fprintf(&sb, "**Created:** %s\n\n", time.Unix(0, exam.Node.CreatedAt).UTC().Format("2006-01-02 15:04"))

	fmt.Fprintf(&sb, "## Content\n%s\n\n", exam.Node.Content)

	fmt.Fprintf(&sb, "## Properties (computed from charges)\n")
	fmt.Fprintf(&sb, "- Valence: %.3f\n", exam.Properties.Valence)
	fmt.Fprintf(&sb, "- Magnitude: %.3f\n", exam.Properties.Magnitude)
	fmt.Fprintf(&sb, "- Consistency: %.3f\n", exam.Properties.Consistency)
	fmt.Fprintf(&sb, "- Self-trust: %.3f\n", exam.Properties.SelfTrust)
	fmt.Fprintf(&sb, "- Charges: %d (positive: %.1f, negative: %.1f)\n\n",
		exam.Properties.ChargeCount, exam.Properties.PositiveWeight, exam.Properties.NegativeWeight)

	if len(exam.Charges) > 0 {
		fmt.Fprintf(&sb, "## Charges (%d)\n", len(exam.Charges))
		for _, c := range exam.Charges {
			polarity := kgtypes.Value(c.Charge, "polarity")
			weight := kgtypes.Value(c.Charge, "weight")
			icon := "+"
			if polarity == "negative" {
				icon = "-"
			}
			fmt.Fprintf(&sb, "  [%s%s] %s\n", icon, weight, c.Charge.Content)
			fmt.Fprintf(&sb, "    ID: %s\n", c.Charge.Id)
			for _, e := range c.Evidence {
				fmt.Fprintf(&sb, "    evidence: [%s] %s (%s)\n", e.Type, e.SymbolName, e.Id)
			}
		}
		sb.WriteString("\n")
	}

	if len(exam.Connections) > 0 {
		fmt.Fprintf(&sb, "## Connections (%d)\n", len(exam.Connections))
		for _, c := range exam.Connections {
			fmt.Fprintf(&sb, "  %s -[%s]-> [%s] %s (%s)\n", c.Direction, c.EdgeType, c.Node.Type, c.Node.SymbolName, c.Node.Id)
		}
	}

	return sb.String()
}
