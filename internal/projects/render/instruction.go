// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// assembleInstruction renders a NodeAgent / NodeSkill / NodeToolGuide:
// header + Content (the authoritative spec, typically markdown
// frontmatter + body) + RenderTree walk + Tool Guides (via EdgeUses)
// + Constraining Rules (via incoming EdgeConstrains) + Used Skills
// (for NodeAgent, via EdgeUses to NodeSkill).
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:351 with
// store reads swapped for wire-shape FetchNode + IterEdges calls.
// Per the FUL-251 plan: NO practice-graph lookup happens here — the
// server-side path resolved instruction nodes via the knowledge
// graph + the practice fallback, but client-side assemble only
// resolves through the knowledge graph (Phase 3's Handle dispatches
// instruction nodes only when found in knowledge — practice
// fallback exists only for patterns).
func assembleInstruction(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s: %s\n\n", node.Type, node.SymbolName)
	// Agent / skill / tool_guide nodes carry their authoritative spec
	// in node.Content (typically a markdown frontmatter + body).
	// Surface it before the tree walk so consumers see the actual
	// instructions, not just the bare name + ID.
	if c := strings.TrimSpace(node.Content); c != "" {
		sb.WriteString(c)
		sb.WriteString("\n\n")
	}
	RenderTree(ctx, gc, &sb, node, 0, 3)

	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)

	// Follow EdgeUses to tool_guides.
	var guides []*knowledgev1.Node
	for _, e := range outEdges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeUses {
			gn, err := FetchNode(ctx, gc, e.ToId)
			if err == nil && gn != nil {
				guides = append(guides, gn)
			}
		}
	}
	if len(guides) > 0 {
		fmt.Fprintf(&sb, "\n## Tool Guides\n\n")
		for _, g := range guides {
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", g.Type, g.SymbolName, g.Id)
		}
	}

	// Find constraining rules via inbound EdgeConstrains.
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges, kgtypes.EdgeConstrains)
	var rules []*knowledgev1.Node
	for _, e := range inEdges {
		rn, err := FetchNode(ctx, gc, e.FromId)
		if err == nil && rn != nil {
			rules = append(rules, rn)
		}
	}
	if len(rules) > 0 {
		fmt.Fprintf(&sb, "\n## Constraining Rules\n\n")
		for _, r := range rules {
			fmt.Fprintf(&sb, "- %s — ID: %s\n", r.SymbolName, r.Id)
			if r.Description != "" {
				fmt.Fprintf(&sb, "  %s\n", truncate(r.Description, 120))
			}
		}
	}

	// For agents: also follow EdgeUses to skills.
	if kgtypes.NodeType(node.Type) == kgtypes.NodeAgent {
		renderAgentSkills(ctx, gc, &sb, outEdges)
	}
	return kgtools.TextResult(sb.String())
}

// renderAgentSkills writes the `## Used Skills` section by walking
// the outgoing-EdgeUses targets and keeping only NodeSkill nodes.
// Verbatim port of cmd/knowledge-server/tools/tools_assemble.go:415
// with the store reads swapped for FetchNode calls.
func renderAgentSkills(ctx context.Context, gc GraphCaller, sb *strings.Builder, outEdges []*knowledgev1.Edge) {
	var skills []*knowledgev1.Node
	for _, e := range outEdges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeUses {
			sn, err := FetchNode(ctx, gc, e.ToId)
			if err == nil && sn != nil && kgtypes.NodeType(sn.Type) == kgtypes.NodeSkill {
				skills = append(skills, sn)
			}
		}
	}
	if len(skills) > 0 {
		fmt.Fprintf(sb, "\n## Used Skills\n\n")
		for _, s := range skills {
			fmt.Fprintf(sb, "- %s — ID: %s\n", s.SymbolName, s.Id)
		}
	}
}
