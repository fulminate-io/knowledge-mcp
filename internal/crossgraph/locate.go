// SPDX-License-Identifier: Apache-2.0

// Package crossgraph is the SINGLE owner of the client-side cross-graph
// proxy-materialization composer. Both package tools'
// handleClientCrossGraphLink AND package linker's emitLink call
// crossgraph.ResolveAndLink — there is exactly ONE proxy-materialization
// implementation across the whole client tree (the single-owner invariant).
//
// It imports only render (FetchNodeIn), engine (Compile/Decode), pkg/store
// (BuildCrossGraphProxy + types), graphclient (to stamp its own query origin on
// the reads it issues, as every other RPC-issuing client package does), and
// gen — NEVER cmd/knowledge/internal/tools
// or cmd/knowledge/internal/linker (which would cycle). It defines its own narrow
// Call-only GraphCaller, mirroring the anti-cycle pattern render/tools/linker use.
package crossgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// GraphCaller is the narrow Execute wire surface, mirroring tools.GraphCaller /
// linker.GraphCaller / render.GraphCaller without an import cycle on any of them.
// The composer reads/links over the Execute carrier.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// ForeignGraph is a located (graph_type, graph_name) pair — a graph outside the
// knowledge graph the composer can probe for a foreign endpoint. Exported because
// callers that resolve a single endpoint (charge evidence, the slug-less proxy
// migration) enumerate the foreign-graph list themselves and reuse it.
type ForeignGraph struct {
	GraphType string
	GraphName string
}

// foreignScanGraphTypes is the client mirror of the server's proxyScanGraphTypes
// precedence (cmd/knowledge-server/internal/codegraph/routing.go): code, then
// practice, then cloud, then cicd. listForeignGraphs orders by this precedence so
// the client's first-hit foreign graph matches the server's.
var foreignScanGraphTypes = []string{
	string(kgtypes.GraphCode),
	string(kgtypes.GraphPractice),
	string(kgtypes.GraphCloud),
	string(kgtypes.GraphCICD),
}

// ListForeignGraphs enumerates every loaded foreign (code/practice/cloud/cicd)
// graph as a (GraphType, GraphName) pair via FOUR per-type RETURN_MODE_GRAPH_NAMES
// reads (one per foreignScanGraphTypes entry, in precedence order). It does its
// OWN per-type graph-name enumeration via the engine primitives — crossgraph must
// NOT import package tools, so this is the third thin engine.DecodeGraphNames
// wrapper (alongside linker.fetchGraphNames + tools.fetchGraphNamesOfType), kept
// per-package by Go boundaries. Empty-name entries are dropped.
func ListForeignGraphs(ctx context.Context, ex render.Executor) ([]ForeignGraph, error) {
	if ex == nil {
		return nil, errors.New("crossgraph locate: Execute seam unavailable")
	}
	var out []ForeignGraph
	for _, gt := range foreignScanGraphTypes {
		infos, err := fetchGraphNamesOfType(ctx, ex, gt)
		if err != nil {
			return nil, fmt.Errorf("crossgraph locate: %w", err)
		}
		for _, gi := range infos {
			if gi.GetName() == "" {
				continue
			}
			out = append(out, ForeignGraph{GraphType: gt, GraphName: gi.GetName()})
		}
	}
	return out, nil
}

// ListForeignGraphsOfType enumerates every loaded foreign graph of the SINGLE
// graphType as a (GraphType, GraphName) pair via ONE RETURN_MODE_GRAPH_NAMES read.
// It is the per-type body of ListForeignGraphs lifted out of the four-type loop:
// callers that only ever resolve against one family (the think born-link path and
// the code-link backfill, both code-only) pay one enumeration read instead of the
// four ListForeignGraphs issues. Empty-name entries are dropped.
func ListForeignGraphsOfType(ctx context.Context, ex render.Executor, graphType string) ([]ForeignGraph, error) {
	if ex == nil {
		return nil, errors.New("crossgraph locate: Execute seam unavailable")
	}
	infos, err := fetchGraphNamesOfType(ctx, ex, graphType)
	if err != nil {
		return nil, fmt.Errorf("crossgraph locate: %w", err)
	}
	var out []ForeignGraph
	for _, gi := range infos {
		if gi.GetName() == "" {
			continue
		}
		out = append(out, ForeignGraph{GraphType: graphType, GraphName: gi.GetName()})
	}
	return out, nil
}

// fetchGraphNamesOfType enumerates the loaded graphs of graphType via the generic
// Execute seam (query(mode:modules) → RETURN_MODE_GRAPH_NAMES). It reads the typed
// graph_names carrier off the response directly (the proto *knowledgev1.GraphInfo
// list); ListForeignGraphs only needs the Name, so there is no need to decode into
// the store catalog struct.
func fetchGraphNamesOfType(ctx context.Context, ex render.Executor, graphType string) ([]*knowledgev1.GraphInfo, error) {
	args, err := json.Marshal(map[string]any{"graph": graphType, "mode": "modules"})
	if err != nil {
		return nil, fmt.Errorf("marshal list-graphs args (%s): %w", graphType, err)
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		return nil, fmt.Errorf("list-graphs query not reducible (%s)", graphType)
	}
	// Attribution only, NOT load-bearing for the working-set gate: a mode:modules
	// query compiles to a type-only target, which the admission recorder's
	// instance-key half refuses regardless of the operation stamped here. The
	// stamp is what stops this enumeration's load from being reported against
	// whichever user call happened to trigger the composer.
	resp, err := ex.Execute(graphclient.WithOperation(ctx, graphclient.OpCrossGraphProbe), req)
	if err != nil {
		return nil, fmt.Errorf("query graphs (%s): %w", graphType, err)
	}
	return resp.GetGraphNames(), nil
}

// LocateForeignNode probes the supplied (pre-fetched) graph list in order for a
// node with the given id, returning the first (graphType, graphName, node) whose
// node is non-nil along with found=true. On no hit it returns ("", "", nil,
// false). Composed from render.FetchNodeIn; the graph list is supplied by the
// caller so a two-endpoint resolve enumerates the foreign graph list ONCE and
// reuses it across FROM and TO. Bounded — at most one FetchNodeIn per loaded
// foreign graph, short-circuiting on the first hit.
func LocateForeignNode(ctx context.Context, gc GraphCaller, graphs []ForeignGraph, id string) (kgtypes.GraphType, string, *knowledgev1.Node, bool) {
	if gc == nil || id == "" {
		return "", "", nil, false
	}
	// The probe scan is attributed to the probe, never to the tool call that
	// happened to trigger it. This is load-bearing: each FetchNodeIn below
	// addresses a concrete graph instance the user never named, so inheriting an
	// admitting caller stamp would pull every scanned foreign graph into this
	// process's working set on the strength of one unrelated interaction. Hoisted
	// out of the loop — the value is loop-invariant, so no probe pays for it.
	ctx = graphclient.WithOperation(ctx, graphclient.OpCrossGraphProbe)
	for _, fg := range graphs {
		n, err := render.FetchNodeIn(ctx, gc, id, fg.GraphType, fg.GraphName)
		if err != nil {
			// A transport/decode error on one graph is not fatal — keep scanning.
			// The trade-off is deliberate: propagating would fail the callers that
			// tolerate a missing proxy today (born-linking drops an unresolvable
			// referent rather than failing the write), so the scan still degrades
			// to not-found. But a SILENT skip once hid a whole broken family — a
			// wrongly-keyed selector made every cloud/cicd probe error and the
			// location feature no-op'd with no operator-visible signal — so the
			// failure is logged loudly instead of dropped on the floor.
			slog.Warn("crossgraph locate: foreign-graph probe failed",
				"graph_type", fg.GraphType,
				"graph_name", fg.GraphName,
				"node_id", id,
				"error", err)
			continue
		}
		if n != nil {
			return kgtypes.GraphType(fg.GraphType), fg.GraphName, n, true
		}
	}
	return "", "", nil, false
}
