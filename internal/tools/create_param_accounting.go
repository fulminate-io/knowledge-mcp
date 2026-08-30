// SPDX-License-Identifier: Apache-2.0

package tools

// create_param_accounting.go carries the tool-generic undeclared-key rejection
// the create_* family runs at the head of every intercept.
//
// It lives in its own file rather than in mutate_param_accounting.go — the step's
// stated first choice — solely because that file stands at 472 lines and the
// helper would carry it past the 500-line cap. Nothing about the mechanism is
// create-specific: the comparison, the key-set reader and the valid-set renderer
// all come from the mutate accounting file, and the mutate gate itself now calls
// through here.
//
// WHY THE FIVE CREATE TOOLS NEEDED THIS AT ALL. Each intercept decodes the
// caller's payload into its own args struct with a plain json.Unmarshal, which
// discards any key the struct has no field for. So a caller who mistyped a param,
// or supplied one that belongs to a sibling tool, got a SUCCESS with that param
// silently gone — a create that did less than it was asked to, reported as if it
// had done all of it.
//
// SCOPE IS TOP-LEVEL ONLY, and here that is load-bearing rather than a
// simplification. create_plan's phases[]/steps[]/criteria[] and create_research's
// questions[] carry sub-object keys that are not top-level params — file_paths is
// itself one of them, declared by planStepItems() — so a check that descended
// would reject every structured create call. suppliedMutateParams never descends,
// which puts nested keys structurally out of reach rather than filtered out.

import (
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// rejectUndeclaredParams returns an error when raw carries a top-level key the
// tool's own schema does not declare, naming the offending key and enumerating
// the valid set. A nil return means every supplied key is declared.
//
// DECLARED IS THE TOOL'S LIVE PROPERTIES MAP, never a hardcoded list: callers
// pass their own ToolDef().InputSchema.Properties, so a param added to a schema
// is accepted from that moment with no second edit here. A frozen copy would rot
// on the next schema addition — the exact drift the parity test exists to catch.
//
// FLEXMAP NAMES THE TOOL'S FLEX-OPEN CARRIER, and it selects between the two
// message shapes already live in this package rather than introducing a third.
// mutate passes "metadata" and keeps its did-you-mean pointer byte-for-byte (an
// assertion in mutate_unknown_param_test.go depends on it); the five create tools
// declare no such map and pass "", which renders the shorter form the query gate
// uses. Weakening that assertion to fit one generic message would have been the
// fake-green this gate exists to prevent.
//
// Reuses suppliedMutateParams for the key set and inherits BOTH of its contracts
// unchanged:
//
//   - FAIL CLOSED on a nil key set. An unreadable payload means accounting never
//     ran, and passing everything through in that state is the silent hole this
//     closes.
//   - EMPTY IS ABSENT. A key whose value is null, "", 0, false, {} or [] is not
//     reported: every downstream reader already treats an empty scalar as
//     unsupplied, so an empty value cannot be a silent drop.
//
// ONE NARROW SUPERSESSION OF THE FAIL-CLOSED RULE, for the WHOLE-PAYLOAD case
// only. An absent or empty payload carries zero supplied keys and is ACCEPTED
// here, because a no-arg call is a legal, documented shape across the tool
// surface: help() with no topic is the documented way to list topics, and the
// propagate handler explicitly guards on a non-empty
// payload. Without this, wiring the rejection across those tools would refuse
// every no-arg call. A NON-EMPTY payload that does not parse still fails closed,
// and the empty-is-absent rule for individual VALUES is untouched.
//
// The guard sits at THIS function's entry rather than inside
// suppliedMutateParams, which has other callers across the mutate accounting —
// changing its nil contract would alter all of them. suppliedMutateParams is
// left byte-identical.
func rejectUndeclaredParams(tool, flexMap string, declared map[string]kgtools.Property, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	supplied := suppliedMutateParams(raw)
	if supplied == nil {
		return fmt.Errorf("%s: param accounting could not read the supplied params", tool)
	}
	unknown := unknownTopLevelParams(supplied, declared)
	if len(unknown) == 0 {
		return nil
	}
	// unknown is sorted, so the same payload always names the same param first —
	// the determinism rule the rejected-set loop states.
	if flexMap != "" {
		return fmt.Errorf(
			"%s: unknown parameter %q — did you mean %s:{%q: ...}? Valid top-level parameters: %s",
			tool, unknown[0], flexMap, unknown[0], validTopLevelParams(declared),
		)
	}
	return fmt.Errorf(
		"%s: unknown parameter %q. Valid top-level parameters: %s",
		tool, unknown[0], validTopLevelParams(declared),
	)
}
