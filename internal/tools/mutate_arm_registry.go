// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_registry.go holds the per-arm param classification table. Split out
// of mutate_param_accounting.go (which owns the types and the gate) purely to
// keep both files inside the repo's file-length convention; the two are one
// logical unit in one package.
//
// AUTHORING RULES for every cell in this file, derived from the arm's own
// source. A param is consumed when the arm demonstrably uses it as (1) ROUTING
// onto the Execute Target, (2) a BODY WRITE (set_fields / set_metadata /
// NodeBody / EdgeSpec), (3) a DISPATCH DISCRIMINANT whose value is what selects
// the arm, or (4) a DERIVATION driving a side effect or derived value. Anything
// else is rejected, except a render param on an arm that renders its own result
// — that is deliberately ignored WITH a justification, because rejecting a
// schema-advertised shape is itself a defect.
//
// There is no complement computed at runtime: every arm names every schema key
// explicitly across its three sets. A newly-added schema param therefore lands
// in NO set and fails the partition assertion until someone classifies it.

// justifyClientRendered is the shared justification for `format` on an arm that
// returns its own rendered result and never reaches the engine render path. It
// is deliberately ignored rather than rejected: the schema advertises format for
// these operations, so rejecting it would be a false rejection.
const justifyClientRendered = "this arm renders its own result; the format switch lives in the engine render path"

// justifyCriterionDerived is the rejection explanation for the two criterion
// params that are DERIVED rather than caller-supplied. It tells the caller what
// to set instead, so the error is actionable rather than merely a refusal.
const justifyCriterionDerived = "a criterion's name and summary are derived from its description " +
	"and command; set those instead"

// justifyCriterionKnowledgeOnly is the rejection explanation for `graph` on the
// criterion-create arm. The generic message ends "drop it or issue a separate
// call that does" — but for this param there IS no such call and there never
// will be: criteria are knowledge-graph-only by decision, not by omission. The
// generic wording invites exactly the re-litigation the decision settles, so
// this arm states the contract instead.
const justifyCriterionKnowledgeOnly = "criterion nodes are knowledge-graph-only — criteria attach to the " +
	"plan/step verifies structure, which no other graph family carries, so this path routes no graph " +
	"selector at all; drop the param"

// paramSet builds a membership set from an explicit key list. Used only to keep
// the table literals readable — it computes no complement.
func paramSet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// clientRenderedFormat is the one-entry deliberately-ignored map shared by every
// client-terminal arm.
func clientRenderedFormat() map[string]string {
	return map[string]string{"format": justifyClientRendered}
}

// The 21 dispatch arms. Each armID names one decision point in the mutate
// dispatch tree; together they cover every path a host-originated mutate call
// can take through the client intercept layer.
const (
	armCriterionCreate         armID = "armCriterionCreate"
	armCreateFinding           armID = "armCreateFinding"
	armCreateResearch          armID = "armCreateResearch"
	armCreateRule              armID = "armCreateRule"
	armCreateFallthrough       armID = "armCreateFallthrough"
	armCreateBatch             armID = "armCreateBatch"
	armUpsert                  armID = "armUpsert"
	armUpdateBackend           armID = "armUpdateBackend"
	armUpdateRollup            armID = "armUpdateRollup"
	armUpdateTyped             armID = "armUpdateTyped"
	armUpdateFallthrough       armID = "armUpdateFallthrough"
	armUpdateBatchIDs          armID = "armUpdateBatchIDs"
	armUpdateBatchItems        armID = "armUpdateBatchItems"
	armBulkUpdateMetadata      armID = "armBulkUpdateMetadata"
	armDelete                  armID = "armDelete"
	armAnswer                  armID = "armAnswer"
	armLinkCrossGraph          armID = "armLinkCrossGraph"
	armLinkFallthrough         armID = "armLinkFallthrough"
	armUnlink                  armID = "armUnlink"
	armGraphPassthrough        armID = "armGraphPassthrough"
	armNonKnowledgeFallthrough armID = "armNonKnowledgeFallthrough"
)

// mutateArmRegistry maps every dispatch arm to its param classification. The
// table is PRODUCTION data, not a test fixture: the gate enforces exactly the
// classification the harness asserts, so the two can never drift.
//
// `graph` is consumed on nearly every arm because the knowledge-graph guard
// reads it to decide whether the arm is reachable at all — a discriminant is
// consumed by definition. The exception is the criterion-create arm, which runs
// ahead of that guard and never reads graph; a graph-bearing criterion create is
// therefore rejected rather than silently written to the knowledge graph.
var createArmSpecs = map[armID]armSpec{
	// Criterion create runs ahead of the generic mutate intercept. It routes
	// step_id/description/command/criterion_type plus status/content/metadata
	// onto the upserted node. name and summary stay rejected PERMANENTLY: both
	// are derived (name from description, summary from criterion_type +
	// description + command), so accepting a caller value would silently lose it
	// to the derivation. Their rejection carries its own explanation.
	//
	// ticket_id/session/links also stay rejected — context-linking is a
	// capability this arm does not have, not a param it drops.
	armCriterionCreate: {
		operation: "create",
		handler:   "InterceptAddCriterion",
		consumed: paramSet(
			"operation", "type", "step_id", "description", "command", "criterion_type",
			"status", "content", "metadata",
		),
		rejected: paramSet(
			"supports",
			"id", "ids", "name", "summary", "expand_to_descendants", "source",
			"evidence", "question_id", "concludes", "scope", "enforcement", "from", "to", "relationship",
			"conclusion", "findings", "graph", "language", "keywords", "binary_vector",
			"confidence", "method", "edge_evidence", "last_validated", "link_graph", "branches_from",
			"links", "session", "ticket_id", "polarity", "weight", "reasoning", "charge_evidence",
			"thought_parent", "references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		rejectionReasons: map[string]string{
			"name":    justifyCriterionDerived,
			"summary": justifyCriterionDerived,
			"graph":   justifyCriterionKnowledgeOnly,
		},
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// Finding create consumes the node body it builds plus five derivation params:
	// question_id (answers + contains edges), concludes (fires the question status
	// update), supports (draws the supports edge), findings-adjacent references,
	// and the context-link trio. This is the ONLY arm that reads supports.
	armCreateFinding: {
		operation: "create",
		handler:   "handleClientMutateCreateFinding",
		consumed: paramSet(
			"operation", "type", "graph", "name", "summary", "description", "evidence", "source",
			"references", "question_id", "concludes", "ticket_id", "session", "links",
			"content", "status", "metadata", "supports",
		),
		rejected: paramSet(
			"id", "ids", "expand_to_descendants", "scope", "enforcement", "step_id",
			"command", "criterion_type", "from", "to", "relationship", "conclusion", "findings",
			"language", "keywords", "binary_vector", "confidence", "method", "edge_evidence",
			"last_validated", "link_graph", "branches_from", "polarity", "weight", "reasoning",
			"charge_evidence", "thought_parent", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// Research create maps name→question and content→background context. A
	// caller-supplied description or status overrides the question-text and
	// "open" defaults, and caller metadata is carried onto the node.
	armCreateResearch: {
		operation: "create",
		handler:   "handleClientMutateCreateResearch",
		consumed: paramSet(
			"operation", "type", "graph", "name", "summary", "content", "ticket_id", "session", "links",
			"description", "status", "metadata",
		),
		rejected: paramSet(
			"supports",
			"id", "ids", "expand_to_descendants", "source", "evidence",
			"question_id", "concludes", "scope", "enforcement", "step_id", "command", "criterion_type",
			"from", "to", "relationship", "conclusion", "findings", "language", "keywords",
			"binary_vector", "confidence", "method", "edge_evidence", "last_validated", "link_graph",
			"branches_from", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// Rule create carries scope + enforcement into metadata, on top of the caller
	// metadata map it seeds first. content and status land on the node body.
	armCreateRule: {
		operation: "create",
		handler:   "handleClientMutateCreateRule",
		consumed: paramSet(
			"operation", "type", "graph", "name", "summary", "description", "scope", "enforcement",
			"ticket_id", "session", "links", "content", "status", "metadata",
		),
		rejected: paramSet(
			"supports",
			"id", "ids", "expand_to_descendants", "source", "evidence",
			"question_id", "concludes", "step_id", "command", "criterion_type", "from", "to",
			"relationship", "conclusion", "findings", "language", "keywords",
			"binary_vector", "confidence", "method", "edge_evidence", "last_validated", "link_graph",
			"branches_from", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// Any other create type declines to the engine CREATE arm, which builds one
	// NodeBody from the seven body fields plus id and source. The thought/charge
	// fold-in params are rejected because the engine CREATE arm has no carrier
	// for them: absent the gate a direct create carrying one would drop it
	// silently, which is exactly what the rejection replaces.
	armCreateFallthrough: {
		operation: "create",
		handler:   "engine compileMutateCreate",
		consumed: paramSet(
			"operation", "type", "id", "name", "description", "summary", "content", "status",
			"metadata", "source", "graph", "language", "format",
		),
		rejected: paramSet(
			"supports",
			"ids", "expand_to_descendants", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "keywords", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// create_batch reads only the nodes[]/edges[] payload plus target routing —
	// the top-level body fields belong to the single-create shape.
	// `name` is REJECTED, like every other top-level body field: each created node
	// names itself in nodes[]{name}. It is not routing either — a foreign-graph
	// create_batch never reaches this arm (the non-knowledge guard claims it under
	// armNonKnowledgeFallthrough, practice/transformers under armGraphPassthrough),
	// so this arm only ever runs on the knowledge family, whose resolver reads no
	// selector name at all. It read as consumed while the compile arm copied it
	// into the Execute Target's graph-instance selector — a request for a graph
	// named after a node rather than a use of the param.
	armCreateBatch: {
		operation: "create_batch",
		handler:   "engine compileMutateCreate",
		consumed:  paramSet("operation", "nodes", "edges", "graph", "language", "format"),
		rejected: paramSet(
			"supports",
			"name",
			"type", "id", "ids", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "metadata", "keywords", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// Upsert builds ONE NodeBody carrying type/name/description/summary/content/
	// status/metadata/id/source. keywords has no NodeBody carrier, so it is
	// rejected rather than dropped.
	armUpsert: {
		operation: "upsert",
		handler:   "engine compileMutateUpsert",
		consumed: paramSet(
			"operation", "type", "id", "name", "description", "summary", "content", "status",
			"metadata", "source", "graph", "language", "format",
		),
		rejected: paramSet(
			"supports",
			"ids", "expand_to_descendants", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "keywords", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{},
	},
}
