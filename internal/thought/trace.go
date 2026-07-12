// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TraceStep is one node in a traced reasoning chain.
type TraceStep struct {
	Node       *knowledgev1.Node
	Properties ThoughtProperties // only populated for thought nodes
	Depth      int
	EdgeType   kgtypes.EdgeType
	Direction  string // "forward" or "backward"
}

// thoughtEdgeTypes are the edge types followed during thought graph
// traversal. Verbatim from pkg/thought/query.go.
//
// EdgeProduced is followed INTENTIONALLY: a thought carrying a developer-origin
// role rides an agent--produced-->thought hub edge, so traces surface the
// originating agent node (e.g. "this thought was produced by the planner agent")
// as a provenance step. That is a feature, not noise — do NOT add an
// agent-endpoint filter to drop the hub from traces; the provenance lineage is
// the point.
var thoughtEdgeTypes = []kgtypes.EdgeType{kgtypes.EdgeNext, kgtypes.EdgeBranchesFrom, kgtypes.EdgeKGContains, kgtypes.EdgeRelatesTo, kgtypes.EdgeProduced}

// traceQueueItem is a BFS queue entry used in TraceThoughts.
type traceQueueItem struct {
	id       string
	depth    int
	edgeType kgtypes.EdgeType
	dir      string
}

// TraceThoughts follows reasoning chains from a starting thought.
// Client-side: takes a graph client; expandTraceNeighbors issues bulk
// fetchNodesByIDs + chargeMapForThoughts round-trips (one of each per
// fan-out level) instead of the original per-neighbor singleton query.
func TraceThoughts(ctx context.Context, gc Caller, startID, direction string, depth int, includeCharges, includeArtifacts bool) ([]TraceStep, error) {
	if gc == nil {
		return nil, errors.New("thought: TraceThoughts: graph client unavailable")
	}
	if depth <= 0 {
		depth = 5
	}
	if direction == "" {
		direction = "both"
	}

	edgeTypes := buildTraceEdgeTypes(includeCharges, includeArtifacts)
	dirs := traceDirections(direction)

	visited := map[string]bool{startID: true}
	queue := []traceQueueItem{{id: startID, depth: 0}}
	// One now for the whole trace: thought props on each fanned-out step are
	// recency-weighted at a single consistent instant.
	now := time.Now()
	var results []TraceStep

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return results, err
		}

		curr := queue[0]
		queue = queue[1:]
		if curr.depth >= depth {
			continue
		}

		for _, et := range edgeTypes {
			newSteps, newItems := expandTraceNeighbors(ctx, gc, curr.id, curr.depth, et, dirs, visited, now)
			results = append(results, newSteps...)
			queue = append(queue, newItems...)
		}
	}

	return results, nil
}

// buildTraceEdgeTypes assembles the edge type list for a trace based on
// inclusion flags. Verbatim from pkg/thought/query.go.
func buildTraceEdgeTypes(includeCharges, includeArtifacts bool) []kgtypes.EdgeType {
	edgeTypes := make([]kgtypes.EdgeType, len(thoughtEdgeTypes))
	copy(edgeTypes, thoughtEdgeTypes)
	if includeCharges {
		edgeTypes = append(edgeTypes, kgtypes.EdgeChargedBy)
	}
	if includeArtifacts {
		edgeTypes = append(edgeTypes, kgtypes.EdgeInformedBy, kgtypes.EdgeSupports)
	}
	return edgeTypes
}

// traceDirections converts a direction string to a slice of forward booleans.
// Verbatim from pkg/thought/query.go.
func traceDirections(direction string) []bool {
	switch direction {
	case "forward":
		return []bool{true}
	case "backward":
		return []bool{false}
	default: // "both"
		return []bool{true, false}
	}
}

// expandTraceNeighbors fetches neighbors for one (node, edgeType, directions)
// combination, returning new TraceStep values and queue items for unvisited
// neighbors. One traverse wire call per (forward,edgeType), one
// bulk hydration of unvisited neighbors, and one bulk charges fetch for the
// thought-typed subset (perf invariant).
func expandTraceNeighbors(
	ctx context.Context,
	gc Caller,
	currID string,
	currDepth int,
	et kgtypes.EdgeType,
	dirs []bool,
	visited map[string]bool,
	now time.Time,
) (steps []TraceStep, items []traceQueueItem) {
	for _, forward := range dirs {
		neighbors, err := fetchEdgeNeighborsTyped(ctx, gc, currID, et, forward)
		if err != nil {
			continue
		}
		dirLabel := "forward"
		if !forward {
			dirLabel = "backward"
		}

		// Collect the unvisited subset before hydrating.
		unvisited := neighbors[:0:0]
		for _, nid := range neighbors {
			if visited[nid] {
				continue
			}
			visited[nid] = true
			unvisited = append(unvisited, nid)
		}
		if len(unvisited) == 0 {
			continue
		}

		// One bulk hydration of all unvisited neighbors.
		nodeMap := fetchNodesByIDs(ctx, gc, unvisited)

		// Collect thought-typed IDs so we can do ONE charges fetch for the lot.
		var thoughtIDs []string
		for _, nid := range unvisited {
			n, ok := nodeMap[nid]
			if !ok {
				continue
			}
			if kgtypes.NodeType(n.Type) == kgtypes.NodeThought {
				thoughtIDs = append(thoughtIDs, nid)
			}
		}
		var chargeMap map[string][]*knowledgev1.Node
		if len(thoughtIDs) > 0 {
			chargeMap = chargeMapForThoughts(ctx, gc, thoughtIDs)
		}

		for _, nid := range unvisited {
			node, ok := nodeMap[nid]
			if !ok {
				continue
			}
			step := TraceStep{
				Node:      node,
				Depth:     currDepth + 1,
				EdgeType:  et,
				Direction: dirLabel,
			}
			if kgtypes.NodeType(node.Type) == kgtypes.NodeThought {
				step.Properties = computePropertiesFromCharges(chargeMap[nid], now)
			}
			steps = append(steps, step)
			items = append(items, traceQueueItem{id: nid, depth: currDepth + 1, edgeType: et, dir: dirLabel})
		}
	}
	return steps, items
}
