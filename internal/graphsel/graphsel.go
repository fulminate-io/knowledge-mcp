// SPDX-License-Identifier: Apache-2.0

// Package graphsel centralizes the per-graph-family instance-key mapping that
// the client uses to address a specific graph instance on the wire.
//
// Every graph type carries exactly one instance key: a code graph is keyed by
// repo, a cloud/CICD graph by account, a practice graph by language, and
// everything else by name. That single disposition was previously duplicated as
// a verbatim GraphCode→repo / GraphCloud,GraphCICD→account / default→name switch
// across
// segmentdist, topology/foundation, postpopulate, and pipeline. This package
// holds that switch exactly once (InstanceField) and exposes thin builders for
// the three output shapes the call sites need:
//
//   - GraphSelectorFor — a *knowledgev1.GraphSelector (struct fields).
//   - ScopePayload      — a map[string]any payload (string keys).
//   - ApplyInstanceKey  — assignment into caller-owned *string struct fields.
//
// The default-case "skip a literal name of \"default\"" guard differs by call
// site, so each builder takes an omitDefaultName flag rather than baking one
// policy in. The single switch is InstanceField; the builders only consume it.
package graphsel

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Field identifies which instance-key field a graph type addresses.
type Field int

const (
	// FieldRepo — code graphs are keyed by repo.
	FieldRepo Field = iota
	// FieldAccount — cloud / CICD graphs are keyed by account.
	FieldAccount
	// FieldName — every other graph family is keyed by name.
	FieldName
	// FieldLanguage — practice graphs are keyed by language.
	FieldLanguage
)

// InstanceField is the ONE switch over graph family → instance-key field. Every
// instance-key builder in the client delegates to this; the switch body lives
// here and nowhere else.
func InstanceField(gt kgtypes.GraphType) Field {
	switch gt {
	case kgtypes.GraphCode:
		return FieldRepo
	case kgtypes.GraphCloud, kgtypes.GraphCICD:
		return FieldAccount
	case kgtypes.GraphPractice:
		return FieldLanguage
	default:
		return FieldName
	}
}

// GraphSelectorFor builds a *knowledgev1.GraphSelector addressing (gt, name).
// When omitDefaultName is true, a name field is left empty for the empty string
// or the literal "default" (the guard the edge/selector-args call sites apply);
// repo/account are always set.
func GraphSelectorFor(gt kgtypes.GraphType, name string, omitDefaultName bool) *knowledgev1.GraphSelector {
	sel := &knowledgev1.GraphSelector{Graph: string(gt)}
	switch InstanceField(gt) {
	case FieldRepo:
		sel.Repo = name
	case FieldAccount:
		sel.Account = name
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			sel.Name = name
		}
	case FieldLanguage:
		sel.Language = name
	}
	return sel
}

// ScopePayload builds a map[string]any scope payload addressing (gt, name),
// seeded with the "graph" key. When omitDefaultName is true, a name key is left
// off for the empty string or the literal "default"; repo/account are always set.
func ScopePayload(gt kgtypes.GraphType, name string, omitDefaultName bool) map[string]any {
	payload := map[string]any{"graph": string(gt)}
	switch InstanceField(gt) {
	case FieldRepo:
		payload["repo"] = name
	case FieldAccount:
		payload["account"] = name
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			payload["name"] = name
		}
	case FieldLanguage:
		payload["language"] = name
	}
	return payload
}

// ApplyInstanceKey assigns name into exactly one of the caller-owned repo /
// account / name / language struct fields per (gt). When omitDefaultName is
// true, a name field is left untouched for the empty string or the literal
// "default".
func ApplyInstanceKey(gt kgtypes.GraphType, name string, repo, account, nameField, language *string, omitDefaultName bool) {
	switch InstanceField(gt) {
	case FieldRepo:
		*repo = name
	case FieldAccount:
		*account = name
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			*nameField = name
		}
	case FieldLanguage:
		*language = name
	}
}
