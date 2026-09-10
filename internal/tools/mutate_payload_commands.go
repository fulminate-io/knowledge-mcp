// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_payload_commands.go carries the PAYLOAD-VALUE half of the mutate gate:
// the criterion-command extraction accountMutateParams runs after its
// classification checks.
//
// IT IS A SPLIT OF mutate_param_accounting.go ON FILE LENGTH ONLY, and this is
// the coherent unit to move because the parent file's own header already names
// it a separate concern: the classification checks answer "is this param
// routed", which cannot answer "is this value usable", and the command-shape
// check is the second question. The gate's call order is unchanged — the
// classification checks still run first, and this still runs last — because the
// order lives in accountMutateParams, not in the file layout.

import "encoding/json"

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
