// SPDX-License-Identifier: Apache-2.0

// Package tools — resolver-trace mode.
//
// `query({ graph: "logs", name: "<id>", mode: "resolver" })` walks
// every stream in the log graph and reports its cloud-resolution
// outcome: which labels drove resolution, which cloud proxies the
// pipeline wired up via EMITTED_BY, and which streams went unresolved.
//
// Lets the agent triage a missing correlation in one call: if the
// expected stream pair both resolved to cloud proxies, the issue is
// BFS reachability or temporal overlap; if either is unresolved, the
// problem is upstream of correlation.
package tools

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// resolverRow captures one stream's resolution status. Resolved is
// the set of cloud-proxy IDs reachable via EMITTED_BY from any of the
// stream's labels.
type resolverRow struct {
	StreamID    string
	Alias       string
	Service     string
	PodName     string
	Namespace   string
	ClusterName string
	Resolved    []string // cloud proxy IDs reached via EMITTED_BY
}

// handleLogsResolverTrace iterates every stream node, resolves its
// EMITTED_BY edges, and renders a per-stream resolution table split
// into resolved vs unresolved sections.
func handleLogsResolverTrace(queryID string, st *logState) kgtools.ToolResult {
	if st == nil {
		return kgtools.ErrorResult(fmt.Sprintf(
			"logs resolver %q: no pre-fetched log state", queryID))
	}
	rows := collectResolverRows(st)
	return kgtools.TextResult(formatResolverTrace(queryID, rows))
}

// collectResolverRows walks every stream node in the pre-fetched state
// and builds a resolverRow per stream. Sorted: unresolved streams first
// (they're the actionable signal), then alphabetically by alias.
func collectResolverRows(st *logState) []resolverRow {
	rows := make([]resolverRow, 0, len(st.Streams))
	for _, s := range st.Streams {
		rows = append(rows, buildResolverRow(st, s))
	}
	sort.Slice(rows, func(i, j int) bool {
		// Unresolved (empty Resolved) sort first.
		ai := len(rows[i].Resolved) == 0
		aj := len(rows[j].Resolved) == 0
		if ai != aj {
			return ai
		}
		return rows[i].Alias < rows[j].Alias
	})
	return rows
}

// buildResolverRow extracts the discriminating labels from a stream
// node and walks each label's EMITTED_BY edges to collect the
// resolved cloud-proxy IDs.
func buildResolverRow(st *logState, stream *knowledgev1.Node) resolverRow {
	row := resolverRow{
		StreamID:    stream.Id,
		Alias:       streamAlias(stream),
		Service:     kgtypes.Value(stream, "label:service"),
		PodName:     kgtypes.Value(stream, "label:pod_name"),
		Namespace:   kgtypes.Value(stream, "label:namespace"),
		ClusterName: kgtypes.Value(stream, "label:cluster_name"),
	}
	row.Resolved = walkResolvedProxies(st, stream)
	sort.Strings(row.Resolved)
	return row
}

// walkResolvedProxies walks the stream's HAS_LABEL edges to its
// label nodes, then each label node's EMITTED_BY edges to cloud
// proxies. Returns the deduped set of proxy IDs.
func walkResolvedProxies(st *logState, stream *knowledgev1.Node) []string {
	labelNodes := collectChildNodesOfType(st, stream.Id,
		kgwire.OutgoingEdges, kgtypes.EdgeHasLabel, kgtypes.NodeLogLabel)
	seen := make(map[string]struct{})
	var out []string
	for _, label := range labelNodes {
		for proxy := range emittedByPeers(st, label.Id) {
			if _, ok := seen[proxy]; ok {
				continue
			}
			seen[proxy] = struct{}{}
			out = append(out, proxy)
		}
	}
	return out
}

// emittedByPeers yields the EMITTED_BY peer IDs for one label node.
// Materialized as a map for the dedup loop above.
func emittedByPeers(st *logState, labelID string) map[string]struct{} {
	peers := make(map[string]struct{})
	edges := st.EdgesOf(labelID, kgwire.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeEmittedBy})
	for i := range edges {
		peers[edges[i].ToId] = struct{}{}
	}
	return peers
}

// formatResolverTrace renders the trace as two markdown tables:
// resolved (one row per stream + the cloud proxies reached) and
// unresolved (one row per stream + its labels for triage).
func formatResolverTrace(queryID string, rows []resolverRow) string {
	resolved, unresolved := splitResolverRows(rows)
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Resolver trace — %s\n\n", queryID)
	fmt.Fprintf(&sb, "**%d stream(s)** · %d resolved · %d unresolved\n\n",
		len(rows), len(resolved), len(unresolved))

	writeUnresolvedSection(&sb, unresolved)
	writeResolvedSection(&sb, resolved)
	return sb.String()
}

// splitResolverRows partitions rows into (resolved, unresolved). Keeps
// the input order within each partition.
func splitResolverRows(rows []resolverRow) (resolved, unresolved []resolverRow) {
	for _, r := range rows {
		if len(r.Resolved) == 0 {
			unresolved = append(unresolved, r)
			continue
		}
		resolved = append(resolved, r)
	}
	return resolved, unresolved
}

// writeUnresolvedSection lists streams that didn't resolve to any
// cloud proxy. The table includes the labels that COULD have driven
// resolution so the agent can spot which labels were missing.
func writeUnresolvedSection(sb *strings.Builder, rows []resolverRow) {
	if len(rows) == 0 {
		sb.WriteString("### Unresolved: (none) — every stream wired up to at least one cloud proxy.\n\n")
		return
	}
	fmt.Fprintf(sb, "### Unresolved (%d)\n\n", len(rows))
	sb.WriteString("Streams with no EMITTED_BY edge — labels present but the resolver found no matching cloud node.\n\n")
	sb.WriteString("| stream | service | pod | namespace | cluster |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(sb, "| `%s` | %s | %s | %s | %s |\n",
			resolverAliasOrID(r),
			serviceOrDash(r.Service),
			serviceOrDash(r.PodName),
			serviceOrDash(r.Namespace),
			serviceOrDash(r.ClusterName))
	}
	sb.WriteString("\n")
}

// writeResolvedSection lists streams that resolved to ≥1 cloud proxy.
// Capped to keep the table readable; the unresolved section is the
// triage path so it isn't capped.
func writeResolvedSection(sb *strings.Builder, rows []resolverRow) {
	if len(rows) == 0 {
		return
	}
	const cap = 50
	shown := min(len(rows), cap)
	fmt.Fprintf(sb, "### Resolved (%d", len(rows))
	if len(rows) > cap {
		fmt.Fprintf(sb, ", showing first %d", cap)
	}
	sb.WriteString(")\n\n")
	sb.WriteString("| stream | service | pod | resolved cloud proxies |\n")
	sb.WriteString("|---|---|---|---|\n")
	for i := range shown {
		r := rows[i]
		fmt.Fprintf(sb, "| `%s` | %s | %s | %s |\n",
			resolverAliasOrID(r),
			serviceOrDash(r.Service),
			serviceOrDash(r.PodName),
			joinResolvedProxies(r.Resolved))
	}
	if len(rows) > cap {
		fmt.Fprintf(sb, "\n…and %d more resolved streams.\n", len(rows)-cap)
	}
}

// resolverAliasOrID returns the stream's alias if set, else its
// short hex ID.
func resolverAliasOrID(r resolverRow) string {
	if r.Alias != "" {
		return r.Alias
	}
	return shortHex(r.StreamID)
}

// joinResolvedProxies renders the cloud-proxy IDs as a single comma-
// separated string. Multiple proxies are common (a stream's labels
// may resolve to a Service AND a Namespace AND a Pod simultaneously).
func joinResolvedProxies(ids []string) string {
	if len(ids) == 0 {
		return "—"
	}
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		cleaned = append(cleaned, "`"+id+"`")
	}
	return strings.Join(cleaned, "<br>")
}
