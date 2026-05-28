// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// traverseLogStream follows three edges from the stream: HAS_LABEL
// outgoing to the shared label nodes, BELONGS_TO incoming from the
// chunks that were assembled against this stream, and EMITTED_BY
// outgoing from each label to the cloud-graph proxies that the
// pipeline wired up. Every section is capped; totals are surfaced when
// the cap hides data.
func traverseLogStream(
	ctx context.Context, st *logState, queryID string, stream *knowledgev1.Node,
) kgtools.ToolResult {
	var sb strings.Builder
	alias := streamAlias(stream)
	if alias != "" {
		fmt.Fprintf(&sb, "## Log stream `%s` (graph %q)\n", alias, queryID)
		fmt.Fprintf(&sb, "**Alias:** `%s`\n", alias)
	} else {
		fmt.Fprintf(&sb, "## Log stream %s (graph %q)\n", stream.Id, queryID)
	}
	fmt.Fprintf(&sb, "**ID:** `%s`\n\n", stream.Id)
	writeStreamLabels(&sb, stream)

	labels := collectChildNodesOfType(st, stream.Id,
		kgwire.OutgoingEdges, kgtypes.EdgeHasLabel, kgtypes.NodeLogLabel)
	writeSharedLabelSection(ctx, &sb, st, labels)

	chunks := collectChildNodesOfType(st, stream.Id,
		kgwire.IncomingEdges, kgtypes.EdgeBelongsTo, kgtypes.NodeLogChunk)
	tmplAliases := lookupTemplateAliases(st, chunks)
	writeStreamChunkSection(&sb, chunks, tmplAliases)
	writeStreamCorrelations(&sb, st, stream, chunks)

	return kgtools.TextResult(sb.String())
}

// lookupTemplateAliases pre-resolves the unique template_ids
// referenced by the given chunks into their aliases. Persisted on the
// template node as `alias` meta — recompute is cheap (O(unique templates))
// and rendered alongside the hex in chunk lines.
func lookupTemplateAliases(st *logState, chunks []*knowledgev1.Node) map[string]string {
	out := make(map[string]string)
	for _, c := range chunks {
		tid := kgtypes.Value(c, "template_id")
		if tid == "" {
			continue
		}
		if _, ok := out[tid]; ok {
			continue
		}
		n, ok := st.NodeByID(tid)
		if !ok {
			out[tid] = ""
			continue
		}
		out[tid] = kgtypes.Value(n, "alias")
	}
	return out
}

// streamAlias returns the readable alias for a stream node — preferring
// the persisted `alias` metadata key. Returning the empty string is fine
// — callers fall back to bare hash rendering.
func streamAlias(stream *knowledgev1.Node) string {
	return kgtypes.Value(stream, "alias")
}

// writeStreamLabels prints the inline label set from the stream's
// metadata. Shared-label graph nodes are covered separately via
// writeSharedLabelSection.
func writeStreamLabels(sb *strings.Builder, stream *knowledgev1.Node) {
	if len(stream.Metadata) == 0 {
		return
	}
	var labels []string
	for k, v := range stream.Metadata {
		key, ok := strings.CutPrefix(k, "label:")
		if !ok {
			continue
		}
		labels = append(labels, key+"="+v)
	}
	if len(labels) == 0 {
		return
	}
	slices.Sort(labels)
	fmt.Fprintf(sb, "Labels: %s\n\n", strings.Join(labels, ", "))
}

// writeSharedLabelSection renders each shared label node plus any
// cloud-graph proxies the label points at (via EMITTED_BY edges). Proxy
// formatting defers to resolveProxyIfNeeded so the displayed cloud
// resource is annotated with its graph, account, and symbol name
// whenever resolution succeeds.
func writeSharedLabelSection(
	ctx context.Context, sb *strings.Builder, st *logState, labels []*knowledgev1.Node,
) {
	if len(labels) == 0 {
		sb.WriteString("### Shared labels: (none)\n\n")
		return
	}
	fmt.Fprintf(sb, "### Shared labels (%d)\n\n", len(labels))
	for _, label := range labels {
		fmt.Fprintf(sb, "- %s (ID: %s)\n", label.SymbolName, label.Id)
		writeCloudProxiesForLabel(ctx, sb, st, label)
	}
	sb.WriteString("\n")
}

// writeCloudProxiesForLabel prints one bulleted line per EMITTED_BY
// destination reachable from the label node. Empty result means the
// pipeline did not resolve this label against the cloud graph — we
// stay quiet rather than adding a noisy "no proxies" line per label.
func writeCloudProxiesForLabel(
	ctx context.Context, sb *strings.Builder, st *logState, label *knowledgev1.Node,
) {
	peerIDs := collectEdgePeerIDs(st, label.Id, kgwire.OutgoingEdges, kgtypes.EdgeEmittedBy)
	for _, pid := range peerIDs {
		peer, ok := st.NodeByID(pid)
		if !ok {
			continue
		}
		annotation := resolveProxyIfNeeded(ctx, peer)
		if annotation == "" {
			annotation = proxyAnnotation(peer)
		}
		if annotation == "" {
			annotation = "[unresolved]"
		}
		fmt.Fprintf(sb, "    → cloud: %s %s\n", peer.SymbolName, annotation)
	}
}

// writeStreamChunkSection renders up to maxChunksPerLogTraversal chunks
// that belong to the stream. This view intentionally omits decoded
// entries — the stream lens is about labels and cloud topology; the
// per-chunk content drill-down is the template traversal's job. The
// templateAliases map (templateID → alias) lets us print the readable
// template alias alongside the hex so the agent doesn't have to
// cross-reference IDs.
func writeStreamChunkSection(sb *strings.Builder, chunks []*knowledgev1.Node, templateAliases map[string]string) {
	fmt.Fprintf(sb, "### Chunks (%d total", len(chunks))
	if len(chunks) > maxChunksPerLogTraversal {
		fmt.Fprintf(sb, ", showing first %d", maxChunksPerLogTraversal)
	}
	sb.WriteString(")\n\n")
	if len(chunks) == 0 {
		sb.WriteString("(none)\n")
		return
	}
	for i, c := range chunks {
		if i >= maxChunksPerLogTraversal {
			break
		}
		fmt.Fprintf(sb, "- %s", c.Id)
		if tid := kgtypes.Value(c, "template_id"); tid != "" {
			fmt.Fprintf(sb, " (template: %s)", renderTemplateLocatorPair(tid, templateAliases[tid]))
		}
		if count := kgtypes.Value(c, "entry_count"); count != "" {
			fmt.Fprintf(sb, " — %s entries", count)
		}
		sb.WriteString("\n")
	}
}

// renderTemplateLocatorPair formats a template's "alias · short-hex"
// pair when the alias is known, falling back to bare hex otherwise.
// Keeps chunk lines concise while still surfacing the readable alias.
func renderTemplateLocatorPair(templateID, alias string) string {
	if alias == "" {
		return templateID
	}
	return fmt.Sprintf("`%s` · %s", alias, shortHex(templateID))
}
