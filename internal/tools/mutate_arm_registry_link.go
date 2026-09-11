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

// justifyUnlinkLinkGraph explains the link_graph rejection on the UNLINK arm and,
// unlike the generic message, names the call that works. The rejection itself is
// honest — only `link` is dispatched ahead of the cross-graph composer
// (intercept_mutate.go:222), so unlink never reaches the one path that routes the
// param, and the engine denies any non-empty link_graph outright
// (engine/compile_mutate.go). But a linkage edge IS deletable today, and a caller
// told only "this path does not route it" reasonably concludes the linkage graph
// is unreachable from unlink, which is false.
//
// THE ASYMMETRY IS DOCUMENTED, NOT REPAIRED, and it runs on two axes: the graph
// is named `link_graph` on link but `graph` on unlink, and link RESOLVES a raw
// foreign id (materializing the proxy) while unlink does no resolution and needs
// the proxy id the link already created. Routing link_graph on unlink would be
// worth nothing without also giving unlink the composer's proxy resolution.
const justifyUnlinkLinkGraph = "unlink addresses the linkage graph through `graph`, not `link_graph`: " +
	"mutate(unlink, from:<id>, to:\"proxy:knowledge:<code-id>\", relationship:\"...\", graph:\"linkage\"). " +
	"The endpoint must be the PROXY id — unlike link, unlink resolves no raw foreign id"

// justifyEdgeArmSourceHub names the call that DOES read `source_hub` on an edge
// write, which the generic "issue a separate call that does" leaves the caller
// to guess at. The two arms carrying this reason serve families that have no
// source hubs — the knowledge family for both, plus the name-addressed foreign
// graphs the link fallthrough also runs on — so the param reaches nothing HERE
// while being routed one family over.
const justifyEdgeArmSourceHub = "`source_hub` scopes the endpoints of a PRACTICE edge write to the nodes " +
	"grouped under one hub, and this arm serves families that have no hubs; the call that reads it is " +
	"mutate(link|unlink, graph:\"practice\", source_hub:\"<hub id>\", from/to/relationship), where BOTH endpoints " +
	"must already be grouped under that hub"

// justifyUnlinkName names the shape that DOES route `name` on an unlink, which
// the generic "issue a separate call that does" leaves the caller to guess at.
// This arm is reached only on the knowledge family (the non-knowledge guard
// claims every foreign-graph unlink under armNonKnowledgeFallthrough), and there
// the resolver reads no selector name and an unlink writes no node body — so the
// param reaches nothing HERE while being perfectly routable one graph over.
const justifyUnlinkName = "this arm runs only on the knowledge family, whose resolver reads no selector " +
	"name and whose unlink writes no node body; `name` is the graph-INSTANCE selector, so it routes only " +
	"on a name-addressed graph — mutate(unlink, graph:\"<family>\", name:\"<instance>\", from/to/relationship)"

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
			// source_hub SCOPES THE ENDPOINTS' RESOLUTION on a practice link, which
			// is not what it does on a create: there it GROUPS the written node, and
			// an edge belongs to no hub. Here both from and to must be practice
			// nodes grouped under the named hub or the write is refused naming the
			// endpoint and its actual hub — the same shape `source` takes on
			// assemble, where it scopes a by-id resolve rather than grouping
			// anything. IT IS ONLY EVER READ ON PRACTICE: every other family is
			// refused by name at the head of InterceptMutate
			// (refusePracticeHubOffFamily), above this arm and every other, so a
			// call that reaches this gate carrying a hub is a practice call. The
			// refusal used to ride this arm's own gate, and moving it up is what
			// closed the same hole on the arms that had no such gate.
			"source_hub",
			"operation", "from", "to", "relationship", "link_graph", "graph",
			"weight", "confidence", "method", "edge_evidence", "last_validated",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			// `language` WAS CONSUMED HERE while practice was language-addressed:
			// a cross-graph link could name one of the eight instance-keyed
			// practice graphs. Those are retired and nothing on an edge arm reads
			// the param — an edge carries no node body, so there is no checks
			// corpus language for it to be either, which is what keeps it consumed
			// on the passthrough arms below.
			"language",
			"repo", "account",
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
			"operation", "from", "to", "relationship", "graph", "name", "format",
			"weight", "confidence", "method", "edge_evidence", "last_validated",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			// `language`, for the reason the cross-graph arm above records: an edge
			// arm reads it on no family now that practice is not language-addressed.
			"language",
			"source_hub",
			"repo", "account",
			"supports",
			"type", "id", "ids", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "conclusion", "findings",
			"metadata", "keywords", "binary_vector", "link_graph", "branches_from", "links",
			"session", "ticket_id", "polarity", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
		),
		rejectionReasons: map[string]string{
			"link_graph": justifyFallthroughLinkGraph,
			"source_hub": justifyEdgeArmSourceHub,
		},
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
		consumed:  paramSet("operation", "from", "to", "relationship", "graph", "format"),
		rejected: paramSet(
			"source_hub",
			"repo", "account",
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
		rejectionReasons: map[string]string{
			"link_graph": justifyUnlinkLinkGraph,
			"name":       justifyUnlinkName,
			"source_hub": justifyEdgeArmSourceHub,
		},
		deliberatelyIgnored: map[string]string{
			"language":       justifyKnowledgeSingletonSelector,
			"weight":         justifyUnlinkEdgeMetadata,
			"confidence":     justifyUnlinkEdgeMetadata,
			"method":         justifyUnlinkEdgeMetadata,
			"edge_evidence":  justifyUnlinkEdgeMetadata,
			"last_validated": justifyUnlinkEdgeMetadata,
		},
	},

	// The practice/checks passthrough re-dispatches the caller's
	// verbatim args through the engine, so it is OPERATION-POLYMORPHIC over create,
	// create_batch, update and delete; `operation` below names one representative
	// of that set. Its consumed set is the union of what those four engine arms
	// read.
	armGraphPassthrough: {
		operation: "create",
		handler:   "inline engine.Dispatch in InterceptMutate",
		consumed: paramSet(
			// source_hub is consumed by ALL FOUR of this arm's operations, and it
			// means a different thing on three of them — which is why one declared
			// class here is not enough on its own to know the param is routed.
			// create and create_batch GROUP the written node (the hub id is stamped
			// onto its metadata AND the node→hub sourced-from edge rides the same
			// CreateBatch); delete uses it as a SELECTION axis; update SCOPES ITS
			// TARGETS to the hub client-side and puts nothing on the wire. The
			// per-operation census that holds this arm to all four is
			// mutate_arm_polymorphic_hub_test.go, because this cell cannot.
			//
			// AND IT IS PRACTICE-ONLY, though this arm also serves checks. A checks
			// write carrying the param is refused at the head of InterceptMutate
			// before it reaches here, so the create lowering below — which is
			// family-blind and once stamped a practice hub onto a checks node —
			// is now reachable by the practice half of this arm alone.
			"source_hub",
			"operation", "type", "id", "ids", "name", "description", "summary", "content", "status",
			"metadata", "source", "keywords", "graph", "format", "nodes", "edges",
			// language IS CONSUMED HERE, AND ITS CLASS IS GRAPH-DEPENDENT — the one
			// param on this arm whose behaviour splits by the graph it serves. This
			// arm claims BOTH practice and checks: on a CHECKS write `language` is
			// the check's corpus language and is read, and on a PRACTICE write it is
			// REFUSED by name (practice is one combined graph, so a write has no
			// per-language graph to land in and dropping it would silently redirect
			// the write; it is refused on a practice READ on the same terms now).
			// The registry classifies one param per arm, so it is declared for the
			// half that reads it and the practice refusal rides its own guard; the
			// parity fixture drives this cell against the checks half for that
			// reason.
			"language",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"repo", "account",
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
	// each time — on a practice/checks graph by every operation outside the
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
			// source_hub is APPLIED on this arm's practice operations rather than
			// merely declared consumed: unlink scopes its endpoints, and
			// update_batch, bulk_update_metadata and upsert scope their targets —
			// an upsert whose key does NOT resolve is refused here rather than
			// grouped, because only a create plan can carry the hub key and the
			// sourced-from edge together. On EVERY
			// OTHER FAMILY this arm never sees the param at all — the head of
			// InterceptMutate refuses it there — which is what turned this
			// declaration from an aspiration into a fact: a whole-schema consumed
			// set on an arm this polymorphic asserts nothing by itself, and the
			// operation census in mutate_arm_polymorphic_hub_test.go plus the
			// family grid in mutate_family_arm_hub_test.go are what hold it.
			"source_hub",
			"operation", "type", "id", "ids", "name", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "graph", "language", "metadata", "keywords", "binary_vector",
			"confidence", "method", "edge_evidence", "last_validated", "link_graph", "format",
			"branches_from", "links", "session", "ticket_id", "polarity", "weight", "reasoning",
			"charge_evidence", "thought_parent", "references", "items", "nodes", "edges", "updates", "supports",
			"verified_quote", "cited_range",
			// The graph-INSTANCE selectors. This is the arm every non-knowledge
			// mutate is accounted under and the arm that declines to the engine,
			// where the Target is built — so it is the one place repo and account
			// are routing rather than an unaddressable param.
			"repo", "account",
		),
		rejected:            paramSet(),
		deliberatelyIgnored: map[string]string{},
	},
}
