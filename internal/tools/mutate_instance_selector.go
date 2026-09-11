// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mutateInstanceSelectorVocabulary names every accepted graph-instance selector
// and the families that take none. It rides every refusal below, because the
// standing rule for bad input here is that the error names the offending value
// AND the vocabulary that would have worked.
const mutateInstanceSelectorVocabulary = "accepted graph-instance selectors: repo (code); knowledge, practice, checks and linkage address one graph and take none"

// requireGraphInstanceSelector refuses an instance-addressed mutate whose
// selector is empty, naming the caller's graph value and the required param
// before the vocabulary. Returns nil when the call is fine to proceed.
//
// IT REFUSES; IT NEVER GUESSES. No repo is inferred from cwd, no fallback to a
// first-listed graph, no proceeding with an empty selector — bad input fails
// loud, at the point of the mistake.
//
// WITHOUT THIS the caller pays a server round trip for "graph=code requires
// repo: graph selector invalid", which names neither what to send nor what the
// alternatives are.
//
// NO SECOND FAMILY SWITCH IS WRITTEN HERE. graphsel.AddressesOneGraph answers
// "is this a singleton" and graphsel.InstanceField answers "which field keys an
// instance of this family"; both live in that package and nowhere else, because
// a duplicated copy of that partition is what produced two prior production
// defects. A singleton family — knowledge, linkage, checks — returns nil, so
// this never shadows the corpus-check guard that runs for graph=="checks".
func requireGraphInstanceSelector(a mutateArgs) error {
	gt := kgtypes.GraphType(a.Graph)
	if graphsel.AddressesOneGraph(gt) {
		return nil
	}

	var param, supplied string
	switch graphsel.InstanceField(gt) {
	case graphsel.FieldRepo:
		param, supplied = "repo", a.Repo
	default:
		// FieldName and FieldNone. The name-addressed families (web, pdf,
		// linkage) have no selector param on this surface to require, and
		// FieldNone addresses no instance at all. THERE IS NO language ARM: it
		// keyed the practice instance until the eight per-language graphs became
		// one, and graphsel dropped the field with the graphs.
		return nil
	}
	if supplied != "" {
		return nil
	}
	return fmt.Errorf("mutate(%s): graph=%q requires %s — pass %s=\"<name>\"; %s",
		a.Operation, a.Graph, param, param, mutateInstanceSelectorVocabulary)
}
