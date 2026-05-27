// SPDX-License-Identifier: Apache-2.0

package content

// analyzer_content_ratio.go — ContentTypeRatioAnalyzer reports the
// distribution of node types across a graph as an aggregate ratio plus
// optional per-root findings. The algorithmic body is the original
// pkg/topology analyzer verbatim — walk every node tallying by Type — only the
// data access swaps from the in-process store.IterateAll / store.Query to the
// foundation wire helpers: ONE FetchAllNodes for the whole-graph tally and ONE
// FetchEdges([EdgeContains]) for the per-root subtree walk (an in-memory BFS
// over the bulk-fetched adjacency, replacing the originals' per-node forward
// edge queries).
//
// Parameters (req.Extra):
//   - root_types: comma-separated types of container nodes that produce
//     per-root findings (e.g. "page" → one Finding per page with that
//     page's subtree distribution). Default: "page".
//   - per_root: "true" emits a Finding per root node. "false" or empty
//     emits only the graph-wide aggregate Finding. Default: "false".
//
// Graph-wide aggregate Finding (always emitted when any node exists):
//   - Algorithm  = "content-type-ratio"
//   - Severity   = SeverityInfo (this is a shape signal, not an alarm)
//   - Metrics["type:<nodetype>"] = fraction of total nodes of that type (0..1).
//   - Metrics["total_nodes"]      = N
//   - Evidence   = []  (no single node is "the evidence")

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// ContentTypeRatioAnalyzer measures graph-wide and (optionally) per-root
// node-type distributions. Zero-value usable; self-registers via init().
type ContentTypeRatioAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (ContentTypeRatioAnalyzer) Name() string { return "content-type-ratio" }

// Run walks every node in the scoped graph counting by Type, assembles the
// aggregate ratio Finding, and — if per_root is enabled — emits one
// per-root Finding with that root's subtree distribution.
func (a ContentTypeRatioAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/content-type-ratio: %w", err)
	}

	nodes, err := foundation.FetchAllNodes(ctx, req.Caller, req.Graph, req.Name)
	if err != nil {
		return nil, fmt.Errorf("topology/content-type-ratio: fetch nodes %s/%s: %w", req.Graph, req.Name, err)
	}

	counts, roots := walkContentTypes(nodes, parseRootTypes(foundation.ExtraString(req, "root_types", "page")))
	if totalNodes(counts) == 0 {
		return nil, nil
	}

	findings := []foundation.Finding{buildAggregateRatioFinding(counts)}
	if foundation.ExtraString(req, "per_root", "false") == "true" {
		findings = append(findings, buildPerRootRatioFindings(ctx, req, nodes, roots)...)
	}
	return applyTopK(findings, req.TopK), nil
}

// walkContentTypes walks the wire node slice once, accumulating per-type node
// counts for the aggregate Finding AND collecting root node IDs (those whose
// type is in rootTypes) for the optional per-root pass.
func walkContentTypes(nodes []*knowledgev1.Node, rootTypes map[kgtypes.NodeType]struct{}) (map[kgtypes.NodeType]int, []string) {
	counts := make(map[kgtypes.NodeType]int)
	var roots []string
	for _, n := range nodes {
		if n == nil {
			continue
		}
		counts[kgtypes.NodeType(n.Type)]++
		if _, ok := rootTypes[kgtypes.NodeType(n.Type)]; ok {
			roots = append(roots, n.Id)
		}
	}
	return counts, roots
}

// totalNodes sums the per-type counts. The helper exists so callers don't
// recompute the total at every decision point.
func totalNodes(counts map[kgtypes.NodeType]int) int {
	total := 0
	for _, c := range counts {
		total += c
	}
	return total
}

// buildAggregateRatioFinding turns the graph-wide counts map into the
// single aggregate Finding. Ratios are stored as Metrics["type:<name>"]
// so downstream renderers produce a stable sorted display.
func buildAggregateRatioFinding(counts map[kgtypes.NodeType]int) foundation.Finding {
	total := totalNodes(counts)
	metrics := make(map[string]float64, len(counts)+1)
	metrics["total_nodes"] = float64(total)
	for t, c := range counts {
		metrics["type:"+string(t)] = float64(c) / float64(total)
	}
	return foundation.Finding{
		Algorithm: "content-type-ratio",
		Severity:  foundation.SeverityInfo,
		Title:     fmt.Sprintf("Content-type ratio: %d nodes across %d types", total, len(counts)),
		Summary:   summarizeRatios(counts, total),
		Evidence:  nil,
		Metrics:   metrics,
		Metadata:  map[string]string{"scope": "aggregate"},
	}
}

// summarizeRatios renders a deterministic "type=ratio" list sorted by
// ratio descending then by type ascending. Used in the aggregate
// Finding's Summary so a human can see the dominant type at a glance.
func summarizeRatios(counts map[kgtypes.NodeType]int, total int) string {
	type pair struct {
		t kgtypes.NodeType
		r float64
	}
	pairs := make([]pair, 0, len(counts))
	for t, c := range counts {
		pairs = append(pairs, pair{t: t, r: float64(c) / float64(total)})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].r != pairs[j].r {
			return pairs[i].r > pairs[j].r
		}
		return pairs[i].t < pairs[j].t
	})
	var s strings.Builder
	for i, p := range pairs {
		if i > 0 {
			s.WriteString(", ")
		}
		fmt.Fprintf(&s, "%s=%.3f", p.t, p.r)
	}
	return s.String()
}

// buildPerRootRatioFindings emits one Finding per root node containing the
// node-type distribution of that root's EdgeContains subtree. Per-root output
// is opt-in (req.Extra["per_root"]=="true") because most callers want only the
// aggregate. The subtree walk reads its CONTAINS adjacency + node types out of
// the in-memory index built from one bulk FetchEdges, replacing the originals'
// per-node forward-edge + by-id queries.
func buildPerRootRatioFindings(ctx context.Context, req foundation.Request, nodes []*knowledgev1.Node, roots []string) []foundation.Finding {
	idx := buildNodeIndex(nodes)
	ids := make([]string, 0, len(idx))
	for id := range idx {
		ids = append(ids, id)
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, []kgtypes.EdgeType{kgtypes.EdgeContains})
	if err != nil {
		return nil
	}
	adj := buildContainsIndex(edges, idx)

	out := make([]foundation.Finding, 0, len(roots))
	for _, rootID := range roots {
		counts := subtreeTypeCounts(ctx, idx, adj, rootID)
		total := totalNodes(counts)
		if total == 0 {
			continue
		}
		metrics := make(map[string]float64, len(counts)+1)
		metrics["total_nodes"] = float64(total)
		for t, c := range counts {
			metrics["type:"+string(t)] = float64(c) / float64(total)
		}
		display := resolveNodeName(idx, rootID)
		out = append(out, foundation.Finding{
			Algorithm: "content-type-ratio",
			Severity:  foundation.SeverityInfo,
			Title:     fmt.Sprintf("Content-type ratio for %s", display),
			Summary:   summarizeRatios(counts, total),
			Evidence:  []string{rootID},
			Metrics:   metrics,
			Metadata:  map[string]string{"scope": "root"},
		})
	}
	return out
}

// subtreeTypeCounts walks every node reachable from rootID via EdgeContains
// (BFS, no depth cap — the root's full descendant set) and tallies occurrences
// per NodeType. The root itself is NOT counted; the analyzer describes what's
// INSIDE a container, not the container. Adjacency + node types come from the
// in-memory index (one bulk FetchEdges + one FetchAllNodes) instead of the
// originals' per-node forward-edge + by-id store queries.
func subtreeTypeCounts(ctx context.Context, idx nodeIndex, adj containsChildrenIndex, rootID string) map[kgtypes.NodeType]int {
	counts := make(map[kgtypes.NodeType]int)
	frontier := []string{rootID}
	visited := map[string]struct{}{rootID: {}}
	for len(frontier) > 0 {
		if ctx.Err() != nil {
			return counts
		}
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			for _, cid := range adj[id] {
				if _, dup := visited[cid]; dup {
					continue
				}
				visited[cid] = struct{}{}
				cn, ok := idx[cid]
				if !ok {
					continue
				}
				counts[kgtypes.NodeType(cn.Type)]++
				next = append(next, cid)
			}
		}
		frontier = next
	}
	return counts
}

// applyTopK truncates the findings slice to req.TopK entries when
// req.TopK > 0. The aggregate Finding is always at index 0, so truncation
// keeps it; per-root findings are ordered as walked by FetchAllNodes and
// truncated deterministically at the tail.
func applyTopK(findings []foundation.Finding, topK int) []foundation.Finding {
	if topK <= 0 || len(findings) <= topK {
		return findings
	}
	return findings[:topK]
}

// parseRootTypes splits a comma-separated list of node types into a set.
// Leading/trailing whitespace is trimmed from each entry; empty entries are
// dropped so "section,,page " still yields {section, page}. Shared with
// structural-motif.
func parseRootTypes(raw string) map[kgtypes.NodeType]struct{} {
	out := make(map[kgtypes.NodeType]struct{})
	for t := range strings.SplitSeq(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out[kgtypes.NodeType(t)] = struct{}{}
	}
	return out
}

func init() {
	foundation.Register(ContentTypeRatioAnalyzer{})
}

var _ foundation.Analyzer = ContentTypeRatioAnalyzer{}
