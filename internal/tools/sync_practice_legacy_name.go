// SPDX-License-Identifier: Apache-2.0

// sync_practice_legacy_name.go — how the sync arms address a LEGACY practice
// graph, and the client-side fence that keeps a name they cannot address from
// costing a whole-graph serialize or creating an unreachable local .bin.
//
// WHY THE SYNC ARMS DIVERGE FROM EVERY OTHER PRACTICE ARM. The practice family is
// a singleton for reading and writing NODES: an unselected address is the
// combined graph, and `language` survives on the read arms only as the legacy
// selector for the eight pre-singleton graphs. Sync moves whole GRAPH IMAGES
// between this machine and the account, and those eight images still exist on
// both sides — the migration has to move them, and a machine that upgrades before
// the migration runs still holds them. So on push and pull a NON-EMPTY name is
// taken to name one of the eight (the store's practices/<slug>.bin, the account's
// practice/<slug>), and only an absent or empty name is the combined graph.
//
// COERCION IS NOT AN OPTION HERE. Folding "go" into "default" is the silent
// coercion the bad-input invariant forbids: it exported one graph under eight
// names and reported success while refusing nothing. Slugifying "Design Patterns"
// into "design-patterns" is the same defect wearing a helpful face. Both are
// refusals, and the refusal names the spelling that would have worked.
//
// EVERY OTHER ARM IS UNTOUCHED BY THIS FILE. query, search, traverse, assemble,
// mutate, delete and manage keep the dispositions they have: an unselected
// practice address is the combined graph, `language` is the legacy READ selector,
// and a practice write carrying `language` is refused naming `source`.

package tools

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// legacyPracticeSyncName returns the LEGACY practice graph a sync arm addresses
// for (graph, name), or "" when the arm addresses the combined graph.
//
// The empty answer covers three cases that must not be told apart downstream:
// another family entirely, an absent practice name, and the combined graph named
// explicitly. InterceptSync has already defaulted an absent name to
// workingset.DefaultInstanceName by the time this runs, so both spellings of "the
// combined graph" arrive here as that literal and leave as "".
func legacyPracticeSyncName(graph, name string) string {
	if kgtypes.GraphType(graph) != kgtypes.GraphPractice {
		return ""
	}
	if name == "" || name == workingset.DefaultInstanceName {
		return ""
	}
	return name
}

// refusePracticeSyncName is the client half of the canonical-name rule for the
// sync arms: a legacy practice name that is not its own slug is refused, naming
// the spelling that would have worked. op is the operator-facing operation label
// ("sync push" / "sync pull").
//
// IT RUNS BEFORE THE ARM DOES ANY WORK, which is the same placement the server's
// cloud receive fence argues for: a name this transfer can never address should
// not cost the caller a whole-graph serialize, an upload, or — on pull — a local
// apply that CREATES the .bin it was told to write. Pull is the direction that
// makes the fence load-bearing rather than merely tidy: OverwriteGraph applies to
// the flat (graph_type, name) it is handed, so an admitted "Design Patterns"
// would leave a local practice graph no read of the family can open.
func refusePracticeSyncName(op, graph, name string) error {
	return graphsel.RefuseNonCanonicalPracticeLanguage(op, legacyPracticeSyncName(graph, name))
}

// syncGraphSelector builds the LOCAL ExportGraph target for a sync push.
//
// It is manageGraphSelector for every family and every combined-graph push — the
// same builder the other five manage arms use, so a family's instance field stays
// declared in graphsel and nowhere else. The ONE divergence is a legacy practice
// name, which manageGraphSelector cannot express: graphsel.InstanceField puts
// practice in the FieldNone arm, so the name a caller supplied lands on no field
// at all and the server opens the combined graph instead. That silent drop is
// what exported practices/default.bin for a push of practice/go.
func syncGraphSelector(graph, name string) *knowledgev1.GraphSelector {
	if legacy := legacyPracticeSyncName(graph, name); legacy != "" {
		return graphsel.LegacyPracticeSelector(legacy)
	}
	return manageGraphSelector(graph, name)
}
