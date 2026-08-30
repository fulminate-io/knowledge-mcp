// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_criterion_pair.go holds the create_batch step↔criterion attachment
// gate. It is the WRITE half of a convention the two native attach paths already
// obey and no create_batch caller was ever told about.
//
// THE CONVENTION. Attaching a criterion to a step takes a PAIR of edges:
// step--contains-->criterion AND criterion--verifies-->step. Both first-class
// paths emit both — projects/builders.go:202-203 (create_plan),
// projects/builders_test_plan.go:46-47 (create_test_plan) and
// intercept_add_criterion.go (mutate(create, type:criterion)) — so the pair is
// the shape of every criterion the tool itself writes, not one builder's habit.
//
// WHY A LONE EDGE IS A DEFECT RATHER THAN A STYLE CHOICE. plan_tree defaults its
// edge-type set to EdgeKGContains alone and passes a SINGLE type to the
// descendants walk (intercept_query_plan_tree.go:67 and :88 — the wire edge_type
// field can override the default, but the walk still takes one type, so no call
// reaches a criterion over both edges). A criterion attached by `verifies` alone
// is therefore written, is returned by the create, and is INVISIBLE in the
// rendered plan — a shape whose only symptom is a tree that renders fewer
// criteria than the batch reported creating. The mirror case,
// `contains` with no `verifies`, renders but loses the criterion→step
// back-reference every other criterion carries.
//
// REJECT, NEVER AUTO-COMPLETE. Growing the missing edge for the caller is the
// coerce-and-continue the house BAD-INPUT-ERRORS rule forbids — it silently
// rewrites the caller's stated edge set, and it has no principled stopping point.
// The rejection is pre-write (the gate runs at the accounting seam, before the
// batch reaches the engine) and names the edge that is missing.
//
// WHAT IS DECIDABLE HERE, STATED PLAINLY. This gate reads the caller's payload
// and issues no node lookups, so it classifies an endpoint only when the payload
// itself says what the endpoint is:
//
//   - `verifies` is criterion→step BY DEFINITION — kgtypes/edge_types.go:177 and
//     the server's edge_types_vocab.go:34 both define it that way and it has no
//     other emitter in the tree — so EVERY verifies edge in a batch is checked for
//     its contains partner, whichever form its endpoints take.
//   - `contains` is general-purpose (plan→phase, phase→step, ticket→finding,
//     session→thought), so it is checked for a verifies partner only when its TO
//     endpoint is an IN-BATCH nodes[] entry declaring type "criterion".
//
// THE RESIDUAL, uncovered on purpose and not silently: a `contains` edge to a
// criterion that ALREADY EXISTS outside the batch, addressed by to_id, carries
// nothing in the payload that identifies it as a criterion. Covering it means a
// node-type read inside the pre-write gate. That case is the rarer half of the
// rarer direction — the invisible-criterion failure this gate exists for is the
// verifies-only one, which is covered without qualification.

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// batchPairNode is what the pair gate reads off one nodes[] entry: its declared
// type, the only field that can identify an in-batch criterion.
type batchPairNode struct {
	Type string `json:"type"`
}

// batchPairEdge is what the pair gate reads off one edges[] entry. The two index
// fields are POINTERS for the same reason engine.edgeBody's UnmarshalJSON
// installs a -1 sentinel: the Go zero value 0 is a legal slot index, so an absent
// from_idx decoded into a plain int would silently read as "slot 0" and match the
// wrong node.
type batchPairEdge struct {
	FromIdx *int   `json:"from_idx"`
	ToIdx   *int   `json:"to_idx"`
	FromID  string `json:"from_id"`
	ToID    string `json:"to_id"`
	Type    string `json:"type"`
}

// pairEndpoint is one resolved edge endpoint: either a slot index into nodes[]
// (idx >= 0) or an existing node id. Two endpoints are the same endpoint when
// their rendered keys match, which is what lets the partner search compare a
// slot-addressed edge against an id-addressed one without conflating them.
type pairEndpoint struct {
	idx int
	id  string
}

// resolvePairEndpoint applies the create_batch endpoint rule — a slot index when
// one is supplied and non-negative, otherwise the string id — and reports
// ok=false for an entry that names neither, which is a malformed edge the
// engine's own decode rejects downstream.
func resolvePairEndpoint(idx *int, id string) (pairEndpoint, bool) {
	if idx != nil && *idx >= 0 {
		return pairEndpoint{idx: *idx, id: ""}, true
	}
	if id != "" {
		return pairEndpoint{idx: -1, id: id}, true
	}
	return pairEndpoint{}, false
}

// key renders an endpoint for equality comparison and for the error message.
// "nodes[3]" for a slot, the verbatim id otherwise — both are what the caller
// wrote, so the error quotes the caller's own spelling back.
func (e pairEndpoint) key() string {
	if e.idx >= 0 {
		return "nodes[" + strconv.Itoa(e.idx) + "]"
	}
	return e.id
}

// guardCreateBatchCriterionPair rejects a create_batch whose edges attach a
// criterion to a step in one direction only, naming the missing partner edge.
// Returns nil for every other batch — including a payload that does not decode,
// which the engine's own decode reports rather than this gate.
//
// Deterministic first hit: edges are walked in the caller's own order, so the
// same payload always names the same edge.
func guardCreateBatchCriterionPair(raw json.RawMessage) error {
	var payload struct {
		Nodes []batchPairNode `json:"nodes"`
		Edges []batchPairEdge `json:"edges"`
	}
	// A payload that does not parse is the DISPATCHER'S error to report — the
	// guard only inspects what parses, and must not preempt the real unmarshal
	// error with a duplicate. On failure payload.Edges stays empty and the
	// guard stands aside at the emptiness check below.
	_ = json.Unmarshal(raw, &payload)
	if len(payload.Edges) == 0 {
		return nil
	}
	const (
		contains = string(kgtypes.EdgeKGContains)
		verifies = string(kgtypes.EdgeVerifies)
	)
	for i, e := range payload.Edges {
		from, fromOK := resolvePairEndpoint(e.FromIdx, e.FromID)
		to, toOK := resolvePairEndpoint(e.ToIdx, e.ToID)
		if !fromOK || !toOK {
			continue
		}
		switch {
		case e.Type == verifies:
			// criterion --verifies--> step. Partner: step --contains--> criterion.
			if hasPairEdge(payload.Edges, contains, to, from) {
				continue
			}
			return pairError(i, verifies, contains, "criterion", "step", from, to)
		case e.Type == contains && inBatchCriterion(payload.Nodes, to):
			// step --contains--> criterion. Partner: criterion --verifies--> step.
			if hasPairEdge(payload.Edges, verifies, to, from) {
				continue
			}
			return pairError(i, contains, verifies, "step", "criterion", from, to)
		}
	}
	return nil
}

// hasPairEdge reports whether the batch carries an edge of the given type
// running from→to. Endpoint identity is the rendered key, so a partner supplied
// by slot index matches only a slot index and one supplied by id matches only
// that id — the gate never assumes a slot and an id denote the same node.
func hasPairEdge(edges []batchPairEdge, edgeType string, from, to pairEndpoint) bool {
	wantFrom, wantTo := from.key(), to.key()
	for _, e := range edges {
		if e.Type != edgeType {
			continue
		}
		ef, fromOK := resolvePairEndpoint(e.FromIdx, e.FromID)
		et, toOK := resolvePairEndpoint(e.ToIdx, e.ToID)
		if !fromOK || !toOK {
			continue
		}
		if ef.key() == wantFrom && et.key() == wantTo {
			return true
		}
	}
	return false
}

// inBatchCriterion reports whether an endpoint names a nodes[] slot whose
// declared type is criterion. An id-addressed endpoint is never classified here:
// the payload says nothing about a node it did not create.
func inBatchCriterion(nodes []batchPairNode, e pairEndpoint) bool {
	if e.idx < 0 || e.idx >= len(nodes) {
		return false
	}
	return nodes[e.idx].Type == string(kgtypes.NodeCriterion)
}

// pairError renders the rejection: which edge was supplied, which partner is
// missing, and the full pair convention — so a caller can repair the batch from
// the message without reading the schema again.
func pairError(idx int, supplied, missing, fromRole, toRole string, from, to pairEndpoint) error {
	return fmt.Errorf(
		"mutate(create_batch): edges[%d] attaches a criterion to a step with only one direction — "+
			"it carries %s--%s-->%s (%s → %s) but not its partner %s--%s-->%s (%s → %s). "+
			"A step/criterion attachment is a PAIR of edges: step--contains-->criterion AND "+
			"criterion--verifies-->step (plan_tree walks contains, so a verifies-only criterion "+
			"never renders under its step). Add the missing edge — the pair is never auto-completed",
		idx,
		fromRole, supplied, toRole, from.key(), to.key(),
		toRole, missing, fromRole, to.key(), from.key(),
	)
}
