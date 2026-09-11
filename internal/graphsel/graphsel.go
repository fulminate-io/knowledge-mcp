// SPDX-License-Identifier: Apache-2.0

// Package graphsel centralizes the per-graph-family instance-key mapping that
// the client uses to address a specific graph instance on the wire.
//
// Every graph type carries at most one instance key: a code graph is keyed by
// repo, and everything else by name — except the SINGLETON families, knowledge,
// checks and practice, which carry no instance key at all and resolve to their
// one graph. Practice joined that set when the eight per-language graphs became
// one, and `language` stopped addressing a practice graph on any arm when those
// graphs were retired. That single disposition was previously duplicated as a
// verbatim GraphCode→repo / default→name switch across
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

// familyOfType maps this module's graph-type vocabulary onto the SHARED wire
// enum. It is the client's only spelling of that correspondence.
//
// The enum is the shared contract, so this map cannot disagree with the server
// about WHICH FAMILIES EXIST — only about the local type name each one is called
// here. A family RESERVED in the enum (cloud, logs, cicd, transformers) has no entry
// here and no constant to key one with: the reservation is what stops the number
// being reused, and the absence here is what stops this binary addressing it. A graph type absent from this map produces UNSPECIFIED, which a reader
// treats as "this writer predates the enum" and answers from the string field,
// so a missing entry degrades to the pre-enum behaviour rather than to a wrong
// family.
var familyOfType = map[kgtypes.GraphType]knowledgev1.GraphFamily{
	kgtypes.GraphKnowledge: knowledgev1.GraphFamily_GRAPH_FAMILY_KNOWLEDGE,
	kgtypes.GraphCode:      knowledgev1.GraphFamily_GRAPH_FAMILY_CODE,
	kgtypes.GraphPractice:  knowledgev1.GraphFamily_GRAPH_FAMILY_PRACTICE,
	kgtypes.GraphWebRaw:    knowledgev1.GraphFamily_GRAPH_FAMILY_WEB,
	kgtypes.GraphPDFRaw:    knowledgev1.GraphFamily_GRAPH_FAMILY_PDF,
	kgtypes.GraphLinkage:   knowledgev1.GraphFamily_GRAPH_FAMILY_LINKAGE,
	kgtypes.GraphChecks:    knowledgev1.GraphFamily_GRAPH_FAMILY_CHECKS,
}

// FamilyOf returns the wire enum for a graph type, or UNSPECIFIED when the type
// has no mapping. The empty graph type means knowledge by contract.
func FamilyOf(gt kgtypes.GraphType) knowledgev1.GraphFamily {
	if gt == "" {
		return knowledgev1.GraphFamily_GRAPH_FAMILY_KNOWLEDGE
	}
	return familyOfType[gt]
}

// typeOfFamily inverts familyOfType, built once from it so the two directions
// cannot drift.
var typeOfFamily = func() map[knowledgev1.GraphFamily]kgtypes.GraphType {
	out := make(map[knowledgev1.GraphFamily]kgtypes.GraphType, len(familyOfType))
	for gt, f := range familyOfType {
		out[f] = gt
	}
	return out
}()

// typeOfSelector reads the family a selector addresses, PREFERRING the typed
// enum and reading the legacy string only when the enum is UNSPECIFIED.
//
// UNSPECIFIED means the writer predates the enum, so the string is that writer's
// authoritative value — this is a version-skew read, not an error-masking
// fallback, and it ends when the string field is removed.
//
// A SET BUT UNRECOGNIZED FAMILY REPORTS ok=false RATHER THAN READING THE STRING.
// Proto carries an unknown enum value through as its number, so a reader that
// saw one has met a family it does not know; answering from the string there
// would silently address SOMETHING for a family this binary cannot honor, which
// is the defect class the enum exists to make loud.
func typeOfSelector(sel *knowledgev1.GraphSelector) (kgtypes.GraphType, bool) {
	f := sel.GetFamily()
	if f == knowledgev1.GraphFamily_GRAPH_FAMILY_UNSPECIFIED {
		return kgtypes.GraphType(sel.GetGraph()), true
	}
	gt, known := typeOfFamily[f]
	if !known {
		return "", false
	}
	return gt, true
}

// Field identifies which instance-key field a graph type addresses.
type Field int

const (
	// FieldRepo — code graphs are keyed by repo.
	FieldRepo Field = iota
	// FieldName — every other graph family is keyed by name.
	//
	// THERE IS NEITHER A FieldAccount NOR A FieldLanguage, and both absences are
	// decisions rather than omissions. The account-keyed families were cloud and
	// the CI inventory, both retired with their built-in collectors. FieldLanguage
	// was the practice instance key and outlived the eight per-language graphs by
	// one ticket: it was kept so a caller projecting a legacy language still had a
	// field to project into, and it went when `language` stopped being accepted on
	// the practice read arms — no family returned it, and every arm switching on
	// it was unreachable. Retired code is removed rather than left inert, so each
	// constant and its arms went with the thing that produced it. A future
	// account-keyed or language-keyed family reintroduces both together.
	//
	// CHECKS AND PRACTICE ARE BOTH IN THE FieldNone ARM, for one reason. Checks
	// carry a `language` metadata key on every node and practice carries a
	// `source_hub` key, so in each family the value selects a SUBSET WITHIN the
	// one graph rather than selecting which graph to open.
	//
	// GraphSelector.account AND GraphSelector.language SURVIVE ON THE WIRE, and
	// the asymmetry is deliberate: removing a proto field is its own change with
	// its own compatibility cost, while a client switch arm nothing can reach is
	// dead code inside one binary.
	FieldName
	// FieldNone — the family addresses NO instance: it holds exactly one graph,
	// so there is no key to carry and every instance field must stay empty.
	//
	// This is NOT the same as FieldName with an empty value. FieldName means "the
	// name field is how you would address an instance of this family"; FieldNone
	// means the family has no instances to address, so a name is a field its
	// resolver cannot honor and the server REFUSES it. Leaving such a family in
	// the FieldName default is how every write to the checks graph reached
	// production broken.
	FieldNone
)

// InstanceField is the ONE switch over graph family → instance-key field. Every
// instance-key builder in the client delegates to this; the switch body lives
// here and nowhere else.
func InstanceField(gt kgtypes.GraphType) Field {
	switch gt {
	case kgtypes.GraphCode:
		return FieldRepo
	case kgtypes.GraphChecks, kgtypes.GraphPractice:
		// checks carries NO instance identity anywhere: it is a singleton with no
		// named consumer, so no builder should ever put a name on it. Naming it
		// here rather than leaving it to the FieldName default is what stops a
		// selector carrying an instance name for a family that has none — the
		// omission that broke every write to this graph in production.
		//
		// knowledge and linkage are singletons too but are NOT here, and the
		// difference is evidence rather than taste: segmentdist addresses
		// knowledge graphs BY NAME ("kg", per manager_owner_test.go), so their
		// instance identity IS the name field even though the server's knowledge
		// resolver ignores it. Those are two different questions —
		// AddressesOneGraph below answers the second one.
		//
		// PRACTICE JOINED THIS ARM when the eight per-language practice graphs
		// became one combined graph. Its per-source grouping is a `source_hub`
		// metadata key on every node, exactly as checks' language is, so there is
		// no practice instance to address and the server's practice policy row
		// consumes no instance field. `language` is refused on every practice arm,
		// read and write alike, so no path composes one to address a graph.
		return FieldNone
	default:
		return FieldName
	}
}

// InstanceKeyOf is the REVERSE of the family switch: it reads the graph type and
// instance name back off a wire selector. It delegates to InstanceField so the
// two directions can never disagree about which field a family is keyed by.
//
// An empty Graph means the knowledge default, so it returns an empty instance
// name and leaves the ""→"default" collapse to workingset.Normalize — one
// normalization site, not two. A nil selector addresses nothing and reports
// ok=false.
//
// A selector naming only a graph TYPE, which is the shape a catalog enumeration
// compiles to, yields an empty instance name for every family whose key is
// repo.
func InstanceKeyOf(sel *knowledgev1.GraphSelector) (kgtypes.GraphType, string, bool) {
	if sel == nil {
		return "", "", false
	}
	gt, ok := typeOfSelector(sel)
	if !ok {
		return "", "", false
	}
	if gt == "" {
		return kgtypes.GraphKnowledge, "", true
	}
	switch InstanceField(gt) {
	case FieldRepo:
		return gt, sel.GetRepo(), true
	case FieldNone:
		// A singleton has no instance name to read back. Falling through to the
		// default would report whatever name a caller wrongly attached as this
		// graph's identity.
		return gt, "", true
	default:
		return gt, sel.GetName(), true
	}
}

// GraphSelectorFor builds a *knowledgev1.GraphSelector addressing (gt, name).
// When omitDefaultName is true, a name field is left empty for the empty string
// or the literal "default" (the guard the edge/selector-args call sites apply);
// repo/account are always set.
func GraphSelectorFor(gt kgtypes.GraphType, name string, omitDefaultName bool) *knowledgev1.GraphSelector {
	// BOTH fields are populated for the whole transition: the typed family for
	// peers that read it, and the legacy string for peers that predate it. See
	// the field comments on GraphSelector — client and server skew in practice,
	// so neither field alone reaches every peer.
	sel := &knowledgev1.GraphSelector{Graph: string(gt), Family: FamilyOf(gt)}
	switch InstanceField(gt) {
	case FieldRepo:
		sel.Repo = name
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			sel.Name = name
		}
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
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			payload["name"] = name
		}
	}
	return payload
}

// ApplyInstanceKey assigns name into exactly one of the caller-owned repo /
// name struct fields per (gt). When omitDefaultName is true, a name field is
// left untouched for the empty string or the literal "default".
//
// IT TAKES NEITHER AN account NOR A language POINTER, because no family is keyed
// by either any more: cloud and the CI inventory were account-keyed and both are
// retired, and practice was language-keyed until its eight per-language graphs
// became one. A parameter no arm can write to is a field a caller would keep
// filling for a case that cannot arise.
func ApplyInstanceKey(gt kgtypes.GraphType, name string, repo, nameField *string, omitDefaultName bool) {
	switch InstanceField(gt) {
	case FieldRepo:
		*repo = name
	case FieldName:
		if !omitDefaultName || (name != "" && name != "default") {
			*nameField = name
		}
	}
}

// InstanceValueOf is the CALLER-SIDE projection of the family switch: given every
// instance value a caller may have supplied, it returns the ONE the family
// actually consumes, and the empty string for a singleton that consumes none.
//
// It is the counterpart to ApplyInstanceKey, which writes one value INTO the
// caller's fields; this reads one value OUT of them. Both delegate to
// InstanceField, so a family's instance key is declared in exactly one place and
// every builder agrees with every reader by construction.
//
// WHY THIS EXISTS. Callers used to hand every field they had to the target
// builder at once, so a write could carry a repo AND a name and rely on the
// server to sort it out — which it does not: a field the target family does not
// consume is REFUSED, not ignored. Projecting first means a selector can only
// ever carry the field its family reads.
func InstanceValueOf(gt kgtypes.GraphType, repo, name string) string {
	if AddressesOneGraph(gt) {
		return ""
	}
	switch InstanceField(gt) {
	case FieldRepo:
		return repo
	case FieldNone:
		return ""
	default:
		return name
	}
}

// AddressesOneGraph reports whether gt is a SINGLETON family — one graph, so a
// caller has no instance to select and the server's resolver consumes no
// instance field for it.
//
// THIS IS A DIFFERENT QUESTION FROM InstanceField, and conflating them was a
// measured mistake. InstanceField answers "which field carries this family's
// instance IDENTITY", which is a client-internal addressing concern:
// segmentdist names knowledge graphs ("kg") and routes on that name, so
// knowledge's identity field is genuinely Name. AddressesOneGraph answers
// "would the SERVER'S RESOLVER consume an instance field", and for knowledge the
// answer is no — its arm returns the request-scoped composite and reads no name.
// The two coincide for every instance-addressed family and diverge here.
//
// Collapsing them broke segmentdist's per-graph routing outright: with knowledge
// reporting no identity field, three of its tests lost the graph name they route
// and log on.
//
// The empty string is knowledge by contract and is listed explicitly: it is the
// spelling almost every real knowledge write uses, since callers omit `graph`
// entirely, so covering only "knowledge" would fix the spelling nobody sends.
func AddressesOneGraph(gt kgtypes.GraphType) bool {
	switch gt {
	case "", kgtypes.GraphKnowledge, kgtypes.GraphLinkage, kgtypes.GraphChecks, kgtypes.GraphPractice:
		return true
	default:
		return false
	}
}
