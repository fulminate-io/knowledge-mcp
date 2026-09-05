// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_update_batch_items.go holds the pre-write guard on update_batch's
// items[] shape.
//
// THE DEFECT IT CLOSES. json.Unmarshal discards any key in an items[] entry that
// engine.batchItem has no field for, so such an entry compiles to the ID ALONE
// and the call RETURNS SUCCESS having written none of what the caller asked for.
// The caller is told the write happened; the node is unchanged. That silent drop
// is exactly what the house rule "bad input always errors" forbids, and the
// caller finds out later from the stored node, if at all.
//
// THE KEY THAT PROMPTED IT IS NO LONGER IN THE CLASS, and this note records the
// reversal rather than leaving a stale rationale standing. `description` was the
// reported case and was originally REFUSED here, because carrying it meant a
// field on the proto UpdateItem and a wire shape is the user's to approve. That
// approval was given: UpdateItem carries description at field 8,
// engine.batchItem decodes it, the server applies it and re-indexes on it, and
// the items[] schema declares it. So description is an ACCEPTED key now and this
// guard admits it automatically — the declared set is read off the schema rather
// than hand-listed here, so widening the schema was the only edit it needed.
//
// WHAT REMAINS, AND WHY THIS GUARD DOES. Every OTHER unrecognized key is still
// dropped at decode, so the class is intact and only its most famous member left
// it. A reader deciding whether some future key should be refused or carried
// should note which way that one went: the answer was to widen the carrier, with
// the user's approval, not to keep refusing.
//
// WHY IT IS A PAYLOAD-SHAPE GUARD AT THIS SEAM. By the time anything typed
// exists the undeclared key is already gone, so the check must see the RAW
// payload — which is what this seam has. It must NOT add a rejectUndeclaredParams
// call: that function is TOP-LEVEL ONLY by construction, and the standing census
// counts per-operation-handler calls to it and fails on a new one.
//
// THE DECLARED SET IS READ OFF THE SCHEMA, never hand-listed here. One list, in
// the tool definition the caller is shown, so the guard and the documentation
// cannot disagree about what an item accepts.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// guardUpdateBatchItemKeys refuses an update_batch whose items[] carries a key
// the schema does not declare, naming the offending item and key.
//
// Deterministic first hit, both axes: items are walked in the CALLER'S own order,
// and one item's undeclared keys are sorted, so the same payload always names the
// same offender. A Go map range is randomized, which would otherwise vary the
// message run to run for one unchanged input.
//
// A payload that does not parse is the DISPATCHER'S error to report; the guard
// stands aside rather than preempting it with a duplicate.
func guardUpdateBatchItemKeys(raw json.RawMessage) error {
	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error
	}
	declared := declaredUpdateBatchItemKeys()
	if len(declared) == 0 {
		// The schema lookup found no item shape. That is a defect in this guard's
		// premise rather than a clean payload, and standing aside silently would
		// make it permanently inert while still looking like a live gate.
		return fmt.Errorf("mutate(update_batch): internal — the items[] schema declares no keys, so the undeclared-key guard cannot run")
	}
	for i, item := range payload.Items {
		var offenders []string
		for key := range item {
			if !declared[key] {
				offenders = append(offenders, key)
			}
		}
		if len(offenders) == 0 {
			continue
		}
		sort.Strings(offenders)
		return fmt.Errorf(
			"mutate(update_batch): items[%d] carries the undeclared key %q — an item accepts %s. "+
				"An undeclared key is DROPPED at decode, so accepting it would return success having written none of it",
			i, offenders[0], strings.Join(sortedDeclaredKeys(declared), ", "))
	}
	return nil
}

// declaredUpdateBatchItemKeys reads the items[] entry's declared keys off the
// mutate tool definition, so the guard and the schema the caller is shown are one
// list rather than two that can drift.
func declaredUpdateBatchItemKeys() map[string]bool {
	items, ok := MutateToolDef().InputSchema.Properties["items"]
	if !ok || items.Items == nil {
		return nil
	}
	out := make(map[string]bool, len(items.Items.Properties))
	for key := range items.Items.Properties {
		out[key] = true
	}
	return out
}

// sortedDeclaredKeys renders a key set in a fixed order for a refusal message.
func sortedDeclaredKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
