// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// serverless_depth_bfs.go implements the per-function forward BFS used by
// ServerlessDepthAnalyzer.Run plus the helpers that turn a completed walk
// into a Finding. Split out from serverless_depth.go to keep both files
// under the line cap. The walk reads adjacency from the in-memory edgeIndex
// and labels discovered dependencies from the resource-type map, rather than
// per-node store queries.

// serverlessDepthBFS runs a forward BFS from a serverless function node
// following dependency edges. Returns a Finding describing the depth, count
// of unique dependencies, and the distinct resource types encountered.
func serverlessDepthBFS(
	idx *edgeIndex,
	resourceTypeByID map[string]string,
	fn *knowledgev1.Node,
	maxDepth int,
) foundation.Finding {
	visited := map[string]int{fn.Id: 0} // nodeID → depth
	frontier := []string{fn.Id}
	depTypes := map[string]bool{}

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		frontier = expandServerlessLayer(idx, resourceTypeByID, frontier, depth, visited, depTypes)
	}

	return buildServerlessFinding(fn, visited, depTypes)
}

// expandServerlessLayer walks one BFS layer: for every node in the frontier,
// reads outgoing dependency edges from the index and registers
// newly-discovered nodes plus their resource types.
func expandServerlessLayer(
	idx *edgeIndex,
	resourceTypeByID map[string]string,
	frontier []string,
	depth int,
	visited map[string]int,
	depTypes map[string]bool,
) []string {
	var next []string
	for _, nodeID := range frontier {
		for _, dep := range idx.outgoing(nodeID, serverlessDependencyEdges) {
			if dep == "" || dep == nodeID {
				continue
			}
			if _, seen := visited[dep]; seen {
				continue
			}
			visited[dep] = depth
			if rt := resourceTypeByID[dep]; rt != "" {
				depTypes[rt] = true
			}
			next = append(next, dep)
		}
	}
	return next
}

// buildServerlessFinding assembles a Finding from the BFS results.
func buildServerlessFinding(
	fn *knowledgev1.Node,
	visited map[string]int,
	depTypes map[string]bool,
) foundation.Finding {
	maxDepth := 0
	depCount := 0
	for id, d := range visited {
		if id == fn.Id {
			continue
		}
		depCount++
		if d > maxDepth {
			maxDepth = d
		}
	}

	severity := classifyServerlessSeverity(maxDepth)
	name := displayName(fn)
	resType := metaValue(fn, "resource_type")

	typeList := sortedMapKeys(depTypes)
	title := fmt.Sprintf("Serverless dependency depth: %s (depth %d, %d deps)", name, maxDepth, depCount)
	summary := fmt.Sprintf(
		"%s %s has a dependency tree %d levels deep with %d unique dependencies. Types: %s",
		resType, name, maxDepth, depCount, strings.Join(typeList, ", "),
	)

	return foundation.Finding{
		Algorithm: "serverless_depth",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  []string{fn.Id},
		Metrics: map[string]float64{
			"dependency_depth": float64(maxDepth),
			"dependency_count": float64(depCount),
		},
		Metadata: map[string]string{
			"dependency_types": strings.Join(typeList, ","),
		},
	}
}

// sortedMapKeys returns sorted keys from a bool map.
func sortedMapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
