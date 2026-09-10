// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_polymorphic_hub_test.go is the STRUCTURAL guard the silent-drop
// class needed, and it exists because the drive-through parity harness cannot
// catch that class on its own.
//
// WHY THE PARITY HARNESS MISSES IT. Two dispatch arms are OPERATION-POLYMORPHIC:
// armGraphPassthrough serves create / create_batch / update / delete, and
// armNonKnowledgeFallthrough serves effectively every other operation. Their
// consumed sets are UNIONS over those operations, and the parity harness drives
// ONE cell per (arm, param) — so a param declared consumed is proven consumed by
// whichever single operation the fixture happens to drive, and every OTHER
// operation of the same arm is unasserted. `source_hub` was declared consumed on
// both arms and was in fact dropped on four of the operations they carry.
//
// WHAT THIS ASSERTS INSTEAD. Every declared mutate operation is driven on
// graph:"practice" carrying source_hub, and each one's DISPOSITION is declared
// here and compared against what the drive observes. The four dispositions are
// the only honest outcomes: the hub reaches the write, a guard read applies it,
// the call is refused, or the call reaches no write at all. "Consumed and then
// absent from the write, with no read and no refusal" is none of them, and it is
// the shape the table cannot express — so a param silently dropped on a new
// operation fails here rather than being discovered one cell per review round.
//
// IT IS NOT A RESTATEMENT OF THE PRODUCTION TABLE. The declared column is a
// specification written from the decided disposition; the observed column comes
// from driving the REAL intercept. A row that disagrees is a real disagreement,
// not a tautology, which is the property the arm registry's own rejected-class
// rows do not have.
//
// IT IS ONE DIMENSION OF TWO, AND ONLY ONE. Every row here fixes the graph at
// practice, so it says nothing about the same operation on another family — and
// that silence was itself a defect: off the practice family four of these
// operations consumed `source_hub` and reached nothing, and a checks create
// stamped a practice hub key and a sourced-from edge onto a checks node through
// the family-blind create lowering. mutate_family_arm_hub_test.go is the FAMILY x
// OPERATION grid that closes the second axis, and it READS the declared column
// below rather than restating it, so a practice cell cannot be relaxed in one
// file while the other still asserts it.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// hubDisposition is what one operation does with `source_hub`. Every field is a
// FACT the drive observes, not a label: a row states all four, so "the write went
// out without the hub" is as visible as "the hub was applied".
type hubDisposition struct {
	// refused: the call was claimed and errored, and nothing was written.
	refused bool
	// guardRead: a hub-resolving read was issued, so the param was APPLIED
	// client-side even though it does not ride the wire.
	guardRead bool
	// inPlan: the plan that would be applied carries the hub value.
	inPlan bool
	// noWrite: the call reaches no write at all — no client mutation, and the
	// payload does not compile — so there is no write for a param to be dropped
	// from.
	noWrite bool
}

// hubPolymorphicRow is one operation's declared disposition plus the payload that
// drives it. The payload names MEMBERS of the hub throughout, so a row measures
// the operation's handling of a well-formed hub-scoped call rather than a
// refusal it would have earned anyway.
type hubPolymorphicRow struct {
	arm      string
	targets  string
	declared hubDisposition
}

// hubPolymorphicRows is the census. Its keys must cover the whole live operation
// vocabulary — see TestMutateArmPolymorphicHub_CoversEveryDeclaredOperation —
// so a new mutate operation cannot ship without a decided source_hub cell.
func hubPolymorphicRows() map[string]hubPolymorphicRow {
	return map[string]hubPolymorphicRow{
		// armGraphPassthrough: the hub GROUPS on the two create shapes and is a
		// SELECTION axis on delete, so all three put it on the wire; only update
		// names existing targets for it to scope.
		//
		// EVERY CELL BELOW NOW CARRIES guardRead, and that is the round-6 contract
		// rather than a drift: the hub id is RESOLVED before any arm acts on it —
		// it must name a live `source` node — and that resolution is one read, on
		// every arm that takes a hub. The three arms that previously read nothing
		// (create, create_batch, delete) pay exactly that one; the arms that
		// already read fold the hub into the read they had, so none pays two.
		"create": {arm: "armGraphPassthrough", declared: hubDisposition{guardRead: true, inPlan: true},
			targets: `"type":"pattern","name":"probe-name","summary":"probe summary"`},
		"create_batch": {arm: "armGraphPassthrough", declared: hubDisposition{guardRead: true, inPlan: true},
			targets: `"nodes":[{"type":"pattern","name":"probe-name","summary":"probe summary"}]`},
		"update": {arm: "armGraphPassthrough", declared: hubDisposition{guardRead: true},
			targets: `"id":"` + hubTargetMemberA + `","description":"probe-description"`},
		// The by-hub delete names NO other target: the hub is its selection axis,
		// and a delete carrying ids as well is refused as two selections.
		"delete": {arm: "armGraphPassthrough", declared: hubDisposition{guardRead: true, inPlan: true},
			targets: ""},

		// armNonKnowledgeFallthrough: every one of these names existing nodes, so
		// the hub scopes a client-side resolve. upsert is the one that does both —
		// its update half keeps the membership key on the body it writes.
		"update_batch": {arm: "armNonKnowledgeFallthrough", declared: hubDisposition{guardRead: true},
			targets: `"items":[{"id":"` + hubTargetMemberA + `","summary":"probe summary"}]`},
		"bulk_update_metadata": {arm: "armNonKnowledgeFallthrough", declared: hubDisposition{guardRead: true},
			targets: `"updates":[{"id":"` + hubTargetMemberA + `","metadata":{"k":"v"}}]`},
		"upsert": {arm: "armNonKnowledgeFallthrough",
			declared: hubDisposition{guardRead: true, inPlan: true},
			targets:  `"id":"` + hubTargetMemberA + `","type":"pattern","name":"n","summary":"s"`},
		"unlink": {arm: "armNonKnowledgeFallthrough", declared: hubDisposition{guardRead: true},
			targets: `"from":"` + hubTargetMemberA + `","to":"` + hubTargetMemberB + `","relationship":"contains"`},

		// The two operations neither polymorphic arm carries, included so the
		// census covers the whole vocabulary rather than the part that was easy.
		// A practice link is claimed upstream by the intra-practice arm, which
		// scopes its endpoints the same way; answer is denied on a foreign graph
		// and reaches no write for the hub to be dropped from.
		"link": {arm: "intraPracticeLinkArm", declared: hubDisposition{guardRead: true},
			targets: `"from":"` + hubTargetMemberA + `","to":"` + hubTargetMemberB + `","relationship":"contains"`},
		"answer": {arm: "armNonKnowledgeFallthrough", declared: hubDisposition{noWrite: true},
			targets: `"id":"` + hubTargetMemberA + `","conclusion":"probe"`},
	}
}

// observeHubDisposition drives ONE payload and reports the four facts. It takes a
// payload rather than an operation because the FAMILY grid in
// mutate_family_arm_hub_test.go reads its cells on this same instrument: two
// observers would let a practice cell and a foreign cell disagree about what
// "on the plan" means, which is the disagreement the grid exists to detect.
func observeHubDisposition(t *testing.T, payload string) (hubDisposition, string) {
	t.Helper()
	fc := practiceHubTargetFake(t)
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(payload),
	})

	assertHubDriveCannotSplit(t, fc, payload, res)
	got := hubDisposition{
		refused:   handled && res.IsError,
		guardRead: len(hubEndpointReads(fc)) > 0,
	}
	// WHAT "APPLIED" MEANS, and the one case where compiling would LIE. A claimed
	// call's write is the plan the client issued. A DECLINED call issues none, so
	// the plan the engine would compile downstream is the honest stand-in. A
	// REFUSED call is neither: it is never passed on, so compiling its payload
	// would report a hub sitting on a plan nothing will ever apply — and read as
	// "the param reached the write" on exactly the calls where it reached nothing.
	var applied string
	switch {
	case len(fc.execMutations) > 0:
		applied = planText(t, fc.execMutations[len(fc.execMutations)-1])
	case got.refused:
	default:
		req, ok := engine.Compile("mutate", json.RawMessage(payload))
		if ok {
			b, err := json.Marshal(req.GetMutation())
			require.NoError(t, err)
			applied = string(b)
		}
	}
	got.noWrite = applied == "" && !got.refused
	got.inPlan = strings.Contains(applied, hubEndpointsHubA)
	return got, toolResultText(res)
}

// TestMutateArmPolymorphicHub_EveryOperationHasADisposition is the guard itself.
func TestMutateArmPolymorphicHub_EveryOperationHasADisposition(t *testing.T) {
	for operation, row := range hubPolymorphicRows() {
		t.Run(operation, func(t *testing.T) {
			got, body := observeHubDisposition(t, hubTargetPayload(operation, hubEndpointsHubA, row.targets))
			assert.Equalf(t, row.declared, got,
				"operation %q on arm %s: source_hub's observed disposition is not the declared one (result: %s)",
				operation, row.arm, body)
			// THE CLASS ASSERTION, stated separately from the row so it survives a
			// future edit to the table: at least one of the four facts must hold.
			// All-false is precisely "consumed and silently dropped".
			assert.Truef(t, got.refused || got.guardRead || got.inPlan || got.noWrite,
				"operation %q consumes source_hub and does nothing with it: no refusal, no guard read, "+
					"not on the plan, and a write went out anyway", operation)
		})
	}
}

// TestMutateArmPolymorphicHub_CoversEveryDeclaredOperation is the NO-SKIP guard
// for its sibling: the census above could pass vacuously by declaring only the
// operations that already behave, so its key set is counted against the LIVE
// schema vocabulary. A new mutate operation ships with a decided source_hub cell
// or it fails here.
func TestMutateArmPolymorphicHub_CoversEveryDeclaredOperation(t *testing.T) {
	require.NotEmpty(t, mutateDeclaredOperations, "the mutate schema declares no operations — the census is vacuous")
	rows := hubPolymorphicRows()
	for _, op := range mutateDeclaredOperations {
		_, declared := rows[op]
		assert.Truef(t, declared,
			"mutate operation %q has no declared source_hub disposition — an operation with no cell is the "+
				"silent drop this census exists to prevent", op)
	}
	assert.Len(t, rows, len(mutateDeclaredOperations),
		"the census must be exactly the declared operation vocabulary, no more and no fewer")
}
