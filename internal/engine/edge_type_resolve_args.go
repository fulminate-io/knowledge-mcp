// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// edge_type_resolve_args.go applies the resolver to the RAW tool arguments in
// the Dispatch seam, before any plan is compiled. Sitting on the raw arguments
// is what lets ONE application cover both traverse shapes — the from_id walk
// that compileTraverse lowers and the start-less graph-wide walk that
// dispatchGraphWideEdges composes from its own edge read.

// resolveArgsEdgeTypes resolves the edge-type spellings a tool call carries and
// returns the rewritten arguments, plus an optional caller-facing notice.
//
// Every tool other than traverse and mutate returns its arguments untouched and
// issues NO Stats read.
func resolveArgsEdgeTypes(ctx context.Context, stats StatsFn, tool string, args json.RawMessage) (json.RawMessage, string, error) {
	switch tool {
	case "traverse":
		return resolveTraverseArgs(ctx, stats, args)
	case "mutate":
		return resolveMutateArgs(ctx, stats, args)
	default:
		return args, "", nil
	}
}

// resolveTraverseArgs resolves the traverse tool's edge_types filter.
//
// EACH ARM RESOLVES AGAINST THE SAME TARGET ITS OWN PLAN RIDES. Here that is
// buildTarget over the caller's selector fields, which is exactly what
// compileTraverse's plan and graphWideEdgeUnion's plan both carry. Resolving
// against a different selector would read the vocabulary of a graph the walk
// never touches — a silent cross-graph read, not a style choice.
//
// A logs traverse is skipped: it is rendered by the client intercept and never
// reaches a stored edge vocabulary here. A malformed payload is skipped too —
// Compile re-parses and returns ok=false, so the deny path surfaces the parse
// error, the same disposition precheckTraverse records.
func resolveTraverseArgs(ctx context.Context, stats StatsFn, args json.RawMessage) (json.RawMessage, string, error) {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return args, "", nil //nolint:nilerr // malformed JSON is surfaced by Compile's deny, not here
	}
	if len(a.EdgeTypes) == 0 || a.Graph == "logs" {
		return args, "", nil
	}
	target := buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch)
	resolved, err := ResolveEdgeTypeFilter(ctx, stats, target, a.EdgeTypes)
	if err != nil {
		return nil, "", err
	}
	next, rerr := replaceArgField(args, "edge_types", resolved)
	return next, "", rerr
}

// resolveMutateArgs resolves a link/unlink relationship declaration.
//
// It acts ONLY on a complete by-id link/unlink, so a create, an update or a
// delete never reaches the resolver and never costs a Stats read. A link_graph
// shape is SKIPPED: that cross-graph write is default-denied by Compile and
// owned by the crossgraph composer, which resolves the declaration itself
// against the graph it actually writes into.
//
// The target is mutateTarget, matching mutationRequest exactly — including its
// EMPTY branch argument. mutateTarget is NOT buildTarget with different
// spelling: it projects the instance selector per graph family, so on a write to
// a family whose key is not `name` the two selectors genuinely disagree.
func resolveMutateArgs(ctx context.Context, stats StatsFn, args json.RawMessage) (json.RawMessage, string, error) {
	var a mutateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return args, "", nil //nolint:nilerr // malformed JSON is surfaced by Compile's deny, not here
	}
	if a.Operation != "link" && a.Operation != "unlink" {
		return args, "", nil
	}
	if a.From == "" || a.To == "" || a.Relationship == "" || a.LinkGraph != "" {
		return args, "", nil
	}
	target := mutateTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, "")
	res, err := ResolveEdgeTypeDeclaration(ctx, stats, target, []string{a.Relationship})
	if err != nil {
		return nil, "", err
	}
	next, rerr := replaceArgField(args, "relationship", res.Types[0])
	if rerr != nil {
		return nil, "", rerr
	}
	return next, unmatchedUnlinkNotice(a.Operation, res, target), nil
}

// unmatchedUnlinkNotice is the disclosure for an UNLINK naming a spelling the
// graph does not store.
//
// ORCHESTRATOR RULING (2026-09-02): unlink keeps the WRITE declaration path, so
// it stays idempotent exactly as the mutate tool documents — an unmatched
// spelling is admitted and does not error. But the standing rule that an
// unmatched spelling is reported with the vocabulary named still holds, so the
// response carries this notice instead of returning silently.
//
// "0 edges removed" is DERIVED, not guessed: the vocabulary is the complete set
// of edge types the graph stores, so a spelling absent from it can have no
// edges of that type to remove. A LINK gets no notice — declaring a new family
// is the write path working as designed, not a surprise worth narrating.
func unmatchedUnlinkNotice(operation string, res EdgeTypeResolution, target *knowledgev1.GraphSelector) string {
	if operation != "unlink" || len(res.Unmatched) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"note: relationship %q matches no edge type stored in %s, whose edge vocabulary is: %s — 0 edges removed.",
		res.Unmatched[0], describeGraphTarget(target), renderVocabulary(res.Vocabulary))
}

// withResolutionNotice appends a resolution notice to an already-rendered
// result, returning res unchanged when there is nothing to disclose.
//
// The notice is a SEPARATE content block, never concatenated into the existing
// text — the same disposition WithTruncationNoticeFor documents and for the
// same reason: the blocks are delivered as an array, so a format=json payload
// stays in its own block and remains independently parseable.
func withResolutionNotice(res kgtools.ToolResult, notice string) kgtools.ToolResult {
	if notice == "" {
		return res
	}
	res.Content = append(res.Content, kgtools.ContentBlock{Type: "text", Text: notice})
	return res
}

// replaceArgField rewrites ONE field of a raw argument payload, leaving every
// other argument byte-for-byte identical.
//
// It goes through a map[string]json.RawMessage rather than round-tripping a
// typed struct on purpose: a typed round trip silently DROPS any argument the
// struct does not declare, so a tool gaining a field would start losing it here
// with nothing to notice.
func replaceArgField(args json.RawMessage, field string, value any) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, fmt.Errorf("resolve %s: %w", field, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", field, err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	raw[field] = encoded
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", field, err)
	}
	return out, nil
}
