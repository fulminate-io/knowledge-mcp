// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_param_accounting.go makes it structurally impossible for a mutate
// dispatch arm to accept a caller-supplied param and return success without
// either consuming it or rejecting it pre-write with an error naming the field.
// Every arm declares which params it consumes, which it rejects, and which it
// deliberately ignores (with a justification); accountMutateParams enforces that
// declaration at the arm head, before any write.
//
// STRUCTURAL GAP, and how it is closed: the classification table is keyed on
// the mutate schema's declared params, so a param that is CONSUMED by an arm but
// absent from the schema has no cell in any arm's sets — it is neither routed
// nor rejected by declaration, only by accident.
//
// `supports` was the first live instance (the finding-create arm draws a
// supports edge from it), and the negation gate's `verified_quote` / `cited_range`
// were the second — read off a top-level key by InterceptNegationGate before any
// write, declared by neither tool. All three have since been CLOSED the only way
// that actually closes it: declaring the param in the schema. Declaration is what
// forces the issue, because the partition assertion then demands a cell on every
// arm — so the accounting became a statement rather than a coincidence.
//
// ONE LIVE INSTANCE REMAINS, and the class is held closed only as far as the walk
// reaches: TestNegationProofParams_NoUndeclaredWireField walks every json-tagged
// field on the mutateArgs and thinkArgs declared IN THIS PACKAGE and fails on any
// tag the owning tool's schema does not declare. That closes the class for the
// structs it walks and only those. The mutate payload has a SECOND verbatim decode
// site in package engine, whose own mutateArgs reads repo, account AND branch;
// repo and account are now declared, and branch remains a live instance — left
// undeclared by ruling, because overlay write semantics for mutate are unproven,
// not because nothing reads it.
//
// The shape that made the old ones dangerous is still worth stating because it is
// not the obvious one: suppliedMutateParams does not filter by schema, so an
// undeclared key DOES appear in its output. What kept those keys from being
// rejected is the other side — every arm's rejected set is authored from schema
// keys only, so a key with no cell can never be classified rejected.
//
// UNKNOWN TOP-LEVEL KEYS ARE REJECTED, and the two rejections are complementary
// rather than alternatives. A caller key that is in no arm's sets because it is
// not a mutate schema param AT ALL — a typo, or a field that belongs in the
// flex-open metadata map — is rejected with the valid set enumerated from
// mutateProperties(). The targeted per-arm rejection still runs FIRST, so a
// DECLARED-but-unrouted param keeps its specific message naming the arm that
// does not route it, which is more useful than a generic unknown-key error; the
// catch-all fires only for keys the schema does not declare. That ordering is
// the behavior, not an implementation detail — reversing it would mask every
// arm's reason text behind the generic form.
//
// The unknown-key check keys on the SCHEMA, not on the arm, which is what makes
// it one place instead of a rejection cell on each of the arms: a key the
// schema does not declare is unknown for every one of them. That one place is
// rejectUndeclaredParams in create_param_accounting.go — mutate calls it with its
// own properties map and with "metadata" as the flex-open carrier the
// did-you-mean pointer names, and the five create_* tools call the same helper
// with theirs. The message shape below is unchanged by that delegation.
//
// SCOPE IS TOP-LEVEL ONLY. Metadata map CONTENTS stay flex-open by design, and
// batch sub-object keys (nodes[]/items[]/updates[] entries) are not top-level —
// the check never descends.
//
// SECOND CONCERN, sharing the same seam: a payload-VALUE check on the generic
// arms. The classification above answers "is this param routed", which cannot
// answer "is this value usable" — and the batch ops write metadata with no
// criterion-specific handling, so a criterion command that asserts nothing about
// its own test selector would reach the graph through them unchecked. It rides
// here rather than in a parallel pre-dispatch hook because this is already the
// one seam every arm passes through with its verbatim payload in hand.

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// armID names one dispatch arm in the mutate decision tree.
type armID string

// paramClass is how one arm handles one schema param.
type paramClass int

const (
	// classConsumed — the arm demonstrably uses the param: as Execute-Target
	// routing, as a body write, as the discriminant that selects the arm, or as
	// the input to a derived value or side effect.
	classConsumed paramClass = iota
	// classRejected — the arm neither applies nor routes the param, so supplying
	// it is a caller error the gate reports pre-write.
	classRejected
	// classDeliberatelyIgnored — the arm does not use the param, and that is
	// correct rather than a defect. Every entry carries its justification.
	classDeliberatelyIgnored
)

// armSpec is one arm's complete param classification plus the identity needed to
// tie it back to source.
type armSpec struct {
	operation string
	handler   string
	consumed  map[string]bool
	rejected  map[string]bool
	// deliberatelyIgnored maps the param to its JUSTIFICATION, not to a bool.
	// Making the justification a data field rather than a nearby comment is what
	// lets a test assert every entry is justified without parsing comments — a
	// text scan cannot tell a justification from prose.
	deliberatelyIgnored map[string]string
	// rejectionReasons optionally overrides the generic rejection explanation for
	// a specific param, for the cases where "this path does not route it" is true
	// but unhelpful — a DERIVED param needs to tell the caller what to set
	// instead. Absent entry means the generic message.
	rejectionReasons map[string]string
	// redundantValues optionally narrows a REJECTED param's rejection from key
	// presence to the VALUE sent, naming the closed set of values the arm accepts
	// as valid-but-redundant — a caller restating a family the arm already pins.
	// Absent entry (the case for every arm but one) leaves the rejection keyed on
	// presence, unchanged. See param_accounting_redundant_values.go for the
	// convention and the selector-layer precedent it follows.
	redundantValues map[string][]string
	// rejectedSorted is the precomputed sorted key list of rejected, built once
	// at package initialization so the gate reports a deterministic first hit
	// without sorting on every call.
	rejectedSorted []string
}

// mutateArmRegistry is the complete per-arm param classification, assembled from
// the three sibling table files. The split is a file-length concern only: the
// registry is ONE object, so the classification the gate enforces and the
// classification the tests assert can never drift apart.
var mutateArmRegistry = map[armID]armSpec{}

// init assembles the registry from its three groups and precomputes each arm's
// sorted rejected-key slice. Package-level vars are initialized before init
// runs, so the groups are populated by the time this executes. The gate runs on
// every host-originated mutate call, so the ordering that makes its error
// deterministic is built once here rather than per call.
func init() {
	maps.Copy(mutateArmRegistry, createArmSpecs)
	maps.Copy(mutateArmRegistry, updateArmSpecs)
	maps.Copy(mutateArmRegistry, linkArmSpecs)
	for arm, spec := range mutateArmRegistry {
		sorted := make([]string, 0, len(spec.rejected))
		for key := range spec.rejected {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		spec.rejectedSorted = sorted
		mutateArmRegistry[arm] = spec
	}
}

// registryParamClass reports how reg's arm classifies param, and whether it is
// classified at all. It holds the classification rule for EVERY registry rather
// than for mutate's alone: the query surface has its own arm registry over the
// same armSpec shape, and a second copy of this switch would be two statements
// of one contract, drifting the moment either is edited.
//
// An unclassified (arm, param) pair returns ok=false; each registry's partition
// assertion is what turns that into a build-breaking failure, so a newly-added
// schema param cannot quietly land in no set.
func registryParamClass(reg map[armID]armSpec, arm armID, param string) (paramClass, bool) {
	spec, ok := reg[arm]
	if !ok {
		return classRejected, false
	}
	switch {
	case spec.consumed[param]:
		return classConsumed, true
	case spec.rejected[param]:
		return classRejected, true
	}
	if _, ignored := spec.deliberatelyIgnored[param]; ignored {
		return classDeliberatelyIgnored, true
	}
	return classRejected, false
}

// paramClassFor is registryParamClass bound to the mutate registry.
func paramClassFor(arm armID, param string) (paramClass, bool) {
	return registryParamClass(mutateArmRegistry, arm, param)
}

// accountMutateParams is the pre-write gate, and it runs four checks in order.
// First the swallowed-parameter check: a caller text field whose value carries
// that field's own closing tag followed by the swallowed remainder of the call,
// which means the tool call was mis-serialized and an unknown number of
// parameters reached this tool as ABSENT (swallowed_param_gate.go). Then the
// classification check: an error when the caller supplied any param this arm
// classifies as rejected, naming the field, so the call fails BEFORE any write
// instead of succeeding with the field dropped. Then the unknown-key sweep: a
// supplied key the mutate schema does not declare at all, which no arm's rejected
// set can catch because those sets are authored from schema keys only. Then the
// payload-value check, on the arms with no criterion-specific handler — a
// `command` metadata value whose shape cannot tell a passing test from an absent
// one is rejected here rather than stored. See payloadCommands for why the two
// criterion-path arms are excluded from the fourth check.
//
// The swallowed-parameter check runs FIRST because every diagnosis below it is
// computed from a param set that is KNOWN INCOMPLETE once that shape is present:
// the swallowed params are indistinguishable from params the caller never sent,
// so an arm-specific "you supplied X" or "you omitted Y" message read off that
// payload would be describing a call the caller did not make.
//
// The classification check then runs before the unknown-key sweep so a rejected
// param keeps its deterministic first hit regardless of what else the payload
// carries, and so a DECLARED param keeps its arm-specific message rather than
// falling to the generic unknown-key form.
//
// Fail-closed on a missing carrier: a nil raw payload means accounting never
// ran, and passing everything through in that state would be the exact silent
// hole this gate exists to close.
func accountMutateParams(arm armID, a mutateArgs) error {
	if a.raw == nil {
		return fmt.Errorf("mutate: param accounting not initialized for arm %s", arm)
	}
	if err := rejectSwallowedParamValues("mutate", a.raw); err != nil {
		return err
	}
	if err := accountParams(mutateArmRegistry, "mutate", arm, a.raw); err != nil {
		return err
	}
	if err := rejectUndeclaredParams("mutate", "metadata", mutateProperties(), a.raw); err != nil {
		return err
	}
	operation := mutateArmRegistry[arm].operation
	for _, c := range payloadCommands(arm, a.raw) {
		if err := validate.RunSelectorGuard(fmt.Sprintf("mutate(%s)", operation), c.path, c.command); err != nil {
			return err
		}
	}
	return nil
}

// accountParams is the registry-agnostic half of the gate, shared by mutate and
// by the query surface: it looks the arm up in reg, reads the caller's verbatim
// key set, and returns the first REJECTED param the caller supplied, naming the
// field so the call fails before doing any work instead of succeeding with the
// field dropped. Every message names the tool it is given rather than a literal,
// which is the whole reason this is a parameter and not a constant.
//
// PRESENCE DECIDES, WITH ONE DECLARED EXCEPTION. A rejected param is refused on
// presence alone unless its arm declares a redundantValues set for it, in which
// case a value INSIDE that closed set is served and every value outside it is
// refused exactly as before — see param_accounting_redundant_values.go.
//
// DELIBERATELY NOT INCLUDED: the payloadCommands / RunSelectorGuard loop. That
// check is mutate-criterion-specific — it exists to reject a test command whose
// shape asserts nothing about its own selector — and has no meaning for a read.
// Its cost is a SECOND full json.Unmarshal of the same bytes, so admitting it
// here would double the per-call parse on every query the client serves. It
// stays in accountMutateParams, where it still runs and still runs AFTER the
// rejection loop, leaving mutate's behavior unchanged.
//
// Fail-closed on an unreadable payload: a nil key set means accounting could not
// run, and passing everything through in that state would be the exact silent
// hole this gate exists to close.
func accountParams(reg map[armID]armSpec, tool string, arm armID, raw json.RawMessage) error {
	spec, ok := reg[arm]
	if !ok {
		return fmt.Errorf("%s: param accounting has no registered arm %s", tool, arm)
	}
	supplied := suppliedMutateParams(raw)
	if supplied == nil {
		return fmt.Errorf("%s: param accounting could not read the supplied params for arm %s", tool, arm)
	}
	// Deterministic first hit: the same payload always names the same param.
	for _, key := range spec.rejectedSorted {
		if !supplied[key] {
			continue
		}
		// The one exception to presence-keyed rejection: a value the arm declares
		// valid-but-redundant restates a family the arm already pins, so it is
		// served rather than refused. Every other value falls through to the
		// rejection below unchanged.
		if redundantValueAccepted(spec, key, raw) {
			continue
		}
		if reason, ok := spec.rejectionReasons[key]; ok {
			return fmt.Errorf("%s(%s): %s is not applied by this path — %s", tool, spec.operation, key, reason)
		}
		return fmt.Errorf(
			"%s(%s): %s is not applied by this path (%s does not route it); "+
				"drop it or issue a separate call that does",
			tool, spec.operation, key, spec.handler,
		)
	}
	return nil
}

// unknownTopLevelParams returns every supplied key the given TOOL SCHEMA does
// not declare, sorted so the same payload always names the same key first — the
// same determinism rule the rejected-set loop above states.
//
// The schema is a PARAMETER rather than a call to mutateProperties() because the
// query surface runs the identical sweep against QueryToolDef's properties; a
// second copy of this loop would be two statements of one contract, drifting the
// moment either is edited. It is generic over the property value type so neither
// caller has to name kgtools.Property here.
//
// TOP-LEVEL ONLY: the input is suppliedMutateParams' key set, which never
// descends into nested objects, so metadata contents and batch sub-object keys
// are structurally out of reach here rather than filtered out.
func unknownTopLevelParams[T any](supplied map[string]bool, schema map[string]T) []string {
	var unknown []string
	for key := range supplied {
		if _, declared := schema[key]; !declared {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// validTopLevelParams renders a tool's declared params as a sorted, stable list
// for the unknown-key error. Shared by mutate and query for the same reason
// unknownTopLevelParams is.
//
// DERIVED FROM THE SCHEMA, never hardcoded: mutateProperties() already merges
// mutateBatchProperties(), so batch params are declared and are never reported
// unknown, and a frozen copy of the list would rot on the next schema addition —
// the exact drift mutate_schema_parity_test.go exists to catch.
func validTopLevelParams[T any](schema map[string]T) string {
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// payloadCommand is one (indexed field path, command) pair pulled out of a
// generic-arm payload for the command-shape gate to check.
type payloadCommand struct {
	path    string
	command string
}

// payloadCommands returns every `command` metadata value carried by a payload on
// an arm with NO criterion-specific handler: create_batch's nodes[],
// update_batch's items[], bulk_update_metadata's updates[], and upsert's
// top-level metadata. Those four ops write metadata blind to node type — upsert's
// decode does not consult it at all — and they are exactly what a bulk repair of
// the stored commands would reach for, so leaving them open would be the one way
// back into the class this gate closes.
//
// THE TWO CRITERION-PATH ARMS ARE SKIPPED, and the skip is load-bearing rather
// than an optimization. accountMutateParams runs at the HEAD of both
// InterceptAddCriterion and the typed-update router, ahead of the points where
// each stores its command. Without the skip this check would fire FIRST and
// report a payload-shaped field path in place of the criterion.command path those
// two handlers produce — preempting the specific error with a vaguer one. Each of
// them carries its own guard on the value it is about to store.
//
// TYPE-BLIND ON PURPOSE, and safe: every production writer of a `command`
// metadata value is a criterion path, and the `go test` requirement narrows it
// further, so a value that trips the check is a vacuous test command whatever
// node happens to hold it. One call site here also means a future arm inherits
// the check for free.
//
// suppliedMutateParams is deliberately NOT reused: it reports top-level KEY NAMES
// only, discarding values and never descending, and this needs values out of
// nested maps. The four-carrier walk itself lives in payloadMetadataMaps
// (payload_metadata.go), shared with the corpus check gate so the two cannot
// disagree about which carriers a payload has; this function adds only the
// `command` selection and the ".command" path suffix.
func payloadCommands(arm armID, raw json.RawMessage) []payloadCommand {
	if arm == armCriterionCreate || arm == armUpdateTyped {
		return nil
	}
	var found []payloadCommand
	for _, pm := range payloadMetadataMaps(raw) {
		if cmd := pm.Metadata["command"]; cmd != "" {
			found = append(found, payloadCommand{path: pm.Path + ".command", command: cmd})
		}
	}
	return found
}

// suppliedMutateParams returns the set of param keys the CALLER actually sent,
// reading the verbatim arguments payload rather than the decoded mutateArgs
// struct. The struct cannot answer this question: it collapses "key absent" and
// "key present but empty" for every string field, and several wire params
// (references, items, nodes, edges, updates, links, concludes, weight) are not
// carried on it in a form the dispatch arms read.
//
// A key is included iff it is PRESENT and NOT semantically empty — JSON null,
// "", 0, false, {} and [] all count as absent. That is the right rule because
// every downstream reader in this package already treats an empty scalar as
// unsupplied (updateSetFields in engine/compile_mutate.go emits only non-empty
// fields), so an empty value cannot be a silent drop and must not be reported
// as one.
//
// A payload that fails to unmarshal returns nil. Callers MUST treat nil as
// "accounting did not run" and fail closed rather than passing everything —
// see accountMutateParams.
//
// The key set is NOT filtered against the mutate schema: it surfaces every
// non-empty key the caller sent, including keys the schema does not declare.
// That unfiltered output is exactly what the two halves of the gate need. No
// arm can classify an undeclared key as rejected — every arm's rejected set is
// authored from schema keys only, so such a key has no cell anywhere — so
// accountMutateParams' schema-derived unknown-key sweep is what rejects it,
// reading this same set.
//
// `status` IS AN EXCEPTION TO THE EMPTY-IS-ABSENT RULE, and its presence is
// read SEPARATELY by statusExplicitlySupplied below — never by this function.
// An explicit status:"" is a clear-to-blank WRITE, so for that one param the
// premise above ("an empty value cannot be a silent drop") does not hold. This
// function's rule is left unchanged because it is right for every other param
// and for status ACCOUNTING (a blank status is still not a dropped param —
// it is a routed one). The two readers answer different questions; do not
// collapse them.
func suppliedMutateParams(raw json.RawMessage) map[string]bool {
	var argMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &argMap); err != nil {
		// Malformed payload: report nothing rather than fabricating a key set.
		// The caller fails closed on the nil.
		return nil
	}
	supplied := make(map[string]bool, len(argMap))
	for key, val := range argMap {
		if isEmptyJSONValue(val) {
			continue
		}
		supplied[key] = true
	}
	return supplied
}

// isEmptyJSONValue reports whether a raw JSON value is semantically empty for
// supply-detection purposes: null, "", 0, false, {} and [] are all "the caller
// did not supply this". Decoding per-type (rather than string-comparing the raw
// bytes) is what makes the rule hold for whitespace-formatted payloads and for
// numeric spellings like 0.0 and 0e0.
func isEmptyJSONValue(val json.RawMessage) bool {
	var decoded any
	if err := json.Unmarshal(val, &decoded); err != nil {
		// Undecodable value — treat as supplied so a malformed field is never
		// silently written off as absent.
		return false
	}
	switch v := decoded.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case float64:
		return v == 0
	case bool:
		return !v
	case map[string]any:
		return len(v) == 0
	case []any:
		return len(v) == 0
	}
	return false
}

// statusExplicitlySupplied reports whether the caller's payload carries a
// top-level `status` key, REGARDLESS of its value. It exists because status is
// the one mutate param whose empty value is a write rather than an omission:
// status:"" clears a node to blank, while an absent status leaves it untouched.
//
// Deliberately NOT built on suppliedMutateParams, whose documented rule treats
// "" as absent — correct for every other param, and exactly wrong here.
// A payload that fails to unmarshal reports false: an unreadable payload
// supplies nothing, and the callers reject it on other grounds first.
func statusExplicitlySupplied(raw json.RawMessage) bool {
	var argMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &argMap); err != nil {
		return false
	}
	_, present := argMap["status"]
	return present
}

// accountNonKnowledgeMutate accounts a mutate that declined every client arm and
// is about to fall through to the server carrying a non-knowledge graph.
//
// The link SKIP is load-bearing rather than an optimization: the cross-graph link
// block upstream does NOT return when its composer declines, so a declined link
// carrying a non-knowledge graph reaches the fallthrough too. It was already
// accounted upstream under its own arm, and accounting it a second time under a
// different spec would reject a call neither arm rejects on its own.
func accountNonKnowledgeMutate(a mutateArgs) error {
	if a.Operation == "link" {
		return nil
	}
	return accountMutateParams(armNonKnowledgeFallthrough, a)
}
