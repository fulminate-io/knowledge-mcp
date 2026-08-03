// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_parity_test.go is the drive-through parity harness: it runs every
// dispatch arm through the fake once per schema param and asserts the OBSERVED
// behavior matches the class the registry DECLARES. The sibling partition test
// proves the table is structurally complete; this proves it is true.
//
// Not parallel by construction: fakeGraphCaller accumulates call state on
// unsynchronised slices, so t.Parallel() would race it.

import (
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// selectionOnlyParams are consumed in a way no MutationPlan can show. `format`
// selects a RENDER path, and `language`/`link_graph` route or re-route rather
// than landing a value, so their rows assert the arm was still selected and the
// call behaved, never that a literal appears in the write. Asserting a literal
// for these would fail against correct work.
//
// verified_quote and cited_range are members for a related but distinct reason:
// they are consumed by an UPSTREAM GATE rather than by the arm. The negation
// gate reads them before dispatch and the write then ignores them entirely —
// they are proof-of-work, never persisted (intercept_mutate.go:61-69) — so no
// MutationPlan can ever show them. `graph` is already a member on the same
// reachability-discriminant grounds.
var selectionOnlyParams = map[string]bool{
	"format": true, "operation": true, "type": true, "graph": true,
	"id": true, "ids": true, "language": true, "link_graph": true,
	"expand_to_descendants": true, "concludes": true,
	"verified_quote": true, "cited_range": true,
}

// parityLastValidated is the probe timestamp; parityLastValidatedNanos is what
// the link arms actually persist after parseLastValidatedNanos.
const (
	parityLastValidated      = "2031-03-04T05:06:07Z"
	parityLastValidatedNanos = "1930367167000000000"
)

// parityContains folds case before comparing. The linkage graph canonicalizes
// relationship casing on the way to the EdgeSpec, so a case-sensitive match
// would report a correctly-routed param as missing.
func parityContains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// parityProbe returns the value to inject for (arm, param) and the distinctive
// string the write must (or must not) contain. An empty distinctive means the
// row is observed by ARM SELECTION only.
//
// The governing rule: a probe must be both ARM-PRESERVING and TYPE-VALID. Type-
// valid means valid for the param's declared JSON type on BOTH the client and
// the engine mutateArgs structs — a probe that fails either unmarshal never
// reaches the arm and measures nothing.
func parityProbe(param string, prop kgtools.Property, fx parityFixture) (value any, distinctive string) {
	if v, ok := fx.discriminants[param]; ok {
		return v, ""
	}
	switch param {
	case "last_validated":
		// Must parse as RFC3339 or compileMutateByIDLinkUnlink bails before the arm.
		// The arm stores the PARSED NANOS, so that is what the write can show —
		// matching on the source timestamp would fail against correct work.
		return parityLastValidated, parityLastValidatedNanos
	case "weight":
		return 7.25, "7.25"
	case "confidence":
		return 0.875, "0.875"
	case "metadata":
		return map[string]any{"probe-metadata-key": "probe-metadata"}, "probe-metadata"
	case "references":
		return []any{map[string]any{"url": "https://probe-references.invalid", "title": "probe-references"}},
			"probe-references"
	case "nodes":
		return []any{map[string]any{"type": "finding", "name": "probe-nodes", "summary": "probe-nodes summary"}},
			"probe-nodes"
	case "edges":
		return []any{map[string]any{"from_idx": 0, "to_idx": -1, "to_id": "probe-edges", "type": "relates-to"}},
			"probe-edges"
	case "items":
		return []any{map[string]any{"id": paritySeedLocalA, "summary": "probe-items"}}, "probe-items"
	case "updates":
		return []any{map[string]any{
			"id": paritySeedLocalA, "metadata": map[string]any{"probe-updates-key": "probe-updates"},
		}}, "probe-updates"
	case "question_id", "thought_parent", "branches_from", "ticket_id", "from", "to", "step_id":
		// Derivation / edge-endpoint params: probe with a SEEDED id so the derived
		// edge actually resolves. An unseeded id is dropped with a warning, which
		// would make a consumed row fail against correct routing.
		return paritySeedLocalB, paritySeedLocalB
	case "findings":
		return paritySeedLocalA, paritySeedLocalA
	case "links", "charge_evidence":
		return []any{paritySeedLocalB}, paritySeedLocalB
	case "session":
		return "probe-session", "probe-session"
	case "polarity":
		return "positive", "positive"
	}
	switch prop.Type {
	case "boolean":
		return true, ""
	case "number":
		return 3.5, "3.5"
	case "array":
		return []any{"probe-" + param}, "probe-" + param
	case "object":
		return map[string]any{"probe-" + param + "-key": "probe-" + param}, "probe-" + param
	}
	return "probe-" + param, "probe-" + param
}

// parityWriteText renders every client-side write the drive captured into one
// searchable blob. Covers NodeBodies, SetFields, SetMetadata, EdgeSpec and
// Target uniformly, so one observation rule serves create, update and link arms.
func parityWriteText(t *testing.T, fc *fakeGraphCaller) string {
	t.Helper()
	var sb strings.Builder
	for _, m := range fc.execMutations {
		b, err := json.Marshal(m)
		require.NoError(t, err)
		sb.Write(b)
	}
	for _, r := range fc.execRequests {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		sb.Write(b)
	}
	return sb.String()
}

// parityCompiledText compiles the payload the way the engine dispatch would and
// renders the resulting plan (including its Target, where the routing-class
// params land). Used for the arms that DECLINE — there is no client write to
// observe, so the consumed assertion is made against the compiled plan.
func parityCompiledText(t *testing.T, payload []byte) string {
	t.Helper()
	req, ok := engine.Compile("mutate", json.RawMessage(payload))
	if !ok {
		return ""
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	return string(b)
}

// TestMutateArmParity_DeclaredClassMatchesObservedBehavior drives every
// (arm, param) cell and asserts the declared class is the observed one:
//
//	classConsumed            → the call behaves (claimed arms succeed, declining
//	                           arms decline) and the probe is observable in the
//	                           write, or the arm is still selected for the params
//	                           no plan can show;
//	classRejected            → IsError, the error names the param, ZERO writes;
//	classDeliberatelyIgnored → the call behaves AND the probe appears nowhere.
//
// KNOWN ASYMMETRY IN WHAT THIS PROVES — read before trusting a green run.
// The three classes are NOT equally falsifiable, because the gate that produces
// the observed behavior reads the same table this asserts against:
//
//   - classConsumed and classDeliberatelyIgnored rows are REAL evidence. The
//     gate lets the param through and the assertion then looks for its effect in
//     the actual write, so declaring a param consumed that no code routes fails
//     here (verified: moving `scope` into armCreateFinding's consumed set makes
//     this test red while the partition test stays green).
//   - classRejected rows are TAUTOLOGICAL with respect to the gate. Moving a
//     param into a rejected set makes the gate reject it, which is exactly what
//     the row then asserts, so a mis-declared rejection passes (verified the
//     same way). What those rows still pin is the CONTRACT SHAPE — the error
//     names the field and no write precedes it — not that rejecting was the
//     right call for that param.
//
// SECOND BLIND SPOT, on the consumed side: a cell whose param is in
// selectionOnlyParams is asserted only as "the arm was still selected and
// behaved", which is equally true of a consumed cell and an ignored one. Those
// rows therefore cannot tell classConsumed from classDeliberatelyIgnored — they
// pin selection, not routing. Any cell that must distinguish the two needs a
// named behavior test, not this harness.
//
// So this harness catches the silent-drop direction, which is the defect class
// the accounting exists for, and does not catch an over-broad rejection. An
// over-broad rejection is caught instead by the per-arm behavior tests that
// drive a REAL payload through each arm and assert it still succeeds.
//
// NO-ACCOUNTING ARMS are asserted differently, and the exemption is load-bearing
// rather than a convenience: an arm that declares the WHOLE schema consumed with
// empty rejected and deliberatelyIgnored sets writes NOTHING client-side (it
// declines and the server owns the write), so "the probe is observable in the
// captured write" is unsatisfiable for it by construction. Its per-cell
// assertion is that the call DECLINES with zero client writes — the gate did not
// reject. Keyed off the armSpec SHAPE, so a future no-accounting arm inherits it.
func TestMutateArmParity_DeclaredClassMatchesObservedBehavior(t *testing.T) {
	schema := mutateProperties()
	require.NotEmpty(t, schema, "mutateProperties() must declare params")
	fixtures := parityFixtures()

	evaluated := 0
	for _, cell := range parityCells() {
		fx, ok := fixtures[armID(cell.arm)]
		require.Truef(t, ok, "arm %q has no parity fixture — every arm must be driven", cell.arm)
		evaluated++
		t.Run(cell.arm+"/"+cell.param, func(t *testing.T) {
			parityCell(t, armID(cell.arm), fx, cell.param, schema[cell.param])
		})
	}
	assert.Equal(t, len(mutateArmRegistry)*len(schema), evaluated,
		"every (arm, param) cell must be driven — a skipped cell is an unasserted claim")
}

// parityCellID is one (arm, param) pair the harness drives.
type parityCellID struct{ arm, param string }

// parityCells enumerates the full cell grid from the TWO LIVE SOURCES — the arm
// registry and the mutate schema — in a deterministic order. Both the harness
// and the coverage test iterate this same function, so the count one asserts is
// the count the other actually drove.
func parityCells() []parityCellID {
	arms := make([]string, 0, len(mutateArmRegistry))
	for arm := range mutateArmRegistry {
		arms = append(arms, string(arm))
	}
	sort.Strings(arms)
	schema := mutateProperties()
	params := make([]string, 0, len(schema))
	for p := range schema {
		params = append(params, p)
	}
	sort.Strings(params)

	cells := make([]parityCellID, 0, len(arms)*len(params))
	for _, arm := range arms {
		for _, param := range params {
			cells = append(cells, parityCellID{arm: arm, param: param})
		}
	}
	return cells
}

// TestMutateArmParity_CoversEveryArmAndParam is the NO-SKIP guard for its
// sibling: the harness could pass vacuously by driving a subset, so the cell
// grid is counted against both live sources independently. It also pins that
// every arm has a drive fixture and every cell is classified — a fixture-less
// arm or an unclassified cell would otherwise be silently undriven.
func TestMutateArmParity_CoversEveryArmAndParam(t *testing.T) {
	schema := mutateProperties()
	require.NotEmpty(t, schema, "mutateProperties() must declare params")
	require.NotEmpty(t, mutateArmRegistry, "the arm registry must declare arms")

	cells := parityCells()
	assert.Len(t, cells, len(mutateArmRegistry)*len(schema),
		"the cell grid must be exactly every arm times every schema param")

	fixtures := parityFixtures()
	seen := map[parityCellID]bool{}
	armsSeen := map[string]bool{}
	paramsSeen := map[string]bool{}
	for _, cell := range cells {
		assert.Falsef(t, seen[cell], "cell %s/%s enumerated twice", cell.arm, cell.param)
		seen[cell] = true
		armsSeen[cell.arm] = true
		paramsSeen[cell.param] = true

		_, hasFixture := fixtures[armID(cell.arm)]
		assert.Truef(t, hasFixture, "arm %q has no drive fixture, so its cells cannot be evaluated", cell.arm)
		_, classified := paramClassFor(armID(cell.arm), cell.param)
		assert.Truef(t, classified, "cell %s/%s is unclassified", cell.arm, cell.param)
	}
	assert.Len(t, armsSeen, len(mutateArmRegistry), "every registered arm must appear in the grid")
	assert.Len(t, paramsSeen, len(schema), "every schema param must appear in the grid")
}

// parityCell drives one (arm, param) cell and asserts its declared class.
func parityCell(t *testing.T, arm armID, fx parityFixture, param string, prop kgtools.Property) {
	t.Helper()
	class, classified := paramClassFor(arm, param)
	require.Truef(t, classified, "param %q is unclassified for arm %q", param, arm)

	value, distinctive := parityProbe(param, prop, fx)
	payload := map[string]any{}
	// A per-param base override lets an OPERATION-POLYMORPHIC arm (whose consumed
	// set is a union across operations) and the per-type update router (whose
	// per-node-type refinement rejects a param the arm itself routes) be driven in
	// the shape that actually reads the param. Without it those rows would assert
	// against a shape that legitimately never consumes it.
	base := fx.base
	if override, ok := fx.paramBase[param]; ok {
		base = override
	}
	maps.Copy(payload, base)
	payload[param] = value
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	fc := paritySeed(t)
	handled, res := parityDrive(fx, fc, raw)

	if fx.noAccounting {
		assert.Falsef(t, handled,
			"no-accounting arm %q must DECLINE so the server owns the write (param %q)", arm, param)
		assert.Emptyf(t, fc.execMutations,
			"no-accounting arm %q must issue zero client writes (param %q)", arm, param)
		return
	}

	// A DISCRIMINANT row is a selection row whatever its declared class: the param
	// is probed arm-preservingly (an arbitrary value would deselect the arm), so
	// the only thing it can honestly assert is that the arm is STILL SELECTED and
	// still behaves. A silent re-route surfaces here rather than as a passing row
	// that measured a different arm.
	if _, isDiscriminant := fx.discriminants[param]; isDiscriminant {
		parityAssertBehaved(t, arm, fx, param, handled, res)
		return
	}

	switch class {
	case classRejected:
		require.Truef(t, handled, "a rejected param must be CLAIMED, not fall through (%s/%s)", arm, param)
		require.Truef(t, res.IsError, "param %q must be rejected by arm %q", param, arm)
		assert.Containsf(t, toolResultText(res), param,
			"arm %q rejected %q without naming the field", arm, param)
		assert.Emptyf(t, fc.execMutations,
			"arm %q wrote before rejecting %q — the reject must precede every write", arm, param)
	case classConsumed:
		parityAssertBehaved(t, arm, fx, param, handled, res)
		if distinctive == "" || selectionOnlyParams[param] {
			return
		}
		observed := parityWriteText(t, fc)
		if fx.declines {
			observed = parityCompiledText(t, raw)
		}
		assert.Truef(t, parityContains(observed, distinctive),
			"arm %q declares %q CONSUMED but the probe %q is nowhere in the write: %s",
			arm, param, distinctive, observed)
	case classDeliberatelyIgnored:
		parityAssertBehaved(t, arm, fx, param, handled, res)
		if distinctive == "" || selectionOnlyParams[param] {
			return
		}
		assert.Falsef(t, parityContains(parityWriteText(t, fc), distinctive),
			"arm %q declares %q deliberately IGNORED but the probe %q reached the write", arm, param, distinctive)
	}
}

// parityAssertBehaved asserts the arm was still selected: a claimed arm returns
// handled with a non-error result, a declining arm returns handled==false. A
// silent re-route to a different arm shows up here rather than as a passing row
// that measured the wrong arm.
func parityAssertBehaved(t *testing.T, arm armID, fx parityFixture, param string, handled bool, res kgtools.ToolResult) {
	t.Helper()
	if fx.declines {
		assert.Falsef(t, handled, "arm %q must decline to the engine (param %q)", arm, param)
		return
	}
	require.Truef(t, handled, "arm %q must claim the call (param %q)", arm, param)
	require.Falsef(t, res.IsError, "arm %q errored on consumed/ignored %q: %s", arm, param, toolResultText(res))
}
