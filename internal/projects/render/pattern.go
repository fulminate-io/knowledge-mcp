// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// assemblePatternIn renders a granular pattern node (NodePattern) as a
// markdown tree. Follows the same shape as assembleTicket:
//
//   - Header: name + summary + description + ID
//   - ## Applies when   — use_case targets via applies-when edges
//   - ## Avoid when     — use_case targets via avoid-when edges
//   - ## Examples       — example children via contains edges
//   - ## References     — reference targets via references edges
//
// Empty sections render nothing (no placeholder) — the planner agent
// reads absence as absence. The edge-bucketing loop runs once and
// dispatches targets into four slices, then the renderPattern* helpers
// format each section.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_containers_pattern.go:30
// with the store.DB parameter replaced by (graphType, graphName) so the
// caller's resolveAssembleNode result drives the cross-graph routing
// transparently. Pattern children live in whichever practice/<lang>
// graph the pattern itself lives in.
func assemblePatternIn(ctx context.Context, gc GraphCaller, node *knowledgev1.Node, graphType, graphName string) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Pattern: %s\n\n", node.SymbolName)
	if node.Summary != "" {
		fmt.Fprintf(&sb, "%s\n\n", node.Summary)
	}
	if node.Description != "" {
		fmt.Fprintf(&sb, "%s\n\n", node.Description)
	}
	// Some practice patterns (e.g. raw code-snippet imports from
	// concurrency-in-go-src) carry the canonical body in node.Content
	// with no Description / use_cases / examples-via-edges. Render
	// Content so those thin pattern nodes don't return as a bare name
	// + ID.
	if c := strings.TrimSpace(node.Content); c != "" {
		fmt.Fprintf(&sb, "%s\n\n", c)
	}
	fmt.Fprintf(&sb, "ID: %s\n", node.Id)

	appliesWhen, avoidWhen, examples, refs := bucketPatternChildrenIn(ctx, gc, node.Id, graphType, graphName)

	sb.WriteString(renderPatternUseCases("## Applies when", appliesWhen))
	sb.WriteString(renderPatternUseCases("## Avoid when", avoidWhen))
	sb.WriteString(renderPatternExamples(examples))
	sb.WriteString(renderPatternReferences(refs))

	// Fallback when the pattern has no children: render carried
	// metadata (file_path / language / repo / relpath / attribution /
	// etc.) so a thin pattern still surfaces the author's curatorial
	// signal.
	if len(appliesWhen) == 0 && len(avoidWhen) == 0 && len(examples) == 0 && len(refs) == 0 {
		sb.WriteString(renderNodeMetadata(node))
	}

	return kgtools.TextResult(sb.String())
}

// renderNodeMetadata emits a `## Metadata` section with the node's
// non-empty inline key-value pairs, sorted by key. Empty section
// returns "" so nodes without metadata don't emit a stray header.
// Verbatim port of tools_assemble_containers_pattern.go:72 — pure
// formatter, no wire calls.
func renderNodeMetadata(node *knowledgev1.Node) string {
	if len(node.Metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(node.Metadata))
	for k := range node.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	first := true
	for _, k := range keys {
		v := node.Metadata[k]
		if v == "" {
			continue
		}
		if first {
			sb.WriteString("\n## Metadata\n\n")
			first = false
		}
		fmt.Fprintf(&sb, "- **%s**: %s\n", k, v)
	}
	return sb.String()
}

// bucketPatternChildrenIn walks the outgoing edges of a pattern node
// once and returns four slices, one per rendered section. Targets are
// resolved via FetchNodeIn against the source graph (practice/<lang>);
// unresolved targets are skipped silently (same broken-link
// tolerance as the ticket renderer).
//
// Ported from tools_assemble_containers_pattern.go:104 with the
// store.DB parameter replaced by (graphType, graphName) and the
// per-target Query reads swapped for FetchNodeIn calls.
func bucketPatternChildrenIn(
	ctx context.Context,
	gc GraphCaller,
	patternID, graphType, graphName string,
) (applies, avoid, examples, refs []*knowledgev1.Node) {
	outEdges, _ := IterEdgesIn(ctx, gc, patternID, graphType, graphName, kgwire.OutgoingEdges)
	for _, e := range outEdges {
		n, err := FetchNodeIn(ctx, gc, e.ToId, graphType, graphName)
		if err != nil || n == nil {
			continue
		}
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeAppliesWhen:
			if kgtypes.NodeType(n.Type) == kgtypes.NodeUseCase {
				applies = append(applies, n)
			}
		case kgtypes.EdgeAvoidWhen:
			if kgtypes.NodeType(n.Type) == kgtypes.NodeUseCase {
				avoid = append(avoid, n)
			}
		case kgtypes.EdgeKGContains:
			if kgtypes.NodeType(n.Type) == kgtypes.NodeExample {
				examples = append(examples, n)
			}
		case kgtypes.EdgeReferences:
			if kgtypes.NodeType(n.Type) == kgtypes.NodeReference {
				refs = append(refs, n)
			}
		}
	}
	return applies, avoid, examples, refs
}

// renderPatternUseCases renders an Applies-when / Avoid-when section
// given a header and a slice of use_case nodes. Empty slice → empty
// string (no header, no body). Each bullet shows the use_case name
// and a truncated description on an indented follow-up line.
// Verbatim port of tools_assemble_containers_pattern.go:140.
func renderPatternUseCases(header string, cases []*knowledgev1.Node) string {
	if len(cases) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n\n", header)
	for _, c := range cases {
		fmt.Fprintf(&sb, "- %s\n", c.SymbolName)
		if c.Description != "" {
			fmt.Fprintf(&sb, "  %s\n", truncate(c.Description, 240))
		}
	}
	return sb.String()
}

// renderPatternExamples renders the `## Examples` section. Each
// example becomes a fenced code block using the `language` metadata
// key for the fence tag, with the example's Content as the body.
// When the example carries an `attribution` metadata value, a
// `Source:` citation line follows the block. Empty slice → empty
// string. Verbatim port of tools_assemble_containers_pattern.go:160.
func renderPatternExamples(examples []*knowledgev1.Node) string {
	if len(examples) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Examples\n\n")
	for _, ex := range examples {
		if ex.SymbolName != "" {
			fmt.Fprintf(&sb, "### %s\n\n", ex.SymbolName)
		}
		lang := kgtypes.Value(ex, "language")
		fmt.Fprintf(&sb, "```%s\n%s\n```\n", lang, ex.Content)
		if attribution := kgtypes.Value(ex, "attribution"); attribution != "" {
			fmt.Fprintf(&sb, "Source: %s\n", attribution)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderPatternReferences renders the `## References` section. Each
// reference is a single bullet. The URL metadata key is preferred;
// if absent, the SymbolName (title) is used. Optional `book`,
// `page`, and `line` metadata keys are appended in parentheses when
// present. Empty slice → empty string. Verbatim port of
// tools_assemble_containers_pattern.go:185.
func renderPatternReferences(refs []*knowledgev1.Node) string {
	if len(refs) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## References\n\n")
	for _, r := range refs {
		sb.WriteString("- ")
		if url := kgtypes.Value(r, "url"); url != "" {
			if r.SymbolName != "" {
				fmt.Fprintf(&sb, "[%s](%s)", r.SymbolName, url)
			} else {
				sb.WriteString(url)
			}
		} else if r.SymbolName != "" {
			sb.WriteString(r.SymbolName)
		}
		writeReferenceCitation(&sb, r)
		sb.WriteString("\n")
	}
	return sb.String()
}

// writeReferenceCitation appends optional book / page / line suffixes
// in parentheses when any of the three metadata keys is set on the
// reference node. Verbatim port of
// tools_assemble_containers_pattern.go:212.
func writeReferenceCitation(sb *strings.Builder, r *knowledgev1.Node) {
	book := kgtypes.Value(r, "book")
	page := kgtypes.Value(r, "page")
	line := kgtypes.Value(r, "line")
	if book == "" && page == "" && line == "" {
		return
	}
	var parts []string
	if book != "" {
		parts = append(parts, book)
	}
	if page != "" {
		parts = append(parts, "p. "+page)
	}
	if line != "" {
		parts = append(parts, "line "+line)
	}
	fmt.Fprintf(sb, " (%s)", strings.Join(parts, ", "))
}
