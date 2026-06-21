// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// evalEmit records a target-graph node per current row into the in-memory
// Result. The node's ID is computed via StableID so re-runs against the same
// source produce identical IDs (making Force=false idempotent). Each emit also
// stamps a translated-from edge back to the source row's node (or the
// env.SourceRef override), enabling Force=true cleanup to filter by source.
//
// On the client, EVERY run accumulates Nodes + TranslatedFrom edges into
// result.Nodes / result.Lineage and records the new ID in the in-run emitted
// set — there is no target DB write here; RunRecipe ships the Result through the
// collector Sink afterwards (skipping the write on DryRun). opts is unused here
// because DryRun is honored by RunRecipe, not by the interpreter.
func evalEmit(
	ctx context.Context,
	env *Env,
	r RuleEmit,
	sv *sourceView,
	target TargetSpec,
	sourceSlug string,
	_ Options,
	result *Result,
	emitted map[string]bool,
) error {
	targetKey := TargetKey(target)
	for i := range env.Rows {
		row := &env.Rows[i]
		fields, err := evalEmitFields(ctx, env, row, r.Fields, sv)
		if err != nil {
			return err
		}
		// Skip rows with no identity signal. A recipe that resolves
		// `name` to empty on some rows (Go101 produces content-only
		// sections without headings) should drop them, not emit a
		// hex-ID-named placeholder by falling back to row.NodeID.
		// Recipes that explicitly want row-NodeID identity can set
		// the `identity` field — that still goes through.
		if fields["name"] == "" && fields["identity"] == "" {
			result.Stats.SkippedChunks++
			continue
		}
		identity := emitIdentity(row, fields)
		nodeID := StableID(targetKey, sourceSlug, r.NodeType, identity)
		node := assembleEmittedNode(nodeID, r.NodeType, fields, sourceSlug)

		anchor := env.SourceRef
		if anchor == "" {
			anchor = row.NodeID
		}
		edge := TranslatedFromEdge(nodeID, anchor, sourceSlug)

		result.Nodes = append(result.Nodes, node)
		result.Lineage = append(result.Lineage, edge)
		emitted[nodeID] = true

		result.Stats.NodesEmitted++
		env.rememberEmit(r.As, row.NodeID, nodeID, row)
	}
	return nil
}

// evalLookup computes the StableID that a prior RuleEmit would have
// produced for the same (target, sourceSlug, NodeType, identity)
// tuple and binds it to env.EmitMap[r.As] when the node was emitted
// earlier in THIS run.
//
// Verify-on-lookup: the in-run emitted set is consulted for each
// computed ID. Misses (empty identity OR not yet emitted) leave the
// cross-emit binding unset — a subsequent evalLink will then silently
// skip that row thanks to the existing empty-endpoint guard. Both
// outcomes increment the matching Stats counter so operators can see at
// a glance how many lookups resolved.
//
// SAME-RUN scope: the lookup sees only nodes this run emitted, never a
// cross-run read of the target graph. This matches the server's
// effective behavior (its target DB lookups within one txn resolved the
// same-run emits) without paying a per-lookup wire RPC.
//
// This is the primitive that lets rule-2-style cross-reference emission
// avoid re-emitting targets already written by rule 1: the lookup pays
// only the hash + map read cost, not the node + lineage-edge cost of emit.
func evalLookup(
	ctx context.Context,
	env *Env,
	r RuleLookup,
	sv *sourceView,
	target TargetSpec,
	sourceSlug string,
	result *Result,
	emitted map[string]bool,
) error {
	targetKey := TargetKey(target)
	for i := range env.Rows {
		row := &env.Rows[i]
		identity, err := evalExpr(ctx, env, row, r.Identity, sv)
		if err != nil {
			return fmt.Errorf("evalLookup identity: %w", err)
		}
		if identity == "" {
			result.Stats.LookupMisses++
			continue
		}
		nodeID := StableID(targetKey, sourceSlug, r.NodeType, identity)
		if !emitted[nodeID] {
			result.Stats.LookupMisses++
			continue
		}
		result.Stats.LookupsResolved++
		env.rememberEmit(r.As, row.NodeID, nodeID, row)
	}
	return nil
}

// evalLink records a target-graph edge between From and To node IDs
// resolved from the recipe's expressions into result.Edges. The primary
// resolution path reads `$var` from the row's Vars (populated at
// emit/lookup time) — the common cross-emit case where `$pat` and `$uc`
// refer to nodes emitted in previous rules against the same source row.
func evalLink(
	ctx context.Context,
	env *Env,
	r RuleLink,
	sv *sourceView,
	result *Result,
	emitted map[string]bool,
) error {
	for i := range env.Rows {
		row := &env.Rows[i]
		from, err := resolveLinkEndpoint(ctx, env, row, r.From, sv)
		if err != nil {
			return err
		}
		to, err := resolveLinkEndpoint(ctx, env, row, r.To, sv)
		if err != nil {
			return err
		}
		if from == "" || to == "" {
			// Silent skip — either endpoint was not emitted for this
			// source row. Same degrade-not-die behavior as the rest
			// of the interpreter.
			result.Stats.LinkMisses++
			continue
		}
		// Verify both endpoints were emitted earlier in THIS run. Cross-emit
		// bindings come from a prior emit/lookup that succeeded for this row, so
		// the common case round-trips through the in-run emitted set (cheap).
		// The check catches dangling-link bugs from literal-ID endpoints and
		// from recipe ordering errors that wouldn't surface any other way.
		if !emitted[from] || !emitted[to] {
			result.Stats.LinkMisses++
			continue
		}
		result.Edges = append(result.Edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   from,
			ToID:     to,
			Type:     kgtypes.EdgeType(r.Rel),
			Method:   "recipe",
			Evidence: "",
		})
	}
	return nil
}

// evalSourceRef overrides the default translated-from target for
// subsequent emits. Evaluates the Ref expression against the first
// row (source_ref is recipe-scope, not row-scope) and stashes the
// result on env.SourceRef.
func evalSourceRef(
	ctx context.Context,
	env *Env,
	r RuleSourceRef,
	sv *sourceView,
) error {
	var row *Row
	if len(env.Rows) > 0 {
		row = &env.Rows[0]
	}
	v, err := evalExpr(ctx, env, row, r.Ref, sv)
	if err != nil {
		return err
	}
	env.SourceRef = v
	return nil
}

// evalEmitFields evaluates every field expression against the row,
// returning the resolved key-value map. Used only by evalEmit.
func evalEmitFields(
	ctx context.Context,
	env *Env,
	row *Row,
	fields map[string]Expr,
	sv *sourceView,
) (map[string]string, error) {
	out := make(map[string]string, len(fields))
	for k, e := range fields {
		v, err := evalExpr(ctx, env, row, e, sv)
		if err != nil {
			return nil, fmt.Errorf("emit field %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// emitIdentity picks the identity string that feeds StableID. Prefers
// the explicit "identity" field (recipes that want content-hash
// stability should set this), then the "name" field (most common
// case), then the row's source NodeID. Returns empty only if all
// candidates are empty, which StableID handles cleanly.
func emitIdentity(row *Row, fields map[string]string) string {
	if id := fields["identity"]; id != "" {
		return id
	}
	if name := fields["name"]; name != "" {
		return name
	}
	if row != nil {
		return row.NodeID
	}
	return ""
}

// assembleEmittedNode folds the field map into a *knowledgev1.Node. Top-level
// keys (type, name, summary, description, content, source) land on the named
// Node fields; everything else lands in Metadata. Source defaults to
// "recipe:" + sourceSlug so downstream audits can tell recipe-emitted nodes
// apart from manual authoring.
func assembleEmittedNode(
	nodeID, fallbackType string,
	fields map[string]string,
	sourceSlug string,
) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:   nodeID,
		Type: fallbackType,
	}
	metadata := map[string]string{}
	for k, v := range fields {
		switch k {
		case "type":
			n.Type = v
		case "name", "symbol_name":
			n.SymbolName = v
		case "summary":
			n.Summary = v
		case "description":
			n.Description = v
		case "content":
			n.Content = v
		case "source":
			n.Source = v
		case "status":
			n.Status = v
		case "identity":
			// Feeds StableID only; never stored.
		default:
			metadata[k] = v
		}
	}
	if n.Source == "" {
		n.Source = "recipe:" + sourceSlug
	}
	if len(metadata) > 0 {
		n.Metadata = metadata
	}
	return n
}

// resolveLinkEndpoint returns the target-graph node ID for a link's
// From or To expression. Resolution falls straight through to evalExpr
// which reads `$var` from the row's Vars (populated at emit/lookup
// time and preserved across traversal by cloneRowVars). This means
// `$pat` after `traverse references out` correctly resolves to the
// binding the emit made for the ORIGINAL source row, not for the
// post-traverse row's NodeID.
//
// The older EmitMap-keyed-by-row-NodeID shortcut was removed because
// it broke this invariant: after traverse, row.NodeID becomes the
// traversal target, so EmitMap[var][row.NodeID] returned whatever
// emit was made for the target row (if it passed the same select
// filter) — producing self-loop edges instead of the intended
// source→target links. EmitMap is still populated for audit and for
// future cross-row lookups that explicitly key by a different row
// ID, but it does NOT drive endpoint resolution.
func resolveLinkEndpoint(
	ctx context.Context,
	env *Env,
	row *Row,
	e Expr,
	sv *sourceView,
) (string, error) {
	return evalExpr(ctx, env, row, e, sv)
}

// collectEmitTypes walks a Recipe and returns the set of node types
// every RuleEmit targets. Used by RunRecipe to scope the Force=true
// cleanup so it only iterates the types the recipe could have emitted.
func collectEmitTypes(r *Recipe) []string {
	seen := map[string]struct{}{}
	var types []string
	for _, rule := range r.Rules {
		if emit, ok := rule.(RuleEmit); ok {
			if _, dup := seen[emit.NodeType]; dup {
				continue
			}
			seen[emit.NodeType] = struct{}{}
			types = append(types, emit.NodeType)
		}
	}
	return types
}
