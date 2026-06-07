// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// deleteArgs is the compile-local view of the standalone `delete` tool's wire
// shape — the by-ids form ({ids:[...]}) AND the prune-by-age form
// ({older_than, type, session_id, dry_run}). It mirrors the server-side
// handlePruneHistory args (tools_prune.go:26) plus the by-id ids[] carrier.
type deleteArgs struct {
	IDs       []string `json:"ids"`
	OlderThan string   `json:"older_than"`
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	DryRun    bool     `json:"dry_run"`
	Graph     string   `json:"graph"`
	Language  string   `json:"language"`
}

// compileDelete lowers BOTH delete shapes onto a MUTATION_KIND_DELETE plan:
//
//   - by-ids ({ids:[...]}) → Selection.Ids (the literal write target set).
//   - prune-by-age ({older_than, type, session_id} with NO ids) →
//     Selection{NodeType: pruneTypeAlias(type), FieldPredicates:[{created_at,
//     OP_LT, Now-older_than RFC3339}]} (+ a session_id MetadataPredicate when
//     set). The MUTATION_KIND_DELETE + created_at OP_LT write path is proven +
//     tested server-side (decodeDelete → selectionToQ → applyFieldPredicates;
//     created_at in fieldPredicateAllowlist; engine_mutate_apply.go db.Delete
//     WithHard() matches legacy handlePruneHistory).
//
// dry_run:true returns (nil,false): a dry-run is NEVER lowered to a DELETE
// MutationPlan. The dispatcher claims the dry-run BEFORE Compile
// (dispatchDeletePreview) and renders a read-only "would delete" preview for
// BOTH the by-ids and prune-by-age shapes — so a dry-run cannot reach the engine
// as a delete. A dry_run that somehow reached this compiler would deny rather
// than delete (the ok=false fall-through), which is the safe failure direction.
// compileDelete is the entry point for BOTH the standalone `delete` tool
// (compile.go switch) AND the id-less mutate(operation:delete) fall-through
// (compileMutateByIDDelete).
func compileDelete(args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	var a deleteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, false
	}
	// Safety: a dry-run must never compile to a real DELETE. The preview seam
	// (dispatchDeletePreview) claims dry_run upstream; if one slips through here
	// (e.g. a non-Dispatch caller), deny rather than delete.
	if a.DryRun {
		return nil, false
	}
	if len(a.IDs) > 0 {
		// By-ids: the literal write target set. Mirrors compileMutateByIDDelete's
		// by-id branch (compile_mutate.go).
		plan := &knowledgev1.MutationPlan{
			Kind:      knowledgev1.MutationPlan_MUTATION_KIND_DELETE,
			Selection: &knowledgev1.Selection{Ids: a.IDs},
		}
		return deleteRequest(plan, a.Graph, a.Language), true
	}

	sel, ok := pruneSelection(a)
	if !ok {
		return nil, false // unknown prune type / unparseable duration → legacy surfaces the error.
	}
	plan := &knowledgev1.MutationPlan{
		Kind:      knowledgev1.MutationPlan_MUTATION_KIND_DELETE,
		Selection: sel,
	}
	return deleteRequest(plan, a.Graph, a.Language), true
}

// pruneSelection builds the prune-by-age Selection (NodeType=alias + a created_at
// OP_LT cutoff FieldPredicate, plus an optional session_id == metadata guard).
// Shared by the real-delete compile path (compileDelete) AND the dry-run preview
// (dispatchDeletePreview) so both resolve the IDENTICAL node set. Returns
// ok=false on an unknown prune type or an unparseable older_than duration.
func pruneSelection(a deleteArgs) (*knowledgev1.Selection, bool) {
	nodeType, ok := pruneTypeAliases[a.Type]
	if !ok {
		return nil, false // unknown prune type → legacy surfaces the error.
	}
	dur, err := ParsePruneDuration(a.OlderThan)
	if err != nil {
		return nil, false // unparseable duration → legacy surfaces the error.
	}
	cutoff := time.Now().Add(-dur).Format(time.RFC3339)

	sel := &knowledgev1.Selection{
		NodeType: string(nodeType),
		FieldPredicates: []*knowledgev1.FieldPredicate{
			{Field: "created_at", Op: knowledgev1.MetadataPredicate_OP_LT, Value: cutoff},
		},
	}
	if a.SessionID != "" {
		// Mirror handlePruneHistory's session_id metadata == SessionID guard
		// (tools_prune.go:51) as an exact-match metadata predicate.
		sel.MetadataPredicates = []*knowledgev1.MetadataPredicate{
			{Key: "session_id", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: a.SessionID},
		}
	}
	return sel, true
}

// deleteRequest wraps a DELETE MutationPlan in an ExecuteRequest with the target
// graph selector (the delete tool, like prune, targets the knowledge graph by
// default — an empty graph is the engine's knowledge default).
func deleteRequest(plan *knowledgev1.MutationPlan, graph, language string) *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: buildTarget(graph, "", "", "", language, ""),
	}
}

// pruneTypeAliases maps the user-facing prune `type` onto the concrete
// kgtypes.NodeType. The client cannot import the cmd/knowledge-server package
// (compile.go import-boundary), so it carries its own copy of this mapping.
// Currently empty: no node type is retention-eligible, so pruneSelection
// returns ok=false for every type and the prune-by-age path falls through to
// the legacy handler.
var pruneTypeAliases = map[string]kgtypes.NodeType{}

// ParsePruneDuration parses durations like "24h", "7d", "30m". The client cannot
// import cmd/knowledge-server, so it carries its own copy of this parser. Exported
// so the manage(prune) intercept reuses the same relative-window grammar.
func ParsePruneDuration(s string) (time.Duration, error) {
	if before, ok := strings.CutSuffix(s, "d"); ok {
		s = before
		var days int
		if _, err := fmt.Sscanf(s, "%d", &days); err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
