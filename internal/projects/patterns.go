// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// ValidatePatternFields enforces the exactly-one-of-three contract shared by
// every pattern-bearing args shape (plans today, tickets too, any future
// pattern-carrying node). Exactly one of (patternIDs non-empty,
// noPatternsReason set, proposedPatterns non-empty) must be true. Empty
// strings inside patternIDs and ProposedPatternArgs entries with empty Name
// are rejected.
//
// Soft validation: each pattern ID is looked up over the wire (knowledge graph
// first, then every loaded practice graph). Any ID that does not resolve to a
// NodePattern node — missing or wrong type — is returned both as a
// human-readable warning string (warnings) and as a raw ID (unresolved) so
// callers can persist the IDs as metadata. Creation MUST still succeed
// because in v1 patterns may not be encoded yet (chicken-and-egg with T2).
//
// Exported because the package's public validate door moved here from the
// now-removed projects.CreatePlan/CreateTicket: the create_plan / create_ticket
// interceptors (package tools) call it across the package boundary.
//
// The error strings are load-bearing: callers (and their tests) assert on
// substrings like "exactly one of", "pattern_ids[%d] is empty", and
// "proposed_patterns[%d].name is empty". Keep them byte-identical.
func ValidatePatternFields(
	ctx context.Context,
	ex render.Executor,
	patternIDs []string,
	noPatternsReason string,
	proposedPatterns []ProposedPatternArgs,
) (warnings []string, unresolved []string, err error) {
	hasPatternIDs := len(patternIDs) > 0
	hasReason := noPatternsReason != ""
	hasProposed := len(proposedPatterns) > 0

	signals := 0
	if hasPatternIDs {
		signals++
	}
	if hasReason {
		signals++
	}
	if hasProposed {
		signals++
	}
	if signals != 1 {
		return nil, nil, fmt.Errorf("exactly one of pattern_ids, no_patterns_reason, or proposed_patterns must be set (got %d)", signals)
	}

	if hasPatternIDs {
		for i, id := range patternIDs {
			if strings.TrimSpace(id) == "" {
				return nil, nil, fmt.Errorf("pattern_ids[%d] is empty", i)
			}
		}
	}
	if hasProposed {
		for i, p := range proposedPatterns {
			if strings.TrimSpace(p.Name) == "" {
				return nil, nil, fmt.Errorf("proposed_patterns[%d].name is empty", i)
			}
		}
	}

	if !hasPatternIDs {
		return nil, nil, nil
	}
	warnings, unresolved = lookupBrokenPatternIDs(ctx, ex, patternIDs)
	return warnings, unresolved, nil
}

// lookupBrokenPatternIDs probes each pattern ID and returns parallel slices:
// human-readable warnings (one per broken ID) and the raw broken IDs (so
// callers can persist them as metadata without re-parsing the warning
// strings). A "broken" ID is one that either fails to resolve or resolves to
// a node whose type is not NodePattern.
//
// Pattern nodes can live in the knowledge graph (project pattern catalogs
// historically) or in any practice graph (the canonical home for both
// language-agnostic libraries like design-patterns and project catalogs like
// knowledge-architecture). The lookup tries the knowledge graph first, then
// walks every loaded practice graph until it finds the ID.
func lookupBrokenPatternIDs(ctx context.Context, ex render.Executor, ids []string) (warnings, unresolved []string) {
	for _, id := range ids {
		node, _, found := findPatternNodeAcrossGraphs(ctx, ex, id)
		if !found {
			warnings = append(warnings, fmt.Sprintf("pattern_id %q not found — will be created during T2 encoding or referenced incorrectly", id))
			unresolved = append(unresolved, id)
			continue
		}
		if kgtypes.NodeType(node.Type) != kgtypes.NodePattern {
			warnings = append(warnings, fmt.Sprintf("pattern_id %q resolves to type %q, not %q", id, node.Type, kgtypes.NodePattern))
			unresolved = append(unresolved, id)
		}
	}
	return warnings, unresolved
}

// findPatternNodeAcrossGraphs searches the knowledge graph first, then every
// loaded practice graph. Returns (node, practiceGraphName, true) on first hit
// — practiceGraphName is empty when the hit was in the knowledge graph and
// the language slug otherwise. Returns (zero, "", false) when no graph holds
// the ID. Practice graphs are scanned in ListForeignGraphs order — first hit
// wins, so an ID that is somehow present in two practice graphs resolves
// deterministically per that ordering rather than failing.
//
// When the knowledge-graph hit is itself a proxy of a practice pattern
// (Type == NodeProxy with foreign_graph=practice), the function chases the
// proxy to the underlying practice pattern and returns that node + its
// practice graph name. This makes downstream type validation see "pattern"
// instead of "proxy" for proxies that already exist in the knowledge graph.
//
// All reads ride the generic Execute wire seam — knowledge via render.FetchNode,
// practice via crossgraph.ListForeignGraphs + crossgraph.LocateForeignNode. The
// store-engine singleton is never reached.
func findPatternNodeAcrossGraphs(ctx context.Context, ex render.Executor, id string) (*knowledgev1.Node, string, bool) {
	if hit, err := render.FetchNode(ctx, ex, id); err == nil && hit != nil {
		if chased, practiceName, ok := chasePracticeProxy(ctx, ex, hit); ok {
			return chased, practiceName, true
		}
		return hit, "", true
	}
	if node, practiceName, ok := lookupInPracticeGraphs(ctx, ex, id); ok {
		return node, practiceName, true
	}
	return nil, "", false
}

// chasePracticeProxy walks a knowledge-graph proxy node to the underlying
// practice pattern it points at. Returns (node, practiceGraphName, true) when
// hit is a proxy with foreign_graph=practice AND the foreign_id resolves in
// some loaded practice graph. Returns (_, _, false) for any other input
// (non-proxy node, proxy of a different graph type, missing foreign_id, or
// foreign_id not found in any practice graph).
func chasePracticeProxy(ctx context.Context, ex render.Executor, hit *knowledgev1.Node) (*knowledgev1.Node, string, bool) {
	if kgtypes.NodeType(hit.Type) != kgtypes.NodeProxy {
		return nil, "", false
	}
	if kgtypes.Value(hit, "foreign_graph") != string(kgtypes.GraphPractice) {
		return nil, "", false
	}
	foreignID := kgtypes.Value(hit, "foreign_id")
	if foreignID == "" {
		return nil, "", false
	}
	return lookupInPracticeGraphs(ctx, ex, foreignID)
}

// lookupInPracticeGraphs scans every loaded practice graph in
// ListForeignGraphs order and returns the first node matching id along with
// the practice graph name. Practice graphs are scanned deterministically per
// that ordering rather than failing on duplicate IDs across graphs.
//
// crossgraph.ListForeignGraphs enumerates code/practice/cloud/cicd over the
// wire; we filter to GraphPractice so the lookup stays practice-scoped per the
// legacy contract, then probe via crossgraph.LocateForeignNode (which returns
// the wire *knowledgev1.Node directly post-FUL-295 retype).
func lookupInPracticeGraphs(ctx context.Context, ex render.Executor, id string) (*knowledgev1.Node, string, bool) {
	all, err := crossgraph.ListForeignGraphs(ctx, ex)
	if err != nil {
		return nil, "", false
	}
	practice := make([]crossgraph.ForeignGraph, 0, len(all))
	for _, fg := range all {
		if fg.GraphType == string(kgtypes.GraphPractice) {
			practice = append(practice, fg)
		}
	}
	_, name, node, ok := crossgraph.LocateForeignNode(ctx, ex, practice, id)
	if !ok {
		return nil, "", false
	}
	return node, name, true
}

// ResolvePatternIDsToEffectiveIDs walks each pattern ID and returns the IDs
// that should actually be used as edge targets in the knowledge graph. IDs
// that resolve to a knowledge-graph pattern are returned unchanged. IDs that
// resolve to a pattern in a practice graph are replaced with a knowledge-graph
// proxy ID (created on demand over the wire via crossgraph.UpsertForeignProxy)
// so the downstream BatchEdge wiring (ticket→pattern via EdgeUses) lands on a
// real node in the knowledge graph instead of failing silently because
// BatchEdge targets cannot span graphs.
//
// IDs that already point at an existing knowledge-graph proxy of a practice
// pattern are passed through unchanged: the proxy IS the effective edge
// target. Without this guard, findPatternNodeAcrossGraphs's proxy-chasing
// behavior would surface the underlying practice pattern and we would create
// a SECOND proxy pointing at the same practice node.
//
// Unresolved IDs (genuinely missing from every graph) are passed through
// unchanged so the soft-warning + unresolved_pattern_ids metadata path keeps
// working — those edges will fail at write time as before, which is the
// correct behavior for IDs that name nothing.
//
// This is the missing piece that pairs with lookupBrokenPatternIDs's
// cross-graph search: validation now finds practice patterns, and resolution
// now produces effective edge targets for them. crossgraph.UpsertForeignProxy
// builds the proxy via the client-relocated crossgraph.BuildCrossGraphProxy
// (byte-identical IDs to the server) and UPSERTs it over the wire — no
// store-engine access.
func ResolvePatternIDsToEffectiveIDs(ctx context.Context, ex render.Executor, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		// Precheck: if the input ID itself resolves directly in the knowledge
		// graph as a NodeProxy with foreign_graph=practice, the input IS
		// already the effective edge target. Pass through unchanged.
		if isPracticeProxyID(ctx, ex, id) {
			out = append(out, id)
			continue
		}
		node, practiceGraph, found := findPatternNodeAcrossGraphs(ctx, ex, id)
		if !found || practiceGraph == "" {
			// Unresolved or direct knowledge-graph hit — pass through.
			out = append(out, id)
			continue
		}
		proxy, err := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", kgtypes.GraphPractice, practiceGraph, node.Id, node)
		if err != nil {
			return nil, fmt.Errorf("create proxy for practice pattern %q in %q: %w", id, practiceGraph, err)
		}
		out = append(out, proxy.Id)
	}
	return out, nil
}

// ValidateLanguagePatterns runs lookup+type-check for language_patterns.
// Returns warnings (non-fatal, surfaced under ## Warnings) and unresolved
// IDs (persisted to <plan|ticket>.unresolved_language_patterns metadata).
// Empty ids in = empty results out — language_patterns is OPTIONAL and the
// "no language_patterns" case is fine.
//
// Independent of validatePatternFields: language_patterns has no
// exactly-one-of-three contract, no proposed-language-pattern variant, and
// no eager-create flow.
func ValidateLanguagePatterns(ctx context.Context, ex render.Executor, ids []string) (warnings, unresolved []string) {
	if len(ids) == 0 {
		return nil, nil
	}
	return lookupBrokenLanguagePatternIDs(ctx, ex, ids)
}

// lookupBrokenLanguagePatternIDs is the language-pattern variant of
// lookupBrokenPatternIDs. Accepts Type=NodeFinding (with metadata.dsl_pattern
// non-empty) OR Type=NodePattern. Practice-graph findings carrying dsl_pattern
// are the canonical language-pattern shape today.
//
// Warning prefix is "language_pattern_id %q ..." (note the language_ prefix)
// so the brainstorm/planner caller can disambiguate from architectural
// pattern_id warnings.
func lookupBrokenLanguagePatternIDs(ctx context.Context, ex render.Executor, ids []string) (warnings, unresolved []string) {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			warnings = append(warnings, fmt.Sprintf("language_pattern_id %q is empty", id))
			unresolved = append(unresolved, id)
			continue
		}
		node, _, found := findPatternNodeAcrossGraphs(ctx, ex, id)
		if !found {
			warnings = append(warnings, fmt.Sprintf("language_pattern_id %q not found", id))
			unresolved = append(unresolved, id)
			continue
		}
		switch kgtypes.NodeType(node.Type) {
		case kgtypes.NodePattern:
			// Pattern nodes are accepted unconditionally (forward-compat).
		case kgtypes.NodeFinding:
			if kgtypes.Value(node, "dsl_pattern") == "" {
				warnings = append(warnings, fmt.Sprintf("language_pattern_id %q resolves to type %q lacking dsl_pattern metadata", id, node.Type))
				unresolved = append(unresolved, id)
			}
		default:
			warnings = append(warnings, fmt.Sprintf("language_pattern_id %q resolves to type %q lacking dsl_pattern metadata", id, node.Type))
			unresolved = append(unresolved, id)
		}
	}
	return warnings, unresolved
}

// isPracticeProxyID reports whether id resolves IMMEDIATELY in the knowledge
// graph to a NodeProxy with foreign_graph=practice. It does NOT chase the
// proxy — the immediate type is the signal we need to short-circuit
// double-proxying in ResolvePatternIDsToEffectiveIDs. The immediate by-id read
// rides render.FetchNode (knowledge/default graph), not the store engine.
func isPracticeProxyID(ctx context.Context, ex render.Executor, id string) bool {
	hit, err := render.FetchNode(ctx, ex, id)
	if err != nil || hit == nil || hit.Id == "" {
		return false
	}
	return kgtypes.NodeType(hit.Type) == kgtypes.NodeProxy && kgtypes.Value(hit, "foreign_graph") == string(kgtypes.GraphPractice)
}
