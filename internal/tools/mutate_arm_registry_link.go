// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_registry_link.go holds the link / unlink arms plus the two
// graph-routed fallthrough arms. Split from its siblings only to keep each file
// inside the repo's file-length gate; see mutate_arm_registry.go for the
// authoring rules every cell in all three files follows.
// justifyUnlinkEdgeMetadata explains why the five edge-metadata params are
// ignored rather than rejected on an unlink: an edge is identified by from/to/
// relationship alone, so the metadata has nothing to attach to and the server
// ignores it. Rejecting would refuse a call that is correct apart from a no-op
// field a caller may reasonably carry over from the matching link.
const justifyUnlinkEdgeMetadata = "an unlink identifies the edge by from/to/relationship alone; " +
	"the engine leaves the EdgeSpec metadata zero and the server ignores it"

// justifyFallthroughLinkGraph explains the link_graph rejection on the engine
// LINK arm. The rejection is honest — the engine denies any non-empty
// link_graph outright — but this arm is also where a link_graph:linkage call
// lands when the cross-graph composer DECLINES it, and one decline cause is a
// transient foreign-graph enumeration failure. Without the retry hint an infra
// blip reads as a permanent "this param is not supported" verdict.
const justifyFallthroughLinkGraph = "the engine link arm cannot route link_graph; " +
	"a link_graph:linkage call reaches this path only when the cross-graph composer declined it, " +
	"which can be a transient graph-enumeration failure — retry before treating this as unsupported"

// WHY THE NEGATION PROOF PARAMS ARE classConsumed, not classDeliberatelyIgnored,
// on the arms a negation call can select. It is the SAME shape as `graph`, whose
// treatment mutate_arm_registry.go:82-86 already justifies: a param the layer
// above reads to decide whether the arm is REACHABLE AT ALL is a discriminant,
// and a discriminant is consumed by definition. InterceptNegationGate
// (negation_gate.go) is wired ahead of both InterceptThoughts and InterceptMutate
// at cmd/knowledge/internal/bootstrap/dream.go, and it reads verified_quote and
// cited_range to decide whether the call reaches its write handler at all. Same
// rule, same class — even though, like `graph`, neither ever lands a value in the
// MutationPlan the arm produces.
var linkArmSpecs = map[armID]armSpec{

	// The client-claimed cross-graph link. The linkage branch threads all five
	// edge-metadata params onto the composed edge.
	armLinkCrossGraph: {
		operation: "link",
		handler:   "handleClientCrossGraphLink",
		consumed: paramSet(
			"operation", "from", "to", "relationship", "link_graph", "graph", "language",
			"weight", "confidence", "method", "edge_evidence", "last_validated",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"type", "id", "ids", "name", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "conclusion", "findings",
			"metadata", "keywords", "binary_vector", "branches_from", "links", "session",
			"ticket_id", "polarity", "reasoning", "charge_evidence", "thought_parent", "references",
			"items", "nodes", "edges", "updates",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// A link the client declines lands on the engine LINK arm, which threads the
	// five edge-metadata params onto the EdgeSpec.
	//
	// link_graph is REJECTED here. The cross-graph composer upstream claims the
	// one value it understands; a link_graph link it declines reaches this arm,
	// and nothing downstream reads the param — the engine denies the whole shape
	// on a non-empty link_graph, and the dispatch default bucket forwards it
	// unaccounted. Rejecting a param no path routes cannot be a false rejection,
	// and waving it through is precisely the silent drop this gate exists to
	// close.
	//
	// `to` is the LINK endpoint the SERVER resolves, which is what makes it a
	// CONSUMED param here rather than one this arm merely forwards. On the
	// knowledge graph the engine hydrates both endpoints before it builds the edge:
	// an unresolvable endpoint returns CodeNotFound rather than a silently-written
	// dangling edge. Off the knowledge graph the endpoint stays raw — linking to an
	// id that is not a node is a live cross-graph contract there, and the engine's
	// probe is gated to the knowledge graph for exactly that reason.
	armLinkFallthrough: {
		operation: "link",
		handler:   "engine compileMutateByIDLinkUnlink",
		consumed: paramSet(
			"operation", "from", "to", "relationship", "graph", "language", "name", "format",
			"weight", "confidence", "method", "edge_evidence", "last_validated",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"type", "id", "ids", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "conclusion", "findings",
			"metadata", "keywords", "binary_vector", "link_graph", "branches_from", "links",
			"session", "ticket_id", "polarity", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
		),
		rejectionReasons:    map[string]string{"link_graph": justifyFallthroughLinkGraph},
		deliberatelyIgnored: map[string]string{},
	},

	// Unlink is split from the link fallthrough precisely because their param
	// surfaces differ: edge identity is from/to/relationship, and the engine
	// leaves every edge-metadata field zero on an UNLINK.
	//
	// `name` differs between the two arms for the same reason. Only the `link`
	// operation is dispatched AHEAD of the non-knowledge guard (the cross-graph
	// composer has to see foreign endpoints), so only armLinkFallthrough can run
	// on a name-addressed graph and route the param. An unlink on a foreign graph
	// is claimed by armNonKnowledgeFallthrough instead, leaving this arm on the
	// knowledge family alone — where the resolver reads no selector name and an
	// unlink writes no node body, so the param reaches nothing.
	armUnlink: {
		operation: "unlink",
		handler:   "engine compileMutateByIDLinkUnlink",
		consumed:  paramSet("operation", "from", "to", "relationship", "graph", "language", "format"),
		rejected: paramSet(
			"supports",
			"name",
			"type", "id", "ids", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "conclusion", "findings",
			"metadata", "keywords", "binary_vector", "link_graph", "branches_from", "links",
			"session", "ticket_id", "polarity", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{
			"weight":         justifyUnlinkEdgeMetadata,
			"confidence":     justifyUnlinkEdgeMetadata,
			"method":         justifyUnlinkEdgeMetadata,
			"edge_evidence":  justifyUnlinkEdgeMetadata,
			"last_validated": justifyUnlinkEdgeMetadata,
		},
	},

	// The practice/transformers passthrough re-dispatches the caller's verbatim
	// args through the engine, so it is OPERATION-POLYMORPHIC over create,
	// create_batch, update and delete; `operation` below names one representative
	// of that set. Its consumed set is the union of what those four engine arms
	// read.
	armGraphPassthrough: {
		operation: "create",
		handler:   "inline engine.Dispatch in InterceptMutate",
		consumed: paramSet(
			"operation", "type", "id", "ids", "name", "description", "summary", "content", "status",
			"metadata", "source", "keywords", "graph", "language", "format", "nodes", "edges",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"expand_to_descendants", "evidence", "question_id", "concludes", "scope", "enforcement",
			"step_id", "command", "criterion_type", "from", "to", "relationship", "conclusion",
			"findings", "binary_vector", "confidence", "method", "edge_evidence", "last_validated",
			"link_graph", "branches_from", "links", "session", "ticket_id", "polarity", "weight",
			"reasoning", "charge_evidence", "thought_parent", "references", "items", "updates",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// The non-knowledge fallthrough is defined by EXCLUSION and is reached by
	// effectively the whole operation vocabulary with a different body surface
	// each time — on a practice/transformers graph by every operation outside the
	// passthrough CRUD set, and on a genuinely foreign graph by every operation
	// at all. `operation` below names one representative.
	//
	// It therefore declares the WHOLE schema consumed, with nothing rejected and
	// nothing ignored: the client has no model of foreign-graph routing, so
	// enforcing the invariant here would guess rather than verify. Both of the
	// properties that make this arm hazardous are handled by rules that do not
	// depend on an enumeration being complete — the caller-side link conjunct
	// keys on the operation rather than on which upstream decline path was taken,
	// and a whole-schema consumed set holds for any operation that reaches here.
	armNonKnowledgeFallthrough: {
		operation: "upsert",
		handler:   "declined in InterceptMutate at the knowledge-graph guard",
		consumed: paramSet(
			"operation", "type", "id", "ids", "name", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "graph", "language", "metadata", "keywords", "binary_vector",
			"confidence", "method", "edge_evidence", "last_validated", "link_graph", "format",
			"branches_from", "links", "session", "ticket_id", "polarity", "weight", "reasoning",
			"charge_evidence", "thought_parent", "references", "items", "nodes", "edges", "updates", "supports",
			"verified_quote", "cited_range",
		),
		rejected:            paramSet(),
		deliberatelyIgnored: map[string]string{},
	},
}
