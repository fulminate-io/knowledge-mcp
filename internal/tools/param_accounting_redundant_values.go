// SPDX-License-Identifier: Apache-2.0

package tools

// param_accounting_redundant_values.go holds the REDUNDANT-VALUE convention for
// the per-arm param gate: the one case where a param an arm classifies REJECTED
// is nonetheless accepted, because the value the caller sent names exactly the
// thing the arm already pins.
//
// THE CONVENTION. An arm that serves one graph family and routes no selector
// rejects `graph` — supplying it means the caller expected a routing this path
// does not perform, and saying so is more useful than dropping it. But the value
// that names THAT SAME family is not a misunderstanding: it is a caller
// restating the arm's own contract, and the honest answer is to serve the call.
// So the rejection keys on the VALUE rather than on mere presence wherever an
// arm declares a redundant-value set, and every value outside that closed set
// keeps the existing structured rejection verbatim.
//
// THE PRECEDENT this follows, rather than a new idea: the server's family
// selector already treats a singleton family's instance name this way.
// knowledgeRootNameAliases (cmd/knowledge-server/internal/tools/
// tools_graph_routing_selector.go) accepts "", "default" and "knowledge" as
// labels for the one knowledge graph and refuses everything else, on the same
// reasoning — a singleton has no instance to select, so a name that denotes it
// is redundant and a name that denotes anything else is a caller asking for a
// graph that does not exist. This file is that rule applied one layer up, at
// param accounting, and the sets are deliberately NOT shared: the selector's set
// answers "which label denotes the root graph", this one answers "which value
// makes a rejected selector redundant", and collapsing them would tie two
// contracts on different paths to one edit.
//
// SCOPE IS DELIBERATELY NARROW. A redundant-value set is only honest where the
// arm pins the family, so a value naming it changes nothing about what the arm
// does. It is NOT a general escape hatch for a rejected param: a param whose
// value would change the read is either consumed or rejected, and there is no
// third answer for it.

import (
	"encoding/json"
	"slices"
)

// redundantValueAccepted reports whether the caller's value for a REJECTED param
// is one the arm declares valid-but-redundant, in which case the gate serves the
// call instead of refusing it.
//
// STRING-VALUED ONLY, and that is the whole class: every redundant-value param is
// a selector label. A non-string value fails the decode and is rejected, which is
// the right answer — a graph selector sent as a number is a caller error whatever
// the alias set says.
//
// An arm with no redundantValues map returns false for every key, so the gate's
// behavior on the other forty-six arms is byte-for-byte what it was.
func redundantValueAccepted(spec armSpec, key string, raw json.RawMessage) bool {
	accepted, declared := spec.redundantValues[key]
	if !declared {
		return false
	}
	var argMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &argMap); err != nil {
		return false
	}
	var value string
	if err := json.Unmarshal(argMap[key], &value); err != nil {
		return false
	}
	return slices.Contains(accepted, value)
}
