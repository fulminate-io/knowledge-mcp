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
// frontmatter + body) + subtree walk + Tool Guides (via EdgeUses)
// + Constraining Rules (via incoming EdgeConstrains) + Used Skills
// (for NodeAgent, via EdgeUses to NodeSkill).
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:351 with
// store reads swapped for wire-shape FetchNode + IterEdges calls.
// Per the plan: NO practice-graph lookup happens here — the
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
	childIndex, byID, dependsOn, truncated := AssembleSubtree(ctx, gc, node.Id, 3)
	RenderTreeFromIndex(&sb, node, 0, 3, childIndex, dependsOn)

	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges, kgtypes.EdgeConstrains)

	// ONE hydrate covers the uses targets and the constrains sources. The uses
	// targets are read twice below — once as tool guides, once as an agent's
	// skills — and hydrating them once here is what removes the duplicate
	// fetch renderAgentSkills used to issue over this same edge slice.
	peerIDs := make([]string, 0, len(outEdges)+len(inEdges))
	for _, e := range outEdges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeUses {
			peerIDs = append(peerIDs, e.ToId)
		}
	}
	for _, e := range inEdges {
		peerIDs = append(peerIDs, e.FromId)
	}
	peers, peersTruncated, _ := FetchNodesByIDs(ctx, gc, peerIDs)
	truncated = truncated || peersTruncated

	// Follow EdgeUses to tool_guides. Walk the edge slice, not the map.
	var guides []*knowledgev1.Node
	for _, e := range outEdges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeUses {
			continue
		}
		if gn, ok := peers[e.ToId]; ok {
			guides = append(guides, gn)
		}
	}
	if len(guides) > 0 {
		fmt.Fprintf(&sb, "\n## Tool Guides\n\n")
		for _, g := range guides {
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", g.Type, g.SymbolName, g.Id)
		}
	}

	// Constraining rules via inbound EdgeConstrains.
	var rules []*knowledgev1.Node
	for _, e := range inEdges {
		if rn, ok := peers[e.FromId]; ok {
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
		renderAgentSkills(&sb, outEdges, peers)
	}
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID)+len(peers))
}

// renderAgentSkills writes the `## Used Skills` section by walking
// the outgoing-EdgeUses targets and keeping only NodeSkill nodes.
// Ported from cmd/knowledge-server/tools/tools_assemble.go:415.
//
// It takes the caller's already-hydrated peer map rather than a graph caller:
// the caller reads these same uses targets for its Tool Guides section, so
// hydrating them here as well would fetch every one of them twice.
func renderAgentSkills(sb *strings.Builder, outEdges []*knowledgev1.Edge, peers map[string]*knowledgev1.Node) {
	var skills []*knowledgev1.Node
	for _, e := range outEdges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeUses {
			if sn, ok := peers[e.ToId]; ok && kgtypes.NodeType(sn.Type) == kgtypes.NodeSkill {
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
