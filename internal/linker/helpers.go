// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// linkerExecutor is the narrow Execute seam. The linker's
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
// name / type / limit); the engine lowers it via compileQuery (the
// relaxed code guard lets a type-browse through — only code id/text is denied).
//
// THIS SEAM SERVES ONE BOUNDED PAGE and refuses, before any RPC, a browse
// payload that carries no positive limit — see requirePositiveBrowseLimit. A
// caller that wants the whole matching set calls drainNodesViaEngine.
func browseNodesViaEngine(ctx context.Context, gc GraphCaller, args map[string]any) ([]*knowledgev1.Node, error) {
	if err := requirePositiveBrowseLimit(args); err != nil {
		return nil, err
	}
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

// unboundedBrowseError reports a browse payload browseNodesViaEngine refuses
// because it carries no positive limit. A non-positive or absent limit is not a
// request for everything at this seam: the client compiler rewrites it to the
// browse default (compile_query.go, applyBrowseLimitOffset), so serving it would
// hand the caller a silent default page in place of the set it asked for.
type unboundedBrowseError struct{ Found any }

func (e *unboundedBrowseError) Error() string {
	return fmt.Sprintf(
		"linker: browse payload carries no positive limit (%v): this seam serves ONE bounded page — "+
			"use drainNodesViaEngine to read the whole matching set", e.Found)
}

// positiveBrowseLimit reads a payload's limit across the numeric shapes such a
// payload can carry: a map built in Go holds an int, one that has been through a
// JSON round trip holds a float64. It reports the value and whether it is a
// usable positive bound.
func positiveBrowseLimit(v any) (int, bool) {
	var n int
	switch t := v.(type) {
	case int:
		n = t
	case int32:
		n = int(t)
	case int64:
		n = int(t)
	case float64:
		n = int(t)
	default:
		return 0, false
	}
	return n, n > 0
}

// requirePositiveBrowseLimit refuses, before any RPC, a browse payload whose
// limit is absent, unparseable or non-positive — the exact shape the compiler
// silently rewrites to the browse default. A by-id payload is EXEMPT: "ids" and
// "id" take the bulk-ids and by-id compile arms, which never reach
// applyBrowseLimitOffset, so no browse default applies to them.
//
// A refusal rather than a silent upgrade to a drain: this seam is the
// single-page primitive drainNodesViaEngine calls, and making it page on its own
// would restore "limit 0 means everything" at a client seam. A loud refusal
// converts a silent partial read into an immediate, attributable failure.
func requirePositiveBrowseLimit(args map[string]any) error {
	if _, ok := args["ids"]; ok {
		return nil
	}
	if _, ok := args["id"]; ok {
		return nil
	}
	if _, ok := positiveBrowseLimit(args["limit"]); ok {
		return nil
	}
	return &unboundedBrowseError{Found: args["limit"]}
}

// unpageablePayloadError reports a browse payload drainNodesViaEngine refuses to
// drain because it does not reach the singular type-browse arm — the only arm
// the id-keyset cursor rides. Key names the key responsible.
type unpageablePayloadError struct {
	Key    string
	Reason string
}

func (e *unpageablePayloadError) Error() string {
	return fmt.Sprintf("linker: browse payload key %q cannot be drained in keyset pages: %s", e.Key, e.Reason)
}

// precedenceKeysAboveType are the query keys the compiler dispatches on BEFORE
// the singular type browse, in its own precedence order.
var precedenceKeysAboveType = []string{"ids", "id", "text", "types"}

// requireSingularTypeBrowse refuses, before any RPC, a payload the keyset drain
// cannot page. It checks FOUR keys rather than one because the compiler
// dispatches a mode-less query in strict precedence order — ids, id, text,
// types, type, meta — and only the singular type-browse arm threads after_id. A
// payload carrying "type" PLUS any higher-precedence key takes the
// higher-precedence arm, which ignores the cursor: every page would return the
// same first page, the drain would never see a short page, and the loop would
// not terminate. Refusing loudly beats hanging.
func requireSingularTypeBrowse(args map[string]any) error {
	for _, k := range precedenceKeysAboveType {
		if _, ok := args[k]; ok {
			return &unpageablePayloadError{
				Key:    k,
				Reason: "it outranks the singular type browse, and its arm threads no keyset cursor",
			}
		}
	}
	t, ok := args["type"].(string)
	if !ok || strings.TrimSpace(t) == "" {
		return &unpageablePayloadError{
			Key:    "type",
			Reason: "a non-blank singular type is what selects the arm the keyset cursor rides",
		}
	}
	return nil
}

// drainNodesViaEngine returns EVERY node matching a type-browse payload, read as
// bounded id-keyset pages, where browseNodesViaEngine performs a single bounded
// read. It is the helper for a linker caller that intends "all".
//
// A limit of 0 is a bounded DEFAULT at this seam, never a request for
// everything: the client compiler rewrites a non-positive limit to the browse
// default, and the server clamps any limit to its own row ceiling. Both bounds
// are deliberate and unchanged — a shared system never serves an unbounded
// query, it pages — so "all" is spelled as a drain rather than as a large limit.
//
// Cross-page ordering is id-ASCENDING and stable on both backends, which is what
// makes the cursor total: each page asks for the ids strictly greater than the
// last id of the page before it, and page one passes a SET BUT EMPTY cursor
// (presence is what selects the keyset browse; an omitted cursor would page in
// the backend's own default order and skip every lower id).
//
// The guard checks four keys rather than one, for the precedence reason
// requireSingularTypeBrowse documents.
func drainNodesViaEngine(ctx context.Context, gc GraphCaller, args map[string]any) ([]*knowledgev1.Node, error) {
	if err := requireSingularTypeBrowse(args); err != nil {
		return nil, err
	}
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		page := make(map[string]any, len(args)+3)
		maps.Copy(page, args)
		// Set AFTER the copy: writing the page keys last is what stops a
		// caller's stale limit from defeating the drain.
		page["limit"] = paging.BrowsePageSize
		page["after_id"] = afterID
		page["skip_total"] = true
		return browseNodesViaEngine(ctx, gc, page)
	}, paging.BrowsePageSize)
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
	// The Stats seam is DERIVED FROM gc rather than taken as a parameter, which
	// is what lets a pass redirect it: an entry point that wrapped its gc with
	// withVocabCache gets the per-pass cached seam here for free, and this
	// signature stays unchanged. Without that wrapper this is one Stats RPC per
	// emitted edge, because every caller of emitLink sits in a per-item loop.
	stats, serr := linkerStatsFnOf(gc)
	if serr != nil {
		return fmt.Errorf("emitLink %s -[%s]-> %s: %w", from, relationship, to, serr)
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
		Stats:         stats,
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
