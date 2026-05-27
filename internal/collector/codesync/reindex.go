// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// augmentWithPreciseCallGraph runs RTA-based call graph analysis on a Go
// repository and replaces tree-sitter heuristic CALLS edges with type-precise
// edges. Non-CALLS edges are preserved unchanged. If BuildGoCallGraph fails,
// a warning is logged and the original populate result is returned unmodified.
//
// The function only runs when a go.mod is present in rootDir.
func augmentWithPreciseCallGraph(ctx context.Context, pop parser.PopulateResult, rootDir string) parser.PopulateResult {
	if _, err := os.Stat(filepath.Join(rootDir, "go.mod")); err != nil {
		slog.Warn("augmentWithPreciseCallGraph: no go.mod found, skipping", "rootDir", rootDir)
		return pop
	}
	callMap, err := BuildGoCallGraph(ctx, rootDir)
	if err != nil {
		slog.Warn("augmentWithPreciseCallGraph: BuildGoCallGraph failed, keeping tree-sitter edges", "error", err)
		return pop
	}
	if len(callMap) == 0 {
		slog.Info("augmentWithPreciseCallGraph: no call edges produced, keeping tree-sitter edges")
		return pop
	}

	symbolToID := buildSymbolToIDMap(pop.Nodes)
	tsWeights := captureCallEdgeWeights(pop.Edges)
	filtered, removedCalls := dropCallEdges(pop.Edges)
	filtered, added := appendRTACallEdges(filtered, callMap, symbolToID, tsWeights)

	slog.Info("precise call graph: replaced tree-sitter CALLS edges",
		"removed", removedCalls,
		"added", added,
		"rta_callers", len(callMap),
	)
	pop.Edges = filtered
	return pop
}

// buildSymbolToIDMap builds a "pkg.symbol" → nodeID lookup from the populate
// result. Used by augmentWithPreciseCallGraph to translate the RTA call
// graph's qualified names back into node IDs.
func buildSymbolToIDMap(nodes []*knowledgev1.Node) map[string]string {
	symbolToID := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.SymbolName == "" || n.FilePath == "" {
			continue
		}
		dir := filepath.Dir(n.FilePath)
		pkg := filepath.Base(dir)
		colon := strings.LastIndex(n.Id, ":")
		if colon < 0 {
			continue
		}
		sym := n.Id[colon+1:]
		if sym == "" {
			continue
		}
		symbolToID[pkg+"."+sym] = n.Id
	}
	return symbolToID
}

// captureCallEdgeWeights snapshots the (FromID, ToID) → Weight map from
// the existing CALLS edges so the RTA merge can re-attach tree-sitter
// call counts to pairs both layers agree on.
func captureCallEdgeWeights(edges []*knowledgev1.Edge) map[[2]string]float64 {
	tsWeights := make(map[[2]string]float64)
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		tsWeights[[2]string{e.FromId, e.ToId}] = e.Weight
	}
	return tsWeights
}

// dropCallEdges returns a new slice containing every non-CALLS edge from
// the input plus the count of CALLS edges that were dropped. The edges are
// *knowledgev1.Edge pointers, so retained edges are appended by pointer —
// no copylocks-safe field-by-field rebuild needed.
func dropCallEdges(edges []*knowledgev1.Edge) ([]*knowledgev1.Edge, int) {
	filtered := make([]*knowledgev1.Edge, 0, len(edges))
	var removed int
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
			removed++
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered, removed
}

// appendRTACallEdges walks the RTA call map and appends a CALLS edge for
// every (caller, callee) pair where both endpoints resolve to a node ID,
// re-attaching the tree-sitter Weight when the same pair was seen by
// both layers and defaulting to Weight=1 for RTA-only pairs.
func appendRTACallEdges(
	dst []*knowledgev1.Edge,
	callMap map[string][]string,
	symbolToID map[string]string,
	tsWeights map[[2]string]float64,
) ([]*knowledgev1.Edge, int) {
	var added int
	seen := make(map[[2]string]bool)
	for callerKey, callees := range callMap {
		callerID, ok := symbolToID[callerKey]
		if !ok {
			continue
		}
		for _, calleeKey := range callees {
			calleeID, ok := symbolToID[calleeKey]
			if !ok {
				continue
			}
			pair := [2]string{callerID, calleeID}
			if seen[pair] {
				continue
			}
			seen[pair] = true
			weight := tsWeights[pair]
			if weight == 0 {
				weight = 1
			}
			dst = append(dst, &knowledgev1.Edge{
				FromId: callerID,
				ToId:   calleeID,
				Type:   string(kgtypes.EdgeCalls),
				Weight: weight,
			})
			added++
		}
	}
	return dst, added
}
