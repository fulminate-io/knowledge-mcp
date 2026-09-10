// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// collect_crossgraph.go — the COLLECTOR-SIDE RESOLUTION PASS: the edges a
// registered custom collect emitted that name another graph are resolved and
// linked into the LINKAGE graph, instead of being written into the collect's own
// graph.
//
// WHICH EDGES THOSE ARE. An edge names another graph through EITHER of two
// fields: target_graph, when its ToID is the foreign endpoint, or source_graph,
// when its FromID is. One resolution reaches one foreign family, so an edge
// naming both is refused; see [validateCrossGraphEdge] and [foreignEndpointOf].
//
// WHERE IT SITS AND WHY. It runs inside the custom-collect runner, BETWEEN the
// provider call and the sink write. Both ends of that span are load-bearing.
// Before the sink write, because a cross-graph edge that reached the chunk
// write would already be an edge in the collector's own graph and no later pass
// could take it back — the tail after the runner sees a CollectComposition,
// which retains counts and not edges. After the provider call, because the
// edges only exist once the provider has answered.
//
// IT REFUSES BEFORE IT RESOLVES, and that ordering is the whole reason this pass
// exists rather than a direct call to the composer. crossgraph.ResolveAndLink is
// deliberately BEST-EFFORT on the linkage path, for the compiled-in linker whose
// edges legitimately name non-nodes: it writes into a target graph nobody
// validated, it links an endpoint it could not find by that endpoint's RAW id,
// and a failed enumeration returns a nil error. Every one of those is silence
// where a collector author needs a message, so the three conditions are
// answered HERE, ahead of it, and the composer's contract is left untouched for
// its two other callers.

// resolveCrossGraphEdges resolves every edge that named a graph family on either
// endpoint and links it into the linkage graph. It returns a non-nil error the
// moment any edge cannot be resolved, and the caller fails the whole collect on
// it.
//
// FAILING THE COLLECT IS THE POINT, not a severity choice. A provider that named
// a foreign graph asserted a relationship to a node in it; writing the rest of
// the graph and dropping that assertion would leave a collect that reported
// success while silently losing the edges the author cared most about. Bad input
// errors, and a graph that is not there is bad input.
//
// IT RUNS IN TWO PHASES, AND THE SPLIT IS THE REQUIREMENT RATHER THAN A TIDINESS
// CHOICE. Phase one validates, enumerates and locates EVERY edge; phase two
// links them. A single walk that resolved and linked each edge in turn would
// commit the earlier edges' proxies and links before it reached one it must
// refuse — and since a refusal fails the whole collect, the collect's own graph
// is never written, so linkage would be left holding an edge FROM a node id that
// exists in no graph. Every refusal now precedes every write, so a failing
// collect leaves the graph as it found it.
//
// WHAT THE SPLIT DOES NOT BUY, said plainly: it is an ordering, not a
// transaction. Phase two issues one upsert and one link per edge over the wire,
// and a failure partway through leaves the earlier ones committed. There is no
// transaction across the linkage graph to have, and the condition is a wire
// failure rather than a refusal, so it surfaces as an error naming the edge that
// failed.
//
// THE ENUMERATION IS PER NAMED FAMILY, NOT PER EDGE. Ten edges naming one family
// pay one RETURN_MODE_GRAPH_NAMES read between them, which is what
// ListForeignGraphsOfType exists for; the located list is carried into phase two
// and handed to the composer so it does not enumerate a second time.
func resolveCrossGraphEdges(ctx context.Context, deps ClientDeps, collectID string, edges []externalcollector.CrossGraphEdge) error {
	if len(edges) == 0 {
		return nil // an ordinary collect pays nothing for this: no caller, no read.
	}
	// The collect's cross-graph fan-out is attributed to the post-collect term
	// for the same reason the linker's is: it addresses graphs the user never
	// named in this call, so inheriting the collect's own stamp would report
	// every scanned foreign graph as part of the collect's working set.
	ctx = graphclient.WithOperation(ctx, graphclient.OpPostCollectFanout)

	gc := deps.GraphCaller()
	if gc == nil {
		return fmt.Errorf(
			"collect %s: %d cross-graph edge(s) name another graph and there is no graph client to resolve them against",
			collectID, len(edges))
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		return fmt.Errorf("collect %s: cross-graph edges: %w", collectID, err)
	}
	statsFn, serr := statsFnOf(gc)
	if serr != nil {
		return fmt.Errorf("collect %s: cross-graph edges: %w", collectID, serr)
	}

	// PHASE ONE — every refusal, over every edge, before any write.
	planned, perr := planCrossGraphEdges(ctx, gc, ex, collectID, edges)
	if perr != nil {
		return perr
	}

	// PHASE TWO — the writes, in envelope order.
	for _, p := range planned {
		if lerr := linkCrossGraphEdge(ctx, gc, ex, statsFn, collectID, p); lerr != nil {
			return lerr
		}
	}
	return nil
}

// plannedCrossGraphEdge is one edge that passed every refusal, carried with the
// graph list its family enumerated to. The list travels with the edge so phase
// two neither re-enumerates nor re-derives which family it belonged to.
type plannedCrossGraphEdge struct {
	edge   *externalcollector.CrossGraphEdge
	graphs []crossgraph.ForeignGraph
}

// planCrossGraphEdges runs every refusal over every edge and returns the ones
// that passed, each with its family's graph list. It writes nothing.
//
// THE REFUSAL ORDER PER EDGE is enumerate, then graph-exists, then
// endpoint-found. The enumeration comes first because the other two both depend
// on it; "not found in a graph that does not exist" is the less useful of the
// two answers, so the absent-family arm reports first.
func planCrossGraphEdges(
	ctx context.Context,
	gc GraphCaller,
	ex render.Executor,
	collectID string,
	edges []externalcollector.CrossGraphEdge,
) ([]plannedCrossGraphEdge, error) {
	// Enumerated families, keyed by family name. A family that enumerated to an
	// EMPTY list is cached too — the second edge naming a missing graph must not
	// re-read to be told the same thing.
	enumerated := make(map[string][]crossgraph.ForeignGraph, 1)
	planned := make([]plannedCrossGraphEdge, 0, len(edges))

	for i := range edges {
		e := &edges[i]
		if verr := validateCrossGraphEdge(collectID, e); verr != nil {
			return nil, verr
		}
		family, endpoint := foreignEndpointOf(e)
		graphs, seen := enumerated[family]
		if !seen {
			var err error
			graphs, err = crossgraph.ListForeignGraphsOfType(ctx, ex, family)
			if err != nil {
				return nil, fmt.Errorf("%s: graph family %q could not be enumerated, so whether it holds the endpoint is unknown: %w",
					describeCrossGraphEdge(collectID, e), family, err)
			}
			enumerated[family] = graphs
		}
		if len(graphs) == 0 {
			return nil, fmt.Errorf("%s: no graph of family %q is loaded, so the edge names a graph that does not exist",
				describeCrossGraphEdge(collectID, e), family)
		}
		if _, _, _, found := crossgraph.LocateForeignNode(ctx, gc, graphs, endpoint); !found {
			return nil, fmt.Errorf("%s: endpoint %q was not found in any %q graph (%d searched)",
				describeCrossGraphEdge(collectID, e), endpoint, family, len(graphs))
		}
		planned = append(planned, plannedCrossGraphEdge{edge: e, graphs: graphs})
	}
	return planned, nil
}

// linkCrossGraphEdge materializes one planned edge's proxy and writes its link
// into the linkage graph, through the shared composer.
func linkCrossGraphEdge(
	ctx context.Context,
	gc GraphCaller,
	ex render.Executor,
	statsFn engine.StatsFn,
	collectID string,
	p plannedCrossGraphEdge,
) error {
	e := p.edge
	handled, res, lerr := crossgraph.ResolveAndLink(ctx, gc, ex, crossgraph.LinkRequest{
		From:         e.FromID,
		To:           e.ToID,
		Relationship: e.Type,
		TargetGraph:  "linkage",
		Weight:       e.Weight,
		Confidence:   e.Confidence,
		Method:       e.Method,
		Evidence:     e.Evidence,
		Stats:        statsFn,
		// The list phase one already enumerated and already located the endpoint
		// in. Supplying it is what lets the composer resolve against a family its
		// own fixed scan list does not carry, and it is why the composer's silent
		// enumeration-failure arm is unreachable from here.
		ScanGraphs: p.graphs,
	})
	if lerr != nil {
		return fmt.Errorf("%s: %w", describeCrossGraphEdge(collectID, e), lerr)
	}
	if !handled {
		// UNREACHABLE TODAY, AND KEPT ANYWAY — stated rather than left for a
		// reader to work out, because no test reds when this branch is removed.
		// The composer declines only on an unresolvable endpoint with best-effort
		// OFF, and best-effort is off for exactly one target graph, "knowledge";
		// this caller always names "linkage", and the composer's other decline
		// arm, a failed enumeration, cannot fire because ScanGraphs is supplied.
		// So the branch is dead by construction from here, and it would come
		// alive the moment either of those two facts changed. A decline means the
		// composer fell through to a legacy path this caller does not have, which
		// would otherwise be a silent success with no edge written.
		return fmt.Errorf("%s: the cross-graph composer declined to handle the edge: %s",
			describeCrossGraphEdge(collectID, e), resultText(res))
	}
	return nil
}

// foreignEndpointOf reports the graph family this edge's FOREIGN endpoint lives
// in, and that endpoint's id.
//
// THERE IS ONE RESOLUTION PATH, NOT TWO, and this derivation is what keeps it
// that way: the enumeration cache, the three refusals and the ScanGraphs handoff
// below apply identically whichever field named the family, so nothing forks on
// the direction. TargetGraph names the family ToID lives in; SourceGraph names
// the family FromID lives in.
//
// EXACTLY ONE OF THE TWO IS SET WHEN THIS IS CALLED. An edge naming NEITHER
// never reaches this pass — Result.CrossGraphEdges lifts only the edges that
// name one — and an edge naming BOTH is refused by validateCrossGraphEdge, which
// runs first for each edge. The order below is therefore a reading of a settled
// value rather than a precedence between two live ones.
func foreignEndpointOf(e *externalcollector.CrossGraphEdge) (family, endpoint string) {
	if e.SourceGraph != "" {
		return e.SourceGraph, e.FromID
	}
	return e.TargetGraph, e.ToID
}

// validateCrossGraphEdge refuses an edge the composer would accept and render
// meaningless. An empty ToID makes LocateForeignNode return not-found for a
// reason that has nothing to do with the graph, and on the linkage path the
// composer's best-effort arm would then link the empty string; an empty FromID
// links FROM nothing; an empty Type reaches the edge-type declaration path and
// would declare the empty family.
//
// AN EDGE NAMING BOTH FAMILIES IS REFUSED FOR A STRUCTURAL REASON, not for
// tidiness. One resolution enumerates ONE family and hands the composer ONE
// ScanGraphs list, which crossgraph.LinkRequest uses for both endpoint lookups.
// An edge foreign at BOTH ends would have one end resolved against the other
// end's family and the remaining endpoint linked by its raw id with no proxy and
// no error — a silent half-resolution, which is exactly what this pass exists to
// refuse loudly.
func validateCrossGraphEdge(collectID string, e *externalcollector.CrossGraphEdge) error {
	switch {
	case e.SourceGraph != "" && e.TargetGraph != "":
		return fmt.Errorf(
			"%s: the edge names both source_graph %q and target_graph %q, and one resolution reaches one foreign family",
			describeCrossGraphEdge(collectID, e), e.SourceGraph, e.TargetGraph)
	case e.FromID == "":
		return fmt.Errorf("%s: the edge has an empty from_id", describeCrossGraphEdge(collectID, e))
	case e.ToID == "":
		return fmt.Errorf("%s: the edge has an empty to_id", describeCrossGraphEdge(collectID, e))
	case e.Type == "":
		return fmt.Errorf("%s: the edge has an empty type", describeCrossGraphEdge(collectID, e))
	}
	return nil
}

// describeCrossGraphEdge renders the edge the way every refusal on this path
// names it: the collect, the endpoints, the relationship and the graph it named.
// A refusal that named only the family would leave a collector author grepping
// their own output for which of several edges was wrong.
//
// IT NAMES THE FIELD THAT CARRIED THE FAMILY, not always target_graph: a
// source-graph edge rendered as `(target_graph "")` would tell an author their
// edge is missing a field they deliberately did not set.
func describeCrossGraphEdge(collectID string, e *externalcollector.CrossGraphEdge) string {
	field, family := "target_graph", e.TargetGraph
	if e.SourceGraph != "" {
		field, family = "source_graph", e.SourceGraph
	}
	return fmt.Sprintf("collect %s: cross-graph edge %s -[%s]-> %s (%s %q)",
		collectID, e.FromID, e.Type, e.ToID, field, family)
}
