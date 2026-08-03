// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryRules ports the server-side
// handleRules handler client-side. Claims query(type:"rule").
//
// READ SHAPE: the fetch is a keyset page drain (engine.DrainKeysetPages at
// engine.BrowsePageSize per request), so the whole rule corpus reaches the
// client in bounded requests rather than one capped read. The order is
// filter-before-slice: drain, apply the scope substring filter, record the
// matching count as the TRUE TOTAL, then apply the caller's offset and the
// render cap to that filtered set. Slicing before filtering would let the cap
// decide which rules the filter ever sees.
//
// TWO SEPARATE BOUNDS, and satisfying one is not a license to drop the other.
// The paging bounds the SERVER READ; the render cap (engine.BrowseDefaultLimit
// when the caller supplies no positive limit) bounds what lands in the caller's
// context window. The render reports both numbers — "Rules (10 of 137):" —
// so a page can no longer pass for the corpus.
//
// Wire shape: the drain's plan carries Selection{NodeType:"rule"} plus the
// caller's status/meta selection and the tombstone flag, and decodes through
// engine.DecodeNodes. The scope filter is applied client-side via the same
// substring rule handleRules used: `strings.Contains(strings.ToLower(scope +
// description), strings.ToLower(filter))`.
//
// `graph` is REJECTED on this arm, with the contract wording shared from
// justifyRulesKnowledgeOnly (query_arm_registry.go) so the registry gate and the
// arm cannot state it differently. Rule nodes are knowledge-graph nodes; this
// path routes no graph selector at all.
//
// WITH ONE EXCEPTION, and it is a convention rather than a special case for
// rules: a graph value that names the family this arm already pins — the
// knowledgeGraphRedundantAliases set, "" and "knowledge" — is accepted as
// valid-but-redundant instead of refused. A caller sending it has restated the
// arm's own contract, not asked for a routing this path cannot perform, and
// hosts that attach a graph selector to every query they issue send exactly that
// shape. Any OTHER value still rejects with the wording above: a rule browse
// naming another graph family is a real caller error. This mirrors the server's
// singleton family selector, which accepts the labels that denote the one
// knowledge graph and refuses the rest (knowledgeRootNameAliases,
// cmd/knowledge-server/internal/tools/tools_graph_routing_selector.go).

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// InterceptQueryRules claims query(type:"rule").
func InterceptQueryRules(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Type != "rule" {
		return false, kgtools.ToolResult{}
	}

	// queryArgs (the shared client mirror at tools_logs_args.go) has
	// no Scope field — and we intentionally do NOT widen it. Mirror
	// the server-side handleRules pattern at
	// cmd/knowledge-server/tools/tools_knowledge_query.go:395-398:
	// decode a LOCAL anonymous struct for the scope filter.
	var scopeArgs struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(params.Arguments, &scopeArgs)

	// Pre-read gate. A rejected param (graph among them) terminates here, before
	// the drain issues its first request.
	if err := accountQueryParams(armRules, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("rules: graph caller unavailable")
	}

	allRules, err := drainRules(ctx, gc, a)
	if err != nil {
		return true, errorResult(err.Error())
	}

	// Local scope substring filter — port of
	// tools_knowledge_query.go:404-410. It runs over the WHOLE drained corpus,
	// which is what makes the count below a true total rather than a page's worth.
	var matched []*knowledgev1.Node
	for _, n := range allRules {
		if scopeArgs.Scope != "" && !strings.Contains(strings.ToLower(kgtypes.Value(n, "scope")+n.Description), strings.ToLower(scopeArgs.Scope)) {
			continue
		}
		matched = append(matched, n)
	}
	page := rulesPage(matched, int(a.Offset), int(a.Limit))

	// JSON callers (the agent graph-explorer) get the {graph, type, results,
	// total} browse envelope — the SAME shape the server browse returns for every
	// non-intercepted node type — reusing the rules already fetched+filtered, with
	// the page as the rows and the matching count as the total. This MUST precede
	// the markdown empty-case returns below so an empty result set serializes as
	// results:[] rather than the "No rules recorded" prose (which would break the
	// caller's JSON.parse).
	if a.Format == "json" {
		return true, engine.BrowseJSONResult("knowledge", "rule", page, len(matched), a.Fields)
	}

	// Keyed on the MATCHING set, not the page: "no rules match" and "your offset
	// is past the last match" are different answers, and only the first one is
	// this prose. The second falls through to the render, which reports a zero
	// page against a non-zero total.
	if len(matched) == 0 {
		if scopeArgs.Scope != "" {
			return true, kgtools.TextResult("No rules found matching scope: " + scopeArgs.Scope)
		}
		return true, kgtools.TextResult("No rules recorded. Use add_rule to add codebase constraints.")
	}

	return true, kgtools.TextResult(renderRulesMarkdown(page, len(matched)))
}

// drainRules reads the rule corpus in keyset pages over the Execute carrier
// seam, decoding each page through engine.DecodeNodes — the full
// knowledgev1.Node payloads, including the scope/enforcement metadata the rules
// render reads.
//
// The caller's status, meta and include_tombstones ride the SELECTION, so those
// filters are applied server-side at the fetch rather than after the fact. The
// caller's limit and offset deliberately do NOT ride it: they select a page of
// the SCOPE-FILTERED set, which only exists client-side, and pushing them to the
// fetch is the defect this shape replaces.
//
// Each page asks for engine.BrowsePageSize rows and SETS AfterId — on every
// page including the first, where the value is the empty string. Presence is
// what selects the keyset browse; an omitted field pages in the backend's
// default order, under which the cursor taken from page one skips every lower
// id. SkipTotal is on because the drain counts what it receives and never reads
// the server's Total, so no page pays for a COUNT.
func drainRules(ctx context.Context, gc GraphCaller, a queryArgs) ([]*knowledgev1.Node, error) {
	// Built inline rather than through engine's compile-local browseSelection,
	// which is unexported and takes an unexported arg type — the same reason
	// composeRecentBrowse hand-builds its Selection.
	sel := &knowledgev1.Selection{NodeType: string(kgtypes.NodeRule)}
	if a.Status != "" {
		sel.Statuses = []string{a.Status}
	}
	sel.MetadataPredicates = engine.LowerMetaPredicates(a.Meta)

	nodes, err := engine.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		plan := &knowledgev1.QueryPlan{
			Selection:         sel,
			IncludeTombstones: a.IncludeTombstones,
			Limit:             int32(engine.BrowsePageSize),
			AfterId:           &cursor,
			SkipTotal:         true,
		}
		resp, rerr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
			Target: domainTarget(a),
		})
		if rerr != nil {
			return nil, fmt.Errorf("query rules: %w", rerr)
		}
		page, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("decode rules: %w", derr)
		}
		return page, nil
	}, engine.BrowsePageSize)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// rulesPage selects the caller's page out of the scope-filtered set: skip
// offset, then cap.
//
// The cap mirrors applyBrowseLimitOffset's semantics verbatim so the client has
// ONE spelling of how a browse limit defaults — a positive caller limit is used
// as given, and an absent, zero or negative one takes engine.BrowseDefaultLimit.
// Reading a zero as "show everything" would put the whole corpus in the caller's
// context window through an explicit param.
func rulesPage(matched []*knowledgev1.Node, offset, limit int) []*knowledgev1.Node {
	page := matched
	if offset > 0 {
		if offset >= len(page) {
			return nil
		}
		page = page[offset:]
	}
	rowCap := limit
	if rowCap <= 0 {
		rowCap = engine.BrowseDefaultLimit
	}
	if len(page) > rowCap {
		page = page[:rowCap]
	}
	return page
}

// renderRulesMarkdown renders one page of rules beside the count of rules that
// MATCHED, which is the number the caller needs to know whether there is more.
//
// The header carries " of <matched>" only when the page is a strict subset. A
// page that IS the whole matching set is already a complete answer, and its
// header stays byte-identical to the port of handleRules's populated branch at
// tools_knowledge_query.go:419-431 that the render goldens capture. Scope and
// Enforcement lines emit ONLY when the corresponding node.Value returns
// non-empty — load-bearing for that golden parity.
func renderRulesMarkdown(page []*knowledgev1.Node, matched int) string {
	var sb strings.Builder
	if len(page) == matched {
		fmt.Fprintf(&sb, "Rules (%d):\n\n", matched)
	} else {
		fmt.Fprintf(&sb, "Rules (%d of %d):\n\n", len(page), matched)
	}
	for _, r := range page {
		fmt.Fprintf(&sb, "- **%s**\n", r.SymbolName)
		fmt.Fprintf(&sb, "  %s\n", r.Description)
		if scope := kgtypes.Value(r, "scope"); scope != "" {
			fmt.Fprintf(&sb, "  Scope: %s\n", scope)
		}
		if enf := kgtypes.Value(r, "enforcement"); enf != "" {
			fmt.Fprintf(&sb, "  Enforcement: %s\n", enf)
		}
		fmt.Fprintf(&sb, "  ID: %s\n\n", r.Id)
	}
	return sb.String()
}
