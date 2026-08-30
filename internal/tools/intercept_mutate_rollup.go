// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_rollup.go holds the descendant-partitioning, hold-predicate
// and success-message helpers the terminal-status rollup arm delegates to,
// together with the sentinel a truncated contains walk is refused with. They live
// here rather than beside handleClientUpdateStatusRollup so that file stays
// inside the repo's per-file length budget.
//
// The message renderer is split in two: rollupStatusMessage is the local arm's
// whole line, and cascadeSummary is the part BOTH dispatch arms share — the
// tracker-backed arm renders its own opening line and appends this summary to it.

import (
	"errors"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// errRollupTraverseTruncated is the refusal a clamped contains walk earns. It is
// a REFUSAL, not a fallback: nothing is repaired, nothing degrades, the write
// simply does not happen and the caller is told which node it did not reach.
//
// A truncated walk matters here in a way it did not before the hold rule. A
// criterion clamped out of the descendant set makes the node that owns it look
// criterion-free, and that node then CASCADES — reintroducing, through a silent
// partial read, exactly the phantom completion the hold exists to remove.
// Cascading anyway would be a lane that fires forever on the same cause.
var errRollupTraverseTruncated = errors.New(
	"the contains walk was truncated, so the descendant set is incomplete and a node's criteria " +
		"may be missing from it; no status was written to the named node or to any descendant")

// hasUnevaluatedCriterion reports whether node directly contains a criterion
// whose work is not yet settled. That is the hold condition: a criterion records
// whether its check was RUN, so a container closing above one nobody marked is
// evidence of none of it.
//
// It reads DIRECT children only. The criterion belongs to the node that owns it,
// and attributing a grandchild's criterion upward would hold ancestors, which
// this rule deliberately does not do.
//
// IT TESTS THE SAME PREDICATE THE ANNOUNCER TESTS, AND THAT IS THE WHOLE POINT.
// A criterion a human deliberately marked cancelled HAS been dispositioned — its
// work is over, and it is not "not yet evaluated". Testing a narrower predicate
// here than partitionRollupTargets tests when deciding what to announce produced
// a state where the two disagreed about one node: the hold fired on a cancelled
// criterion while the announcer, correctly reading its work as over, said nothing
// about it. The container was then held with no pointer to the cause, the remedy
// the response names ("run and mark each criterion") was already satisfied, and
// re-issuing the completion reproduced the identical hold — a lane firing forever
// on the same cause. One predicate, one authority, no stuck state.
//
// A criterion in NEITHER set — a spelling no status vocabulary here
// recognizes — still holds, and is still announced. That pairing is the correct
// outcome: the hold is real and the caller is told which criterion to look at.
// The evaluated-pass spellings ("pass", "passed", "verified", "satisfied",
// "met") are NOT in that residue: isSettledForCascade recognizes them as a
// class, so a criterion whose check was run and passed neither holds nor is
// announced.
func hasUnevaluatedCriterion(node *knowledgev1.Node, childIndex map[string][]*knowledgev1.Node) bool {
	for _, child := range childIndex[node.GetId()] {
		if kgtypes.NodeType(child.GetType()) != kgtypes.NodeCriterion {
			continue
		}
		if !isSettledForCascade(child.GetStatus()) {
			return true
		}
	}
	return false
}

// partitionRollupTargets splits a completed container's contains-tree
// descendants into the ids whose status the cascade writes and the nodes it
// deliberately leaves alone. The cascade writes status ONLY to a descendant
// whose own type is one of the seven container types (project, phase, plan,
// step, ticket, test_plan, test_step) — the closure hierarchy, and nothing
// else. Every other type is held and named: those nodes record evidence rather
// than task progress, so a container closing above them is evidence of none of
// it. Otherwise a criterion nobody executed reads green, a question nobody
// answered reads settled, and a finding nobody revisited reads done.
//
// Criteria and questions get their own buckets because each carries a specific
// remedy to hand back to the caller — run it and mark it, answer it or close
// it. Everything else shares heldOther, which carries the NODES rather than
// bare ids: that bucket is heterogeneous, so the message has to name each
// node's type for the enumeration to mean anything.
//
// A container descendant is ALSO held, into heldUnevaluated, when it directly
// contains a criterion nobody has marked AND the cascade is writing
// kgtypes.StatusCompleted. Statuses are the audit surface: a governance gate
// whose container closed above it would otherwise read green having never run.
// THE ROOT IS NEVER HELD — the caller named it, and holding it would make a
// criteria-bearing node impossible to complete at all — which is why rootID is
// appended before the loop and never re-examined inside it.
//
// THE HOLD IS SCOPED TO COMPLETED-FAMILY CASCADES, and outside them it inverts.
// "completed" claims the work was done, so holding a node whose criteria nobody
// ran is what keeps that claim honest. "skipped" claims the work was abandoned,
// which is true of a live descendant whether or not its criteria were ever
// evaluated — so holding it there would leave the node LIVE under a dead
// container, which is the phantom the cascade exists to remove. cascadeStatus is
// what tells the two apart, and both conjuncts sit on one statement so the rule
// cannot be satisfied by a condition written elsewhere in this function.
//
// Holding a node does NOT hold its ancestors: a phase whose step is held still
// completes. The exclusion attaches to the node carrying the unevaluated
// criteria, and naming that node in the response is what keeps the audit surface
// honest at the node itself.
//
// structureEdges are the contains edges the traversal returned alongside the
// nodes; the child index built from them is what attributes a criterion to the
// node that owns it. A caller that passes none gets no holds, because nothing
// can be attributed.
//
// cascadeStatus is the status the cascade will write to the ids it returns. It
// scopes the unevaluated-criterion hold, as described above.
func partitionRollupTargets(
	rootID string,
	descs []*knowledgev1.Node,
	structureEdges []*knowledgev1.Edge,
	cascadeStatus string,
) (cascade, heldCriteria, heldQuestions []string, heldUnevaluated, heldOther []*knowledgev1.Node) {
	cascade = make([]string, 0, len(descs)+1)
	heldCriteria = make([]string, 0, len(descs))
	heldQuestions = make([]string, 0, len(descs))
	heldUnevaluated = make([]*knowledgev1.Node, 0, len(descs))
	heldOther = make([]*knowledgev1.Node, 0, len(descs))

	childIndex, _ := render.BuildChildIndex(rootID, descs, structureEdges)

	cascade = append(cascade, rootID)
	for _, d := range descs {
		nodeType := kgtypes.NodeType(d.GetType())
		if isClientRollupContainer(nodeType) {
			if isSettledForCascade(d.GetStatus()) {
				continue
			}
			if cascadeStatus == kgtypes.StatusCompleted && hasUnevaluatedCriterion(d, childIndex) {
				heldUnevaluated = append(heldUnevaluated, d)
				continue
			}
			cascade = append(cascade, d.GetId())
			continue
		}
		// Held — but a descendant whose own work is already over was not held back
		// by anything, so it is not news and is not announced.
		if isSettledForCascade(d.GetStatus()) {
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
	return cascade, heldCriteria, heldQuestions, heldUnevaluated, heldOther
}

// rollupNamedNodeFields returns, in a stable order, the names of the body fields
// the caller supplied alongside the status. An empty result is the status-only
// rollup, which must stay byte-for-byte the original two-RPC path.
//
// It lives here rather than beside its single caller because that file is within
// a few lines of the repo's per-file length budget and this helper is
// self-contained.
func rollupNamedNodeFields(a mutateArgs) []string {
	var names []string
	if a.Name != "" {
		names = append(names, "name")
	}
	if a.Description != "" {
		names = append(names, "description")
	}
	if a.Summary != "" {
		names = append(names, "summary")
	}
	if a.Content != "" {
		names = append(names, "content")
	}
	if a.Keywords != "" {
		names = append(names, "keywords")
	}
	if a.Source != "" {
		names = append(names, "source")
	}
	if len(a.Metadata) > 0 {
		names = append(names, "metadata")
	}
	return names
}

// rollupStatusMessage renders the LOCAL arm's success line: the named node's own
// transition, followed by the shared cascade summary.
//
// rootStatus is the status the named node itself took — the caller's own
// spelling, which is not necessarily the status its descendants took. Rendering
// the caller's spelling rather than a normalized one is deliberate: the message
// reports what was written, and a container that went to "Canceled" did not go to
// "skipped".
func rollupStatusMessage(
	rootID string,
	rootStatus, cascadeStatus string,
	cascade, heldCriteria, heldQuestions []string,
	heldUnevaluated, heldOther []*knowledgev1.Node,
) string {
	return "Status updated: " + rootID + " → " + rootStatus + " [graph: knowledge/default]" +
		cascadeSummary(cascadeStatus, cascade, heldCriteria, heldQuestions, heldUnevaluated, heldOther)
}

// cascadeSummary renders everything a cascade has to say about the nodes OTHER
// than the one the caller named, and both dispatch arms render through it so the
// two cannot drift.
//
// A status write that moved more than the named node names every id it moved: a
// bare count tells the caller that something else changed without telling them
// what, which is how an unnoticed cascade survives. The evidence-bearing nodes
// the cascade skipped are named too, so the caller knows they still need
// attention.
//
// Nothing here is truncated. The enumeration IS the deliverable, and a capped
// list reintroduces the invisible-cascade defect on exactly the large trees
// where it matters most.
//
// It lives in this file rather than beside the cascade helpers because the reuse
// fences pin pluralSuffix's call site here.
func cascadeSummary(
	cascadeStatus string,
	cascade, heldCriteria, heldQuestions []string,
	heldUnevaluated, heldOther []*knowledgev1.Node,
) string {
	var b strings.Builder
	if n := len(cascade) - 1; n > 0 {
		// cascade[0] is the root, which the caller's own line already named.
		b.WriteString(" — cascaded " + cascadeStatus + " to " + strconv.Itoa(n) + " node" + pluralSuffix(n) + ": " +
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
	// The held-container bucket. Named rather than counted, and named with each
	// node's type, because the remedy is per node: run its criteria, mark them,
	// then complete it explicitly. A hold nobody is told about leaves the node
	// silently open, which is the invisible-cascade defect wearing the opposite
	// sign.
	if len(heldUnevaluated) > 0 {
		b.WriteString(" — nodes held (criteria not yet evaluated; run and mark each criterion, then complete the node explicitly): ")
		for i, n := range heldUnevaluated {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(n.GetId() + " (" + n.GetType() + ")")
		}
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
