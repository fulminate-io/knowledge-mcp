// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_cascade.go holds the terminal-status cascade: the downward
// status mapping, the evaluated-pass class and its canonical spelling, the
// settled-descendant predicate, the container-type predicate that decides which
// node types the rollup reaches at all, the single claim predicate both dispatch
// arms consult, the batch writer and its partial-write reporter, and the whole
// cascade in one call for the tracker-backed arm.
//
// It is a separate file from its two callers because both of them are within a
// few lines of the repo's per-file length budget, and because keeping the
// cascade's own vocabulary in one place is what makes the mapping table
// reviewable cell by cell.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// terminalCascadeStatus maps a container's own terminal status to the status its
// live descendants take, and reports whether the input is terminal at all. A
// status it does not recognize returns ("", false) and the caller does not
// cascade.
//
// THE PRINCIPLE THAT PICKS EVERY CELL: THE CASCADE MUST NEVER MAKE A FALSE CLAIM
// ABOUT A DESCENDANT. "completed" claims the work was DONE, which is only safe
// under a completed-family parent and, even there, only with the
// unevaluated-criterion hold in place. "skipped" claims the work was ABANDONED
// and will not be done, which is true of every LIVE descendant of a canceled,
// failed or wont_do parent — and only live descendants are ever in the set,
// because already-settled ones are skipped by isSettledForCascade. Anyone
// extending this table applies that test to the new cell rather than guessing.
//
// The members are the corpus as it is actually spelled, not an invented
// vocabulary: tracker display casing ("Canceled", "Done") and the local
// vocabulary fold onto the same cells through the normalize step, which is the
// same lower-then-trim-then-switch shape parsePriority uses.
//
// DELIBERATE NON-MEMBERS, each with where it is handled instead:
//   - "invalidated" is the update-shaped negation status, gated on a verified
//     quote by the negation gate. It makes a claim about a node's TRUTH rather
//     than about its work being over. Invalidating a plan is not closing it, and
//     cascading anything downward from it would be a claim nobody made.
//   - "parked", "blocked", "open", "active", "in_progress" and the tracker's
//     workflow states are live: no cascade, which is what the non-terminal
//     characterization test pins.
//   - The empty string is a clear-to-blank. It returns ("", false) here; on a
//     tracker-backed node it is separately refused before this point, because a
//     tracker's status vocabulary has no blank state.
func terminalCascadeStatus(containerStatus string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(containerStatus)) {
	case "completed", "done", "closed", "archived":
		return kgtypes.StatusCompleted, true
	case "canceled", "cancelled", "wont_do", "failed":
		return kgtypes.StatusSkipped, true
	case "superseded":
		return "superseded", true
	case kgtypes.StatusSkipped:
		return kgtypes.StatusSkipped, true
	}
	return "", false
}

// criterionPassStatus is the CANONICAL spelling of the evaluated-pass class —
// the status a criterion carries when its check was run and it passed. New
// writes use this spelling; the four other accepted spellings below are read,
// never written.
//
// It is derived from the shipped status vocabulary rather than invented: "pass"
// is the only pass-outcome status literal this codebase declares anywhere (see
// the test_run line in the node-type help and the "pending | pass | fail | skip"
// block in the status help), and it sits in the same short-form family as the
// sibling outcomes fail and skip.
const criterionPassStatus = "pass"

// isEvaluatedPass reports whether a status means the node's check was RUN and it
// PASSED — the evaluated-pass class.
//
// THE FIVE MEMBERS ARE THE CORPUS AS IT IS ACTUALLY SPELLED, not an invented
// vocabulary: a complete census of every criterion in a live corpus found the
// class written five ways, and all five mean the same thing. Treating them as
// one class is a vocabulary ruling, taken deliberately, and this arm is the
// code half of it — the data half normalizes existing nodes onto
// criterionPassStatus.
//
// MATCHED EXACTLY, WITH NO CASE FOLDING, unlike terminalCascadeStatus above and
// like isTerminalForClientRollup beside it. That table folds case because
// tracker DISPLAY casing ("Done", "Canceled") reaches it; these five are data
// spellings written by local callers, and the census that produced the class
// found every member of it in lower case. A status outside these five keeps
// whatever classification it already had.
func isEvaluatedPass(status string) bool {
	switch status {
	case criterionPassStatus, "passed", "verified", "satisfied", "met":
		return true
	}
	return false
}

// isSettledForCascade reports whether a descendant's own status already means its
// work is over, so the cascade leaves it alone.
//
// IT IS WIDER THAN isTerminalForClientRollup, AND THE WIDENING IS REQUIRED FOR
// CORRECTNESS RATHER THAN TIDINESS. That predicate's members are completed,
// closed, archived, skipped, failed and superseded — it does not include done,
// canceled, cancelled or wont_do. Without the widening a cascade under a canceled
// container would overwrite a descendant a human had deliberately set to
// "cancelled", replacing a human's status with a machine's. That is a
// data-integrity regression this cascade would itself introduce, so closing it is
// part of the same change.
//
// THE THIRD DISJUNCT IS THE EVALUATED-PASS CLASS, and it closes a second
// instance of the same defect. A criterion spelled "pass" or "verified" HAS been
// evaluated — that is precisely what the spelling records — yet it sat in
// neither of the two vocabularies above, so it held its container while the
// response told the caller to go "run and mark" a criterion that had already
// been run and marked. A remedy already satisfied is a lane that fires forever
// on the same cause.
//
// IT HAS THREE CALL SITES AND THE WIDENING POINTS A DIFFERENT WAY AT EACH, so
// each is stated rather than covered by one claim about "skips":
//   - The partitioner's CONTAINER branch: a superset strictly ADDS skips, so
//     nothing the existing completed rollup writes today stops being written. For
//     the pass class specifically that means a container descendant spelled
//     "pass" is now left alone rather than overwritten with "completed" — the
//     same never-overwrite-an-evaluated-status rule the cancelled case turns on.
//   - The partitioner's ANNOUNCE branch: a superset strictly REMOVES
//     announcements, because a node whose work is already over was not held back
//     by anything and is not news. That direction is only correct while the hold
//     predicate agrees about which statuses are settled — when it did not, a
//     cancelled criterion was held by one and silenced by the other, leaving a
//     hold with no pointer to its cause.
//   - hasUnevaluatedCriterion: the same authority, which is what keeps the two
//     branches above consistent. This is where the pass class does its work — a
//     passed criterion stops holding its container.
//
// isTerminalForClientRollup is deliberately NOT modified. It is the narrow
// reading and it stays the first disjunct here; this predicate is where every
// widening lands, so the narrow one stays available to any caller that wants it.
func isSettledForCascade(status string) bool {
	if isTerminalForClientRollup(status) {
		return true
	}
	if isEvaluatedPass(status) {
		return true
	}
	_, ok := terminalCascadeStatus(status)
	return ok
}

// clientRollupContainerTypes is the ONE declaration of the criteria-owning
// container vocabulary. isClientRollupContainer tests membership against it, and
// the criterion step_id refusal (validateCriterionArgs,
// intercept_add_criterion.go) renders it, so the rule and the message quoting it
// cannot drift.
var clientRollupContainerTypes = []kgtypes.NodeType{
	kgtypes.NodeProject, kgtypes.NodeTicket, kgtypes.NodePlan, kgtypes.NodePhase,
	kgtypes.NodeStep, kgtypes.NodeTestPlan, kgtypes.NodeTestStep,
}

// isClientRollupContainer returns true for the seven container types that
// participate in the closure rollup: project, ticket, plan, phase, step,
// test_plan, test_step. These are the containers whose live descendants take a
// mapped status when the container itself reaches a terminal one.
//
// MEMBERSHIP IS DECIDED BY ONE STRUCTURAL TEST — does the type sit on a
// contains-path down to a criterion? — and not by a list anyone maintains by
// hand. A type on that path either strands criteria below it when it is a
// descendant, or closes with no cascade and no hold when it is the named root.
// The census in intercept_mutate_rollup_testplan_hold_test.go derives that path
// set from the project-domain BUILDERS and asserts it equals this predicate cell
// by cell, in both directions, so a new criteria-bearing container type cannot
// ship outside the rollup.
//
// THE TEST-PLAN PAIR WAS AN ENUMERATION GAP, not an exclusion. The five original
// members were carried over verbatim from the server-side rollup branch this
// predicate replaced; nothing anywhere recorded a reason to leave test_plan out,
// and a live close of a test plan carrying twenty unevaluated criteria returned a
// bare affected-count with no cascade, no hold and nothing named. test_plan
// contains test_step contains criterion is the same shape as plan contains phase
// contains step contains criterion, so it earns the same treatment.
//
// ONE PREDICATE, NEVER A SECOND TYPE LIST. Both the partitioner and the shared
// claim predicate below consult this one, and it tests membership against the
// single clientRollupContainerTypes declaration above, so a widening reaches the
// descendant hold, the root's cascade eligibility and the criterion step_id
// refusal together; a hold-only list would have fixed the held descendant while
// leaving a named test_plan root closing silently.
//
// It lives in this file, beside the claim predicate that gates the ROOT on it,
// rather than beside the create/answer handlers it was originally extracted with:
// this is cascade vocabulary, and intercept_mutate_create.go is at the repo's
// per-file length budget.
func isClientRollupContainer(t kgtypes.NodeType) bool {
	return slices.Contains(clientRollupContainerTypes, t)
}

// cascadeStatusForContainerUpdate is the SINGLE claim predicate both dispatch
// arms consult, and it returns the status the descendants should take — or "" for
// "do not cascade". ONE predicate, two call sites: duplicating this decision
// across the local and tracker-backed arms is the defect generator this shape
// exists to avoid, since the two would then be free to disagree about which
// updates cascade.
//
// The four "" cases are the four old conjuncts of the local arm's claim guard,
// absorbed here so the arms keep identical precedence: no node to classify, a
// node that is not one of the seven container types, an explicit
// expand_to_descendants:false opt-out, and a status the mapping does not
// recognize as terminal.
//
// THE SECOND CASE IS WHY THE CONTAINER SET IS ONE PREDICATE. It decides whether
// the ROOT the caller named cascades at all, while partitionRollupTargets uses
// the same predicate to decide which DESCENDANTS move. A type missing from it is
// therefore two defects at once — a root that closes silently and a descendant
// bucketed as evidence — and both close together when the predicate widens.
func cascadeStatusForContainerUpdate(a mutateArgs, node *knowledgev1.Node) string {
	if node == nil {
		return ""
	}
	if !isClientRollupContainer(kgtypes.NodeType(node.GetType())) {
		return ""
	}
	if !a.cascadeToDescendants() {
		return ""
	}
	cascadeStatus, ok := terminalCascadeStatus(a.Status)
	if !ok {
		return ""
	}
	return cascadeStatus
}

// writeCascadeStatuses applies the root's own status and the mapped descendant
// status to the partition's id slice, whose FIRST element is the root because the
// partitioner prepends it. It returns whether the ROOT's status landed, which is
// what lets a failure be reported as the partial write it is.
//
// Three shapes, and the branch is what preserves the existing cost contract:
//   - rootStatus == cascadeStatus: ONE batch over the whole slice. This is the
//     status:"completed" case, byte-identical on the wire to the pre-existing
//     rollup, which is what keeps that path at one traverse and one update.
//   - rootStatus == "": ONE batch over the descendants only. The tracker-backed
//     arm, whose caller already wrote the root through the local forward — so
//     rootWritten is reported true.
//   - otherwise: TWO batches, the root at its own status then the descendants at
//     the mapped one. The "Done" ticket case, where the root keeps the caller's
//     spelling and the descendants take "completed".
//
// An empty descendant slice is a no-op: the batch helper returns nil for no ids.
func writeCascadeStatuses(
	ctx context.Context,
	gc GraphCaller,
	ids []string,
	rootStatus, cascadeStatus, bundleID string,
) (rootWritten bool, err error) {
	if rootStatus == cascadeStatus {
		if werr := UpdateBatchStatus(ctx, gc, ids, cascadeStatus, bundleID); werr != nil {
			// One batch, so nothing landed.
			return false, werr
		}
		return true, nil
	}
	if rootStatus == "" {
		if werr := UpdateBatchStatus(ctx, gc, descendantIDs(ids), cascadeStatus, bundleID); werr != nil {
			return true, werr
		}
		return true, nil
	}
	if werr := UpdateBatchStatus(ctx, gc, rootIDOnly(ids), rootStatus, bundleID); werr != nil {
		return false, werr
	}
	if werr := UpdateBatchStatus(ctx, gc, descendantIDs(ids), cascadeStatus, bundleID); werr != nil {
		// The root moved and the descendants did not — the state
		// cascadeWriteFailureResult exists to report rather than hide.
		return true, werr
	}
	return true, nil
}

// rootIDOnly and descendantIDs split the partition's slice at the root, which it
// prepends. Named rather than sliced inline so the two call sites cannot drift on
// which end the root sits at.
func rootIDOnly(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids[:1]
}

func descendantIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids[1:]
}

// cascadeWriteFailureResult reports a cascade write failure, distinguishing the
// state where the root's status LANDED from the state where nothing did.
//
// THE DISTINCTION IS NEW BECAUSE THE PARTIAL STATE IS NEW. While the cascade was
// a single batch a failure meant nothing was written, and the shared failure
// helper's "status reached neither" was accurate. The two-call branch breaks
// that: the root write can land while the descendant write fails, and a message
// that named neither would tell the caller the root had not moved when it had.
//
// On the not-root-written path it DELEGATES to rollupFailureResult rather than
// re-authoring its messages, so there is one message body per state and no fork.
// That delegation is not byte-neutral in both directions and the difference is
// deliberate: the fields arm ignores the stage argument entirely and is preserved
// byte-for-byte, while the status-only arm gains a "cascade write" stage prefix,
// which names WHICH of the cascade's writes failed.
//
// The root-written literal is deliberately worded to be anchor-distinct from the
// helper's "status reached neither ... nor its descendants", so a reader — or a
// gate — matching either phrase cannot match the other.
func cascadeWriteFailureResult(id string, fields []string, rootWritten bool, err error) kgtools.ToolResult {
	if !rootWritten {
		return rollupFailureResult(id, fields, "cascade write", err)
	}
	msg := fmt.Sprintf(
		"mutate(update): status reached %s but not its descendants: %v — re-issue this status update once the cause is cleared",
		id, err,
	)
	if len(fields) > 0 {
		msg += fmt.Sprintf(" — %s were also applied to %s", strings.Join(fields, ", "), id)
	}
	return errorResult(msg)
}

// cascadeBackendFailureResult reports a cascade failure on the TRACKER-BACKED
// arm, where two writes have already landed by the time the cascade runs.
//
// CRASH WINDOW, stated rather than implied. Neither preceding write is rolled
// back when the cascade fails, which is why this message names all three facts:
// the tracker write landed, the local root write landed, and the descendants were
// not reached. A bare "cascade failed" would be the silent-partial report the
// local arm's failure helper exists to prevent.
//
// The remedy it names is safe to follow because re-issuing the same status update
// is idempotent: the tracker treats a repeat as a no-op, the local forward
// rewrites the same value, and the cascade re-derives its target set from the tree
// as it then stands rather than from anything the failed attempt recorded.
func cascadeBackendFailureResult(backendName, id string, err error) kgtools.ToolResult {
	return errorResult(fmt.Sprintf(
		"mutate(update): backend %q + local update succeeded for %s, but the descendant cascade failed: %v"+
			"; live descendants still sit under it — re-issue this status update once the cause is cleared",
		backendName, id, err))
}

// cascadeToLiveDescendants is the tracker-backed arm's whole cascade in one call:
// walk the contains tree, refuse a truncated walk, partition, write the
// descendants and render the summary. It returns the summary to append to that
// arm's own success line.
//
// It writes NO root status — the caller already forwarded the root's own update —
// which is why it passes an empty rootStatus to the batch writer.
//
// WHY THE TRUNCATION REFUSAL BELONGS ON THIS ARM TOO. A clamped walk yields a
// partial subtree indistinguishable from a complete one, and under a terminal
// cascade a descendant clamped out of the walk stays LIVE under a dead
// container — reintroducing, through a silent partial read, exactly the phantom
// this cascade removes. Refusing is the only correct disposition; cascading what
// was seen and moving on would be a lane that fires forever on the same cause.
func cascadeToLiveDescendants(
	ctx context.Context,
	gc GraphCaller,
	a mutateArgs,
	cascadeStatus string,
) (string, error) {
	descs, structureEdges, truncated, terr := render.TraverseDescendantsWithEdges(ctx, gc, a.ID, kgtypes.EdgeKGContains, 16)
	if terr != nil {
		return "", terr
	}
	if truncated {
		return "", errRollupTraverseTruncated
	}
	ids, heldCriteria, heldQuestions, heldUnevaluated, heldOther := partitionRollupTargets(
		a.ID, descs, structureEdges, cascadeStatus)
	if _, werr := writeCascadeStatuses(ctx, gc, ids, "", cascadeStatus, newBundleID()); werr != nil {
		return "", werr
	}
	return cascadeSummary(cascadeStatus, ids, heldCriteria, heldQuestions, heldUnevaluated, heldOther), nil
}
