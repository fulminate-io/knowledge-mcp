// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_rollup.go holds the descendant-partitioning and
// success-message helpers the completed-status rollup arm delegates to. They
// live here rather than beside handleClientUpdateStatusRollup so that file stays
// inside the repo's per-file length budget.

import (
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// partitionRollupTargets splits a completed container's contains-tree
// descendants into the ids whose status the cascade writes and the nodes it
// deliberately leaves alone. The cascade writes status ONLY to a descendant
// whose own type is one of the five container types (project, phase, plan,
// step, ticket) — the closure hierarchy, and nothing else. Every other type is
// held and named: those nodes record evidence rather than task progress, so a
// container closing above them is evidence of none of it. Otherwise a criterion
// nobody executed reads green, a question nobody answered reads settled, and a
// finding nobody revisited reads done.
//
// Criteria and questions get their own buckets because each carries a specific
// remedy to hand back to the caller — run it and mark it, answer it or close
// it. Everything else shares heldOther, which carries the NODES rather than
// bare ids: that bucket is heterogeneous, so the message has to name each
// node's type for the enumeration to mean anything.
func partitionRollupTargets(rootID string, descs []*knowledgev1.Node) (cascade, heldCriteria, heldQuestions []string, heldOther []*knowledgev1.Node) {
	cascade = make([]string, 0, len(descs)+1)
	heldCriteria = make([]string, 0, len(descs))
	heldQuestions = make([]string, 0, len(descs))
	heldOther = make([]*knowledgev1.Node, 0, len(descs))

	cascade = append(cascade, rootID)
	for _, d := range descs {
		nodeType := kgtypes.NodeType(d.GetType())
		if isClientRollupContainer(nodeType) {
			if isTerminalForClientRollup(d.GetStatus()) {
				continue
			}
			cascade = append(cascade, d.GetId())
			continue
		}
		// Held — but an already-terminal node was not held back by anything, so
		// it is not news and is not announced.
		if isTerminalForClientRollup(d.GetStatus()) {
			continue
		}
		switch nodeType {
		case kgtypes.NodeCriterion:
			heldCriteria = append(heldCriteria, d.GetId())
		case kgtypes.NodeQuestion:
			heldQuestions = append(heldQuestions, d.GetId())
		default:
			heldOther = append(heldOther, d)
		}
	}
	return cascade, heldCriteria, heldQuestions, heldOther
}

// rollupStatusMessage renders the rollup's success line. A status write that
// moved more than the named node names every id it moved: a bare count tells
// the caller that something else changed without telling them what, which is
// how an unnoticed cascade survives. The evidence-bearing nodes the cascade
// skipped are named too, so the caller knows they still need attention.
//
// Nothing here is truncated. The enumeration IS the deliverable, and a capped
// list reintroduces the invisible-cascade defect on exactly the large trees
// where it matters most.
func rollupStatusMessage(rootID string, cascade, heldCriteria, heldQuestions []string, heldOther []*knowledgev1.Node) string {
	var b strings.Builder
	b.WriteString("Status updated: " + rootID + " → " + kgtypes.StatusCompleted + " [graph: knowledge/default]")
	if n := len(cascade) - 1; n > 0 {
		// cascade[0] is the root, already named above.
		b.WriteString(" — cascaded to " + strconv.Itoa(n) + " node" + pluralSuffix(n) + ": " +
			strings.Join(cascade[1:], ", "))
	}
	if len(heldCriteria) > 0 {
		b.WriteString(" — criteria left unmarked (status unchanged; mark each after you run it): " +
			strings.Join(heldCriteria, ", "))
	}
	if len(heldQuestions) > 0 {
		b.WriteString(" — questions left open (status unchanged; answer or close each): " +
			strings.Join(heldQuestions, ", "))
	}
	if len(heldOther) > 0 {
		b.WriteString(" — left unchanged (status untouched; these record evidence, not task progress): ")
		for i, n := range heldOther {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(n.GetId() + " (" + n.GetType() + ")")
		}
	}
	return b.String()
}
