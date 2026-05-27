// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_resource.go is the single PARAMETRIC renderer family covering both the
// cloud and cicd graphs. The server formatCloud{Node,SearchResults,Browse}
// (cmd/knowledge-server/tools/tools_query_cloud.go) and
// formatCICD{Node,SearchResults,Browse} (tools_query_cicd.go) were byte-for-byte
// identical modulo the graph label ("Cloud" vs "CI/CD") and the secondary
// metadata key ("region" vs "provider"). Per decision 1ad493da this collapses
// the two server families into one client family parameterized on those two
// inputs — strictly less code than two ports.
//
// ResourceKind carries the per-graph parameters. secondaryKey is the metadata
// key read for the secondary value (region / provider); secondaryLabel is the
// capitalized label used on the node detail line ("Region:" / "Provider:"); the
// search/browse paths only emit the raw value in parens so they don't need the
// label. nodeHeader is the node-detail header noun ("Cloud Resource" / "CI/CD
// Resource"); listHeader is the search/browse header noun ("Cloud" / "CI/CD").
type ResourceKind struct {
	nodeHeader     string
	listHeader     string
	secondaryKey   string
	secondaryLabel string
}

// ResourceKindCloud / ResourceKindCICD are the two pre-built kinds. Callers
// select by graph.
var (
	ResourceKindCloud = ResourceKind{
		nodeHeader:     "Cloud Resource",
		listHeader:     "Cloud",
		secondaryKey:   "region",
		secondaryLabel: "Region",
	}
	ResourceKindCICD = ResourceKind{
		nodeHeader:     "CI/CD Resource",
		listHeader:     "CI/CD",
		secondaryKey:   "provider",
		secondaryLabel: "Provider",
	}
)

// RenderResourceNode renders a single cloud/cicd resource node — port of
// formatCloudNode / formatCICDNode. The trailing metadata loop skips the two
// already-shown keys (resource_type + the kind's secondary key).
func RenderResourceNode(kind ResourceKind, account string, n *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s [%s]\n\n", kind.nodeHeader, account)
	fmt.Fprintf(&sb, "**%s**\n", n.SymbolName)
	if rt := kgtypes.Value(n, "resource_type"); rt != "" {
		fmt.Fprintf(&sb, "Type: %s\n", rt)
	}
	if secondary := kgtypes.Value(n, kind.secondaryKey); secondary != "" {
		fmt.Fprintf(&sb, "%s: %s\n", kind.secondaryLabel, secondary)
	}
	fmt.Fprintf(&sb, "ID: %s\n", n.Id)
	if n.Summary != "" {
		fmt.Fprintf(&sb, "\n**Summary:** %s\n", n.Summary)
	}
	if n.Keywords != "" {
		fmt.Fprintf(&sb, "**Keywords:** %s\n", n.Keywords)
	}
	// Show additional metadata (skip resource_type and the secondary key
	// already shown above).
	for k, v := range n.Metadata {
		if k != "resource_type" && k != kind.secondaryKey {
			fmt.Fprintf(&sb, "%s: %s\n", k, v)
		}
	}
	return kgtools.TextResult(sb.String())
}

// RenderResourceSearch renders cloud/cicd search results — port of
// formatCloudSearchResults / formatCICDSearchResults.
func RenderResourceSearch(kind ResourceKind, account, query string, results []SearchResult) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s [%s] — %d results for %q\n\n", kind.listHeader, account, len(results), query)
	for i, r := range results {
		n := r.Node
		rt := kgtypes.Value(n, "resource_type")
		secondary := kgtypes.Value(n, kind.secondaryKey)
		fmt.Fprintf(&sb, "### %d. %s", i+1, n.SymbolName)
		if rt != "" {
			fmt.Fprintf(&sb, " [%s]", rt)
		}
		if secondary != "" {
			fmt.Fprintf(&sb, " (%s)", secondary)
		}
		fmt.Fprintf(&sb, "\n%.2f — %s\n\n", r.Score, n.Id)
	}
	return kgtools.TextResult(sb.String())
}

// RenderResourceBrowse renders a browse listing of cloud/cicd resource nodes —
// port of formatCloudBrowse / formatCICDBrowse.
func RenderResourceBrowse(kind ResourceKind, account string, nodes []*knowledgev1.Node, offset int, resourceType string) kgtools.ToolResult {
	var sb strings.Builder
	header := fmt.Sprintf("## %s [%s] — %d resources", kind.listHeader, account, len(nodes))
	if resourceType != "" {
		header += fmt.Sprintf(" (type: %s*)", resourceType)
	}
	if offset > 0 {
		header += fmt.Sprintf(" (offset %d)", offset)
	}
	sb.WriteString(header + "\n\n")
	for i, n := range nodes {
		rt := kgtypes.Value(n, "resource_type")
		secondary := kgtypes.Value(n, kind.secondaryKey)
		fmt.Fprintf(&sb, "%d. **%s**", offset+i+1, n.SymbolName)
		if rt != "" {
			fmt.Fprintf(&sb, " [%s]", rt)
		}
		if secondary != "" {
			fmt.Fprintf(&sb, " (%s)", secondary)
		}
		fmt.Fprintf(&sb, "\n   ID: %s\n\n", n.Id)
	}
	return kgtools.TextResult(sb.String())
}
