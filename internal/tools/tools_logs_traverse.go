// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// maxChunksPerLogTraversal caps the number of chunks traverseLogs will
// touch per call. Log graphs can hold millions of chunks; without a cap
// a single template walk could try to format and decompress everything.
// Twenty is large enough to show ranging behavior (multiple windows)
// but small enough that the response stays conversational.
const maxChunksPerLogTraversal = 20

// maxExampleEntriesPerChunk limits how many timestamp + var rows the
// handler decodes from any one chunk. Chunks commonly hold thousands of
// entries — we show the first few and leave the rest to log_query.
const maxExampleEntriesPerChunk = 3

// traverseLogs dispatches a log-graph walk by inspecting the start
// node's type. Two shapes are supported:
//
//   - LogTemplate (direction='down'): the template's chunks and a short
//     sample of their decompressed entries.
//   - LogStream (direction='both'): the stream's shared labels, the
//     chunks that belong to it, and any cloud-graph proxies reachable
//     via EMITTED_BY edges from the labels.
//
// Any other starting node type is a user error — the contract for a
// log-graph traversal is that the caller already knows which node it
// wants to explore (typically from a searchLogs result or a log_query
// drill-down).
//
// This handler is kept specialized because log template/stream rendering
// needs zstd decompression of chunk nodes — not a pure edge-first walk.
// BCN11.3: bulk-fetch via getOrFetchLogState; all subsequent lookups
// hit the pre-fetched *logState rather than the wire.
func (h *Handler) traverseLogs(ctx context.Context, a traverseArgs) kgtools.ToolResult {
	engine, st, err := h.getOrFetchLogState(ctx, a.Name)
	if err != nil {
		return kgtools.ErrorResult(err.Error())
	}
	if st == nil {
		return kgtools.ErrorResult(fmt.Sprintf(
			"traverseLogs %q: no pre-fetched log state (graph empty?)", a.Name))
	}

	// Resolve the start input through the engine first so callers can pass
	// either a stream/template alias or a raw hex ID. Engine may be nil
	// when the graph is empty — fall through to a raw ID lookup so a
	// missing engine doesn't block traversal.
	resolvedStart := a.Start
	if engine != nil {
		if id, ok := engine.ResolveStreamID(a.Start); ok {
			resolvedStart = id
		} else if id, ok := engine.ResolveTemplateID(a.Start); ok {
			resolvedStart = id
		}
	}

	start, ok := st.NodeByID(resolvedStart)
	if !ok {
		return kgtools.ErrorResult(fmt.Sprintf(
			"start %q not found as stream alias, template alias, or hex ID in log graph %q",
			a.Start, a.Name,
		))
	}

	switch kgtypes.NodeType(start.Type) {
	case kgtypes.NodeLogTemplate:
		return traverseLogTemplate(st, a.Name, start)
	case kgtypes.NodeLogStream:
		return traverseLogStream(ctx, st, a.Name, start)
	default:
		return kgtools.ErrorResult(fmt.Sprintf(
			"log traversal starts at a log-template or log-stream node (got %s). "+
				"Use searchLogs or log_query to find a valid start point.",
			start.Type,
		))
	}
}

// traverseLogTemplate walks a template's contains-children (chunks) and
// renders a short decoded sample of each. The cap (maxChunksPerLogTraversal)
// keeps the response bounded; the skipped count is surfaced so callers
// know more data is available via log_query.
func traverseLogTemplate(
	st *logState, queryID string, template *knowledgev1.Node,
) kgtools.ToolResult {
	chunks := collectChildNodesOfType(st, template.Id,
		kgwire.OutgoingEdges, kgtypes.EdgeContains, kgtypes.NodeLogChunk)

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log template %s (graph %q)\n\n", template.Id, queryID)
	if pattern := templatePattern(template); pattern != "" {
		fmt.Fprintf(&sb, "Pattern: %s\n", pattern)
	}
	writeTemplateMeta(&sb, template)
	if len(chunks) == 0 {
		sb.WriteString("\nNo chunks stored for this template.\n")
		return kgtools.TextResult(sb.String())
	}
	fmt.Fprintf(&sb, "\n### Chunks (%d total", len(chunks))
	if len(chunks) > maxChunksPerLogTraversal {
		fmt.Fprintf(&sb, ", showing first %d", maxChunksPerLogTraversal)
	}
	sb.WriteString(")\n\n")
	for i, c := range chunks {
		if i >= maxChunksPerLogTraversal {
			break
		}
		renderChunkExample(&sb, i+1, c)
	}
	return kgtools.TextResult(sb.String())
}

// templatePattern returns the template's search-visible pattern from
// SymbolName, where templateNode writes it.
func templatePattern(n *knowledgev1.Node) string {
	return n.SymbolName
}

// writeTemplateMeta prints the non-pattern template metadata fields that
// matter for orientation (severity, count, time range). Missing fields
// are silently skipped so empty metadata doesn't produce blank lines.
func writeTemplateMeta(sb *strings.Builder, n *knowledgev1.Node) {
	if sev := kgtypes.Value(n, "severity"); sev != "" {
		fmt.Fprintf(sb, "Severity: %s\n", sev)
	}
	if count := kgtypes.Value(n, "count"); count != "" {
		fmt.Fprintf(sb, "Count: %s\n", count)
	}
	if first := kgtypes.Value(n, "first_seen"); first != "" {
		fmt.Fprintf(sb, "First seen: %s\n", first)
	}
	if last := kgtypes.Value(n, "last_seen"); last != "" {
		fmt.Fprintf(sb, "Last seen: %s\n", last)
	}
}

// renderChunkExample prints a header for the chunk plus a few decoded
// entries. Decoding failures are reported inline but don't abort the
// traversal — one corrupt chunk shouldn't hide the rest.
func renderChunkExample(sb *strings.Builder, index int, c *knowledgev1.Node) {
	fmt.Fprintf(sb, "**Chunk %d — %s**\n", index, c.Id)
	if count := kgtypes.Value(c, "entry_count"); count != "" {
		fmt.Fprintf(sb, "  Entries: %s", count)
	}
	if start := kgtypes.Value(c, "start_time"); start != "" {
		fmt.Fprintf(sb, ", window %s", start)
	}
	if end := kgtypes.Value(c, "end_time"); end != "" {
		fmt.Fprintf(sb, " → %s", end)
	}
	sb.WriteString("\n")
	timestamps, vars, err := decodeChunkNode(c)
	if err != nil {
		fmt.Fprintf(sb, "  (decode error: %v)\n\n", err)
		return
	}
	shown := min(len(timestamps), maxExampleEntriesPerChunk)
	for j := range shown {
		ts := timestamps[j].UTC()
		fmt.Fprintf(sb, "  - %s", ts.Format("2006-01-02T15:04:05Z"))
		if len(vars[j]) > 0 {
			fmt.Fprintf(sb, " vars=%v", vars[j])
		}
		sb.WriteString("\n")
	}
	if len(timestamps) > shown {
		fmt.Fprintf(sb, "  ... %d more entries\n", len(timestamps)-shown)
	}
	sb.WriteString("\n")
}

// decodeChunkNode reconstructs a logwire.LogChunk from the node's Content
// and metadata, then decodes it. Keeping this bridging glue in one
// helper avoids scattering the Content-to-bytes cast across the formatter.
func decodeChunkNode(n *knowledgev1.Node) ([]time.Time, [][]string, error) {
	// Build a minimal LogChunk — only CompressedData is required for
	// DecodeChunk; timestamps/stream IDs are already available via meta.
	chunk := &logwire.LogChunk{CompressedData: []byte(n.Content)}
	return logs.DecodeChunk(chunk)
}

// collectChildNodesOfType walks one edge type in a single direction from
// nodeID and returns the peer nodes that match nodeType. Peers that
// aren't present in the pre-fetched state or have the wrong type are
// silently skipped so a partial walk still produces useful output.
func collectChildNodesOfType(
	st *logState, nodeID string,
	direction kgwire.EdgeDirection, edgeType kgtypes.EdgeType, nodeType kgtypes.NodeType,
) []*knowledgev1.Node {
	peerIDs := collectEdgePeerIDs(st, nodeID, direction, edgeType)
	var out []*knowledgev1.Node
	for _, pid := range peerIDs {
		n, ok := st.NodeByID(pid)
		if !ok {
			continue
		}
		if kgtypes.NodeType(n.Type) != nodeType {
			continue
		}
		out = append(out, n)
	}
	return out
}

// collectEdgePeerIDs returns the peer IDs reachable from nodeID via the
// given edge type in the given direction. Incoming edges yield FromID;
// outgoing edges yield ToID.
func collectEdgePeerIDs(
	st *logState, nodeID string,
	direction kgwire.EdgeDirection, edgeType kgtypes.EdgeType,
) []string {
	var out []string
	edges := st.EdgesOf(nodeID, direction, []kgtypes.EdgeType{edgeType})
	for i := range edges {
		e := &edges[i]
		switch direction {
		case kgwire.OutgoingEdges:
			out = append(out, e.ToId)
		case kgwire.IncomingEdges:
			out = append(out, e.FromId)
		case kgwire.BothEdges:
			if e.FromId == nodeID {
				out = append(out, e.ToId)
			} else {
				out = append(out, e.FromId)
			}
		}
	}
	return out
}

// Stream-side traversal helpers live in tools_logs_traverse_stream.go.
// Keeping them separate keeps each file under the 300-line soft cap and
// makes the split-by-node-type structure easy to follow.
