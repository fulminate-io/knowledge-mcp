// SPDX-License-Identifier: Apache-2.0

package tools

// create_plan_sections.go holds the sectioned-plan half of create_plan: the
// exactly-one-of-two shape gate and the per-section validator.
//
// IT IS A SEPARATE FILE because intercept_create_plan.go is already near the
// repo's 500-line per-file budget, and because the sectioned shape's rules read
// as one set.
//
// EVERY REFUSAL HERE NAMES THE OFFENDING KEY AND THE VALID SET, in the caller's
// own indexed spelling (`sections[1].position`), and refuses PRE-WRITE. Nothing
// is defaulted, coerced or dropped: the house rule is "bad input always errors",
// and the model this file copies states it as REJECT, NEVER AUTO-COMPLETE —
// growing the missing value for the caller silently rewrites what they asked
// for and has no principled stopping point.
//
// THE WALK IS IN THE CALLER'S OWN ORDER, so a payload with two offenders always
// names the same one first. A validator that reported whichever offender a map
// iteration reached first would hand a caller a different error on every retry
// of the same payload.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// createPlanSection is one sections[] entry off the wire. Position is a POINTER
// because 0 is a legal position: a plain int could not tell "position zero" from
// "no position supplied", which is exactly the distinction the
// all-or-none-positions rule turns on.
type createPlanSection struct {
	Name     string `json:"name"`
	Body     string `json:"body"`
	Summary  string `json:"summary"`
	Position *int   `json:"position"`
}

// validatePlanShape enforces EXACTLY ONE of phases or sections.
//
// Both shapes at once has no defensible reading — the two build different trees
// under one root, one chained by depends-on and one ordered by position, and a
// depends-on chain overrides positions — so it is refused rather than merged.
// Neither shape keeps the pre-existing zero-phase refusal, reworded to name both
// shapes now that there are two.
func validatePlanShape(a *createPlanArgs) error {
	switch {
	case len(a.Phases) > 0 && len(a.Sections) > 0:
		return fmt.Errorf(
			"create_plan: supply exactly one of phases or sections, not both — "+
				"got %d phases and %d sections. A phase plan chains its children with depends-on edges "+
				"and a sectioned plan orders its children by position; a chain overrides positions, "+
				"so the two shapes cannot share one root",
			len(a.Phases), len(a.Sections))
	case len(a.Phases) == 0 && len(a.Sections) == 0:
		return fmt.Errorf("create_plan: at least one phase is required, or supply sections instead for a chunked plan (exactly one of phases or sections)")
	}
	return nil
}

// validatePlanSections validates and clamps every section in the caller's order,
// then checks the position set as a whole. Author clamps mutate a in place (the
// slice header is shared) so the clamped summaries flow into
// buildPlanArgsFromWire, the same contract validatePlanSummaries has.
//
// ITERATION IS BY INDEX, deliberately, for the reason validateCriteria states: a
// range-value loop clamps a COPY and ships the unclamped summary into
// PersistBatch, passing every local assertion on the way.
func validatePlanSections(sections []createPlanSection) (warnings []string, err error) {
	for i := range sections {
		if sections[i].Name == "" {
			return nil, fmt.Errorf("create_plan: sections[%d].name is required — every section is named, and the name is what the tree and the section index show", i)
		}
		if sections[i].Body == "" {
			return nil, fmt.Errorf("create_plan: sections[%d].body is required — the body is the section's whole point; the plan root carries none of it", i)
		}
		clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("sections[%d].summary", i), sections[i].Summary)
		if cerr != nil {
			return nil, cerr
		}
		sections[i].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
	}
	if perr := validateSectionPositions(sections); perr != nil {
		return nil, perr
	}
	return warnings, nil
}

// validateSectionPositions applies the all-or-none and uniqueness rules to an
// explicit position set.
//
// ALL OR NONE: a partially positioned list has no defensible reading. Filling the
// gaps from array order would let a caller's explicit 5 and an implied 1 collide
// silently; refusing the whole list says which entry is missing one.
//
// UNIQUE, BUT NOT CONTIGUOUS. Two sections claiming one position is an ambiguity
// with no answer, so it errors naming both. A GAP is legal: deleting a section
// leaves a hole, and closing it would mean rewriting every later section's
// position — the whole-plan rewrite the chunked shape exists to remove. Ordering
// is ascending by key, which a gap does not disturb.
func validateSectionPositions(sections []createPlanSection) error {
	supplied := 0
	for i := range sections {
		if sections[i].Position != nil {
			supplied++
		}
	}
	if supplied == 0 {
		return nil // array order governs; positions run 0..N-1.
	}
	seen := map[int]int{}
	for i := range sections {
		if sections[i].Position == nil {
			return fmt.Errorf(
				"create_plan: sections[%d].position is missing while %d of %d sections supply one — "+
					"positions are all-or-none: either every section supplies one, or none does and the array order governs",
				i, supplied, len(sections))
		}
		pos := *sections[i].Position
		if pos < 0 {
			return fmt.Errorf("create_plan: sections[%d].position is %d — a position is a zero-based index and cannot be negative", i, pos)
		}
		if prev, dup := seen[pos]; dup {
			return fmt.Errorf(
				"create_plan: sections[%d].position is %d, which sections[%d] already claims — "+
					"two sections cannot hold one position; gaps in the sequence are allowed, duplicates are not",
				i, pos, prev)
		}
		seen[pos] = i
	}
	return nil
}

// rejectUndeclaredSectionKeys refuses a sections[] entry carrying a key the
// schema does not declare.
//
// IT EXISTS BECAUSE rejectUndeclaredParams IS TOP-LEVEL ONLY: the decode into
// createPlanArgs discards any nested key createPlanSection has no field for, so
// without this scan a typo'd `overview` on a section would vanish into a
// successful create and the author would find out from the stored node. The
// nested-scan precedent is the swallowed-param gate's own findSwallowedParamValue.
//
// IT SCANS THE RAW PAYLOAD rather than the decoded struct, because by the time
// the struct exists the undeclared key is already gone. A payload that does not
// parse is the caller's own decode error to report, not this scan's, so a failed
// unmarshal stands aside.
//
// THE DECLARED SET IS READ OFF THE SCHEMA, never hand-listed here. One list, in
// the tool definition the caller is shown, so the guard and the documentation
// cannot disagree about what a section accepts — the rule its sibling guard on
// update_batch's items[] states and follows.
func rejectUndeclaredSectionKeys(raw json.RawMessage) error {
	var payload struct {
		Sections []map[string]json.RawMessage `json:"sections"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	declared := declaredSectionKeys()
	if len(declared) == 0 {
		// The schema lookup found no section shape. That is a defect in this
		// guard's premise rather than a clean payload, and standing aside
		// silently would make it permanently inert while still looking like a
		// live gate.
		return fmt.Errorf("create_plan: internal — the sections[] schema declares no keys, so the undeclared-key guard cannot run")
	}
	for i, entry := range payload.Sections {
		// The walk is over the SCHEMA's declared key order rather than the map's,
		// so a section carrying two undeclared keys always names the same one
		// first — a map range would pick a different one on every run.
		for _, key := range undeclaredKeysInOrder(entry, declared) {
			return fmt.Errorf(
				"create_plan: sections[%d] carries the undeclared key %q — a section accepts %s. "+
					"Nested keys are not covered by the top-level undeclared-param check, so an unrecognized one would otherwise be dropped silently",
				i, key, strings.Join(sortedDeclaredKeys(declared), ", "))
		}
	}
	return nil
}

// declaredSectionKeys reads a sections[] entry's declared keys off the create_plan
// tool definition, so the guard and the schema the caller is shown are one list
// rather than two that can drift.
func declaredSectionKeys() map[string]bool {
	sections, ok := CreatePlanToolDef().InputSchema.Properties["sections"]
	if !ok || sections.Items == nil {
		return nil
	}
	out := make(map[string]bool, len(sections.Items.Properties))
	for key := range sections.Items.Properties {
		out[key] = true
	}
	return out
}

// undeclaredKeysInOrder returns the entry's undeclared keys in a DETERMINISTIC
// order (sorted), so the same payload always names the same offending key first.
// A Go map range is randomized, which would make the message vary run to run for
// one unchanged input.
func undeclaredKeysInOrder(entry map[string]json.RawMessage, declared map[string]bool) []string {
	var out []string
	for key := range entry {
		if !declared[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
