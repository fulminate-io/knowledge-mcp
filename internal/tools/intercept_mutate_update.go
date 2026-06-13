// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// handleClientMutateUpdateTyped claims a mutate(update) on a type-bearing
// knowledge node (criterion / finding / rule / research / etc.) and routes its
// create-time first-class params (command/criterion_type/scope/enforcement/
// evidence/source) the generic UPDATE arm would otherwise drop on the floor,
// re-derives the auto-summary when the caller passed none, and re-stamps a
// criterion's name from its description. Unroutable params for the (operation,
// type) pair are rejected loudly (byte-identical node after reject). Returns
// (false, _) when it does not claim the update (the caller routes it through
// the cloud-aware engine dispatch).
//
// CLEAN-FORWARD seam: executeMutate re-compiles the forwarded args through
// engine.Compile → compileMutateByIDUpdate → updateSetFields, which routes
// name/description/summary/content/status/keywords/source as top-level
// set_fields. So the per-type params MUST land in the metadata map ONLY and be
// STRIPPED from the top-level forward — an unstripped top-level `source` would
// double-route into the node Source FIELD, diverging from create (which keeps
// finding source in metadata). The handler builds the forward explicitly.
func handleClientMutateUpdateTyped(
	ctx context.Context,
	deps ClientDeps,
	a mutateArgs,
	node *knowledgev1.Node,
) (bool, kgtools.ToolResult) {
	if node == nil {
		return false, kgtools.ToolResult{}
	}
	nodeType := kgtypes.NodeType(node.GetType())

	// Claim gate: the typed router owns an update only when it has per-type work —
	// the node is a routing/derive type (criterion/rule/finding), OR a first-class
	// per-type param is present (so an unroutable one is rejected loudly). Every
	// other typed update (e.g. a ticket name change) falls through to the generic
	// engine dispatch byte-unchanged.
	if !routesPerTypeUpdate(nodeType) && !hasFirstClassUpdateParam(a) {
		return false, kgtools.ToolResult{}
	}

	// (b) Targeted loud rejection of params unroutable for this (operation,type).
	// Asserted before any routing so a rejected update leaves the node
	// byte-identical (no forward issued).
	if rejectErr := rejectUnroutableUpdateParams(nodeType, a); rejectErr != nil {
		return true, errorResult(rejectErr.Error())
	}

	// Per-type params → metadata ONLY (mirrors the create handlers). The merged
	// map starts from the caller's metadata (universal passthrough) and gains the
	// per-type keys; the server merges it per-key via set_metadata.
	meta := mergeUpdateMetadata(a, nodeType)

	// (c) Re-derive the auto-summary ONLY when the caller passed none AND a
	// derive-source field changed. Caller-supplied summary always wins.
	summary := a.Summary
	if summary == "" {
		summary = rederiveUpdateSummary(nodeType, a, node)
	}

	// (d) Criterion-only: re-stamp name=description when description changes
	// (Name==Description convention from upsertCriterionNode).
	name := a.Name
	if nodeType == kgtypes.NodeCriterion && a.Description != "" {
		name = a.Description
	}

	forward := forwardedTypedUpdatePayload{
		Operation:   "update",
		ID:          a.ID,
		Name:        name,
		Description: a.Description,
		Summary:     summary,
		Content:     a.Content,
		Status:      a.Status,
		Keywords:    a.Keywords,
		Metadata:    meta,
		Graph:       a.Graph,
		Language:    a.Language,
		Format:      a.Format,
	}
	// FINDING source lands in metadata (create parity) — NOT the node field. For
	// non-finding types, source routes to the node field via the Phase-1
	// top-level passthrough (no strip).
	if nodeType != kgtypes.NodeFinding {
		forward.Source = a.Source
	}

	args, err := json.Marshal(forward)
	if err != nil {
		return true, errorResult("mutate(update): marshal forward: " + err.Error())
	}
	gc := deps.GraphCaller()
	if _, uerr := executeMutate(ctx, gc, args); uerr != nil {
		return true, errorResult("mutate(update): " + uerr.Error())
	}
	return true, textResult(fmt.Sprintf("mutate(update): updated %s [graph: knowledge/default]", a.ID))
}

// routesPerTypeUpdate reports whether nodeType has per-type param routing and a
// derived summary the typed update router owns (criterion/rule/finding). Other
// types fall through to the generic engine dispatch.
func routesPerTypeUpdate(nodeType kgtypes.NodeType) bool {
	switch nodeType {
	case kgtypes.NodeCriterion, kgtypes.NodeRule, kgtypes.NodeFinding:
		return true
	}
	return false
}

// hasFirstClassUpdateParam reports whether a carries any per-type first-class
// param that must be routed-or-rejected by the typed router. `source` is
// excluded: it routes top-level via the Phase-1 widening for every non-finding
// type, so its presence alone does not force a typed claim.
func hasFirstClassUpdateParam(a mutateArgs) bool {
	return a.Command != "" || a.CriterionType != "" || a.Scope != "" ||
		a.Enforcement != "" || a.Evidence != ""
}

// forwardedTypedUpdatePayload is the CLEAN forward wire shape: universal
// passthrough scalars at top level (routed by updateSetFields) plus the merged
// metadata map (routed by set_metadata). Per-type params (command/criterion_type/
// scope/enforcement/evidence, and finding source) are NEVER emitted here — they
// ride inside Metadata only.
type forwardedTypedUpdatePayload struct {
	Operation   string            `json:"operation"`
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Content     string            `json:"content,omitempty"`
	Status      string            `json:"status,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	Source      string            `json:"source,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Graph       string            `json:"graph,omitempty"`
	Language    string            `json:"language,omitempty"`
	Format      string            `json:"format,omitempty"`
}

// mergeUpdateMetadata builds the forward metadata map: the caller's metadata
// (copied — never mutated in place) plus the per-type first-class params for
// this node type, mirroring the create handlers' SetValue calls. Per-type keys:
//   - criterion → type (default "manual"), command
//   - rule      → scope, enforcement
//   - finding   → evidence, source (finding-specific: create puts source in
//     metadata and hardcodes the node Source field to "llm:claude")
func mergeUpdateMetadata(a mutateArgs, nodeType kgtypes.NodeType) map[string]string {
	meta := map[string]string{}
	maps.Copy(meta, a.Metadata)
	switch nodeType {
	case kgtypes.NodeCriterion:
		if a.CriterionType != "" {
			meta["type"] = a.CriterionType
		}
		if a.Command != "" {
			meta["command"] = a.Command
		}
	case kgtypes.NodeRule:
		if a.Scope != "" {
			meta["scope"] = a.Scope
		}
		if a.Enforcement != "" {
			meta["enforcement"] = a.Enforcement
		}
	case kgtypes.NodeFinding:
		if a.Evidence != "" {
			meta["evidence"] = a.Evidence
		}
		if a.Source != "" {
			meta["source"] = a.Source
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// rederiveUpdateSummary re-derives the auto-summary for a typed update using the
// EFFECTIVE post-update fields. Supplied args win; unsupplied fields are read
// from the looked-up node by SOURCE — metadata fields (command/criterion_type/
// scope/evidence) via kgtypes.Value, node FIELDS (name → SymbolName, description)
// via proto getters. Returns "" for types with no derived summary (the caller
// then leaves summary unchanged).
func rederiveUpdateSummary(nodeType kgtypes.NodeType, a mutateArgs, node *knowledgev1.Node) string {
	switch nodeType {
	case kgtypes.NodeCriterion:
		cType := effective(a.CriterionType, kgtypes.Value(node, "type"))
		if cType == "" {
			cType = "manual"
		}
		desc := effective(a.Description, node.GetDescription())
		command := effective(a.Command, kgtypes.Value(node, "command"))
		return projects.DeriveCriterionSummary(cType, desc, command)
	case kgtypes.NodeRule:
		name := effective(a.Name, node.GetSymbolName())
		scope := effective(a.Scope, kgtypes.Value(node, "scope"))
		return projects.DeriveRuleSummary(name, scope)
	case kgtypes.NodeFinding:
		desc := effective(a.Description, node.GetDescription())
		evidence := effective(a.Evidence, kgtypes.Value(node, "evidence"))
		return projects.DeriveFindingSummary(desc, evidence)
	}
	return ""
}

// effective returns supplied when non-empty, else the existing value off the
// node. Encodes the "caller-supplied field wins; otherwise read the current
// post-update-equivalent value off the looked-up node" rule.
func effective(supplied, existing string) string {
	if supplied != "" {
		return supplied
	}
	return existing
}

// rejectUnroutableUpdateParams returns a structured CodeInvalidArgument-style
// error when a carries a first-class per-type param that is unroutable for an
// update of nodeType (e.g. a finding update carrying scope, or a rule update
// carrying command), naming the offending param. The caller returns BEFORE any
// write so a rejected update leaves the node byte-identical.
//
// This is a TARGETED allowlist over the six per-type params only — NOT a blanket
// unknown-field reject — because the universal scalars (name/description/summary/
// content/status/keywords/metadata/format/graph/language/id/operation) and
// `source` route for every claimed type (source: finding→metadata, others→node
// field via the Phase-1 widening), and the metadata/edge-meta/batch fields share
// this wire struct. The per-type allowed set:
//   - criterion → command, criterion_type
//   - rule      → scope, enforcement
//   - finding   → evidence
//
// `source` is intentionally absent from the reject set (routable everywhere).
func rejectUnroutableUpdateParams(nodeType kgtypes.NodeType, a mutateArgs) error {
	type perType struct {
		value   string
		param   string
		ownedBy kgtypes.NodeType
	}
	checks := []perType{
		{a.Command, "command", kgtypes.NodeCriterion},
		{a.CriterionType, "criterion_type", kgtypes.NodeCriterion},
		{a.Scope, "scope", kgtypes.NodeRule},
		{a.Enforcement, "enforcement", kgtypes.NodeRule},
		{a.Evidence, "evidence", kgtypes.NodeFinding},
	}
	for _, c := range checks {
		if c.value != "" && c.ownedBy != nodeType {
			return fmt.Errorf("mutate(update, type=%s): %s is not a %s field", nodeType, c.param, nodeType)
		}
	}
	return nil
}
