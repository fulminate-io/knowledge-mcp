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
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// handleClientMutateUpdateTyped claims a mutate(update) on a type-bearing
// knowledge node (criterion / finding / rule / research / etc.) and routes its
// create-time first-class params (command/criterion_type/scope/enforcement/
// evidence/source) the generic UPDATE arm would otherwise drop on the floor,
// resolves the summary to forward through the single summary seam
// (resolveTypedUpdateSummary, intercept_mutate_update_summary.go — an explicit
// summary wins verbatim and everything else forwards nothing, leaving the stored
// summary untouched), and DERIVES a criterion's name from its
// description (its FIRST LINE — see projects.DeriveCriterionName) — a
// caller-supplied name is rejected on that type rather than silently discarded.
// ONE class of loud rejection remains, leaving the node byte-identical (no
// forward issued): params unroutable for the (operation, type) pair.
// Returns (false, _) when it does not claim the update (the caller routes it
// through the cloud-aware engine dispatch).
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
	// the node is a per-type-param routing type (criterion, which also re-stamps
	// its name) or a derive type (rule/finding), OR a first-class per-type param is
	// present (so an unroutable one is rejected loudly). Every
	// other typed update (e.g. a ticket name change) falls through to the generic
	// engine dispatch byte-unchanged.
	if !routesPerTypeUpdate(nodeType) && !hasFirstClassUpdateParam(a) {
		return false, kgtools.ToolResult{}
	}

	// Param accounting sits AFTER the claim gate, not at the call site: the call
	// site is an if-init with no statement position that runs only for CLAIMED
	// calls, so a gate placed there would also fire for the updates this router
	// declines — which are then accounted again downstream under a different
	// arm. Gating here makes "this call is the typed-update arm" true by
	// construction.
	if err := accountMutateParams(armUpdateTyped, a); err != nil {
		return true, errorResult(err.Error())
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

	// Command-shape gate, on the value the merge is about to STORE and ONLY when
	// the caller is SETTING one — either through the param or through a `command`
	// metadata key. Thousands of stored criteria already carry a selector with no
	// assertion; gating on the EFFECTIVE value instead — falling back to the
	// stored command when this call supplies none — would reject every ordinary
	// edit to all of them, including the edits that replace those very commands.
	if nodeType == kgtypes.NodeCriterion {
		_, metaCommandSupplied := a.Metadata["command"]
		if a.Command != "" || metaCommandSupplied {
			if gerr := validate.RunSelectorGuard("mutate(update, type=criterion)", "criterion.command", meta["command"]); gerr != nil {
				return true, errorResult(gerr.Error())
			}
		}
	}

	// (c) Resolve the summary to forward — see resolveTypedUpdateSummary in
	// intercept_mutate_update_summary.go, which owns the whole summary rule: an
	// explicit summary wins, and everything else forwards nothing.
	sr := resolveTypedUpdateSummary(a)

	// (d) Criterion-only: DERIVE the name from the description when the
	// description changes (the Name==Description convention upsertCriterionNode
	// establishes, CLAMPED TO THE DESCRIPTION'S FIRST LINE — see
	// projects.DeriveCriterionName, which is the single source all three
	// derivation sites share). With a supplied name rejected above, this
	// derivation can no longer discard anything.
	name := a.Name
	if nodeType == kgtypes.NodeCriterion && a.Description != "" {
		name = projects.DeriveCriterionName(a.Description)
	}

	// Status rides the forward only when the CALLER named it — read from the raw
	// payload, because a.Status cannot tell an explicit blank (clear to blank)
	// from an absent one (leave untouched).
	var status *string
	if statusExplicitlySupplied(a.raw) {
		status = &a.Status
	}

	forward := forwardedTypedUpdatePayload{
		Operation:   "update",
		ID:          a.ID,
		Name:        name,
		Description: a.Description,
		Summary:     sr.summary,
		Content:     a.Content,
		Status:      status,
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
	return true, textResult(renderTypedUpdateReceipt(a, forward, sr))
}

// routesPerTypeUpdate reports whether nodeType has per-type work the typed
// update router owns: per-type param routing into metadata for all three (rule
// scope/enforcement, finding evidence/source, criterion command/criterion_type),
// plus the criterion name re-stamp. No type has per-type SUMMARY work any more —
// every summary is author-supplied and nothing derives one. Other types fall
// through to the generic engine dispatch.
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

// forwardedTypedUpdatePayload is the CLEAN by-id update forward wire shape:
// universal passthrough scalars at top level (routed by updateSetFields) plus
// the merged metadata map (routed by set_metadata). Per-type params
// (command/criterion_type/scope/enforcement/evidence, and finding source) are
// NEVER emitted here — they ride inside Metadata only.
//
// Shared, despite the name: the per-type update router builds it, and the
// container-rollup arm reuses it for the accompanying-field write it issues
// against the named node. Any field added here reaches both.
type forwardedTypedUpdatePayload struct {
	Operation   string `json:"operation"`
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Content     string `json:"content,omitempty"`
	// Status is a POINTER so the forward can distinguish "leave it alone" (nil,
	// omitted by omitempty) from "clear it to blank" (a pointer to "", which
	// emits "status":""). A plain string collapses the two and the clear-to-blank
	// write is silently lost.
	Status   *string           `json:"status,omitempty"`
	Keywords string            `json:"keywords,omitempty"`
	Source   string            `json:"source,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Graph    string            `json:"graph,omitempty"`
	Language string            `json:"language,omitempty"`
	Format   string            `json:"format,omitempty"`
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

// rejectUnroutableUpdateParams returns a structured CodeInvalidArgument-style
// error when a carries a first-class per-type param that is unroutable for an
// update of nodeType (e.g. a finding update carrying scope, or a rule update
// carrying command), naming the offending param. The caller returns BEFORE any
// write so a rejected update leaves the node byte-identical.
//
// This is a TARGETED allowlist over the per-type params only — NOT a blanket
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
//
// The criterion `name` rule is a DIFFERENT shape from the five above and is
// checked separately. Those five reject a param that is non-empty AND owned by
// some other node type; `name` is owned by no type in that sense — it is a
// universal scalar every other claimed type routes. It is rejected on criterion
// updates alone because a criterion's name is DERIVED from its description
// (the Name==Description convention upsertCriterionNode establishes), so a
// supplied one could only be discarded. Mirrors the create arm, which already
// rejects `name` on a criterion create; letting the caller's name win on update
// would make the two paths disagree about whether a criterion may carry an
// independent name.
func rejectUnroutableUpdateParams(nodeType kgtypes.NodeType, a mutateArgs) error {
	if nodeType == kgtypes.NodeCriterion && a.Name != "" {
		return fmt.Errorf(
			"mutate(update, type=criterion): name is not applied by this path — " +
				"a criterion's name is derived from the FIRST LINE of its description; " +
				"set description instead, leading it with the line you want as the name")
	}
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
