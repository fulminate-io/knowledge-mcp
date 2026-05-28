// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// linkerExecutor is the narrow Execute seam (T-GTB3 Phase 6). The linker's
// read helpers compile each browse to a declarative QueryPlan and decode the
// RAW ExecuteResponse carriers (nodes_json / graph_names_json) instead of the
// formatted JSON tool wire. They keep their GraphCaller param + type-assert it
// to linkerExecutor — the production graphClientCaller implements both Call and
// Execute, so the assertion succeeds (mirrors render.Executor). This avoids
// widening the narrow Call-only GraphCaller interface. A GraphCaller that is
// NOT an Executor returns a typed error so the missing seam is loud.
type linkerExecutor interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// asExecutor upgrades a GraphCaller to a linkerExecutor or returns a typed error.
func asExecutor(gc GraphCaller) (linkerExecutor, error) {
	ex, ok := gc.(linkerExecutor)
	if !ok {
		return nil, fmt.Errorf("linker requires an Execute-capable graph client")
	}
	return ex, nil
}

// browseNodesViaEngine compiles a type-browse query through the Execute carrier
// seam and decodes the typed Nodes carrier into []*knowledgev1.Node. Shared by the
// code/cloud type-browse helpers (queryCodeFiles / queryCodePackages /
// queryCloudResources). args is the query tool's JSON arg shape (graph / repo /
// name / type / limit); the engine lowers it via compileQuery (the T-GTB1e
// relaxed code guard lets a type-browse through — only code id/text is denied).
func browseNodesViaEngine(ctx context.Context, gc GraphCaller, args map[string]any) ([]*knowledgev1.Node, error) {
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal browse query: %w", err)
	}
	req, ok := engine.Compile("query", body)
	if !ok {
		return nil, fmt.Errorf("linker: browse query args not reducible to an ExecuteRequest")
	}
	resp, err := ex.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return engine.DecodeNodes(resp)
}

// fetchGraphNames enumerates the indexed graph names of a given type via the
// Execute carrier seam: a query(mode:modules) compiled to RETURN_MODE_GRAPH_NAMES
// whose carrier (engine.DecodeGraphNames) carries []*knowledgev1.GraphInfo;
// we project GraphInfo.Name → []string. On a missing-seam / decode failure we
// return the error so the caller surfaces it (the old []string best-effort nil
// path masked the OBJECT-shape mismatch).
func fetchGraphNames(ctx context.Context, gc GraphCaller, graphType string) ([]string, error) {
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	args, err := json.Marshal(map[string]any{
		"graph": graphType,
		"mode":  "modules",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal list graphs args: %w", err)
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		return nil, fmt.Errorf("linker: list-graphs query args not reducible to an ExecuteRequest")
	}
	resp, err := ex.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("query graphs (%s): %w", graphType, err)
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil, fmt.Errorf("query graphs (%s): %w", graphType, err)
	}
	names := make([]string, 0, len(infos))
	for _, gi := range infos {
		names = append(names, gi.Name)
	}
	return names, nil
}

// emitLink composes a single cross-graph link into the LINKAGE graph via the
// single-owner crossgraph.ResolveAndLink — the SAME proxy-materialization impl
// tools' handleClientCrossGraphLink uses. Used by every sub-linker to write a
// derived edge. It materializes the foreign FROM/TO proxies INTO linkage and
// writes the from→to LINK into linkage carrying the edge metadata, ENTIRELY over
// the client Execute seam — so the linker link NEVER reaches the server's legacy
// handleCrossGraphLink → ResolveOrProxy / proxyScanGraphTypes.
//
// confidence/method/evidence ride onto the linkage EdgeSpec; LastValidated is
// stamped per-run (RFC3339). When DryRun is true the call short-circuits with
// success — the caller still counts the link as emitted.
func emitLink(ctx context.Context, gc GraphCaller, opts LinkOptions, from, to, relationship, method, evidence string, confidence float64) error {
	if opts.DryRun {
		return nil
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return fmt.Errorf("emitLink %s -[%s]-> %s: %w", from, relationship, to, err)
	}
	_, _, lerr := crossgraph.ResolveAndLink(ctx, gc, ex, crossgraph.LinkRequest{
		From:          from,
		To:            to,
		Relationship:  relationship,
		TargetGraph:   "linkage",
		Confidence:    confidence,
		Method:        method,
		Evidence:      evidence,
		LastValidated: time.Now().UTC().Format(time.RFC3339),
	})
	if lerr != nil {
		return fmt.Errorf("emitLink %s -[%s]-> %s: %w", from, relationship, to, lerr)
	}
	return nil
}

// resultText extracts the textual body of a ToolResult by concatenating
// every Content entry. Mirrors the helper sub-linker tests use to inspect
// fake-graphCaller results.
func resultText(res kgtools.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}
