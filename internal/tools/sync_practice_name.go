// SPDX-License-Identifier: Apache-2.0

// sync_practice_name.go — how the sync arms answer a practice `name`, and the
// client-side fence that keeps a name they cannot address from costing a
// whole-graph serialize or creating an unreachable local .bin.
//
// THE ARMS NO LONGER DIVERGE FROM EVERY OTHER PRACTICE ARM, and the convergence
// is what this file now records. Sync moves whole GRAPH IMAGES rather than
// nodes, and for a while the eight pre-singleton practice images still existed
// on both sides — the migration had to move them, and a machine that upgraded
// before the migration ran still held them — so on push and pull a NON-EMPTY
// name named one of the eight while only an absent or empty name meant the
// combined graph. Those images are gone from the plane, and the only practice
// image either direction can move is the combined graph's.
//
// COERCION IS NOT AN OPTION HERE, which is the half that did not change. Folding
// "go" into "default" is the silent coercion the bad-input invariant forbids: it
// exported one graph under eight names and reported success while refusing
// nothing. The answer is a refusal, and the refusal names the address that works.
//
// A DELETION WOULD HAVE REINSTATED THE DEFECT RATHER THAN REMOVING IT. The
// builder below used to special-case a legacy name because manageGraphSelector
// cannot express one: graphsel.InstanceField puts practice in the FieldNone arm,
// so a name a caller supplied lands on NO field at all and the server opens the
// combined graph instead. That silent drop is what exported practices/default.bin
// for a push of practice/go. So the special case became a refusal, ahead of both
// seams, rather than going away.

package tools

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// unaddressablePracticeSyncName returns the practice name a sync arm cannot
// address for (graph, name), or "" when the arm addresses the combined graph.
//
// The empty answer covers three cases that must not be told apart downstream:
// another family entirely, an absent practice name, and the combined graph named
// explicitly. InterceptSync has already defaulted an absent name to
// workingset.DefaultInstanceName by the time this runs, so both spellings of "the
// combined graph" arrive here as that literal and leave as "".
func unaddressablePracticeSyncName(graph, name string) string {
	if kgtypes.GraphType(graph) != kgtypes.GraphPractice {
		return ""
	}
	if name == "" || name == workingset.DefaultInstanceName {
		return ""
	}
	return name
}

// refusePracticeSyncName is the client half of the singleton rule for the sync
// arms: a practice name other than the combined graph's own is refused, naming
// the address that works. op is the operator-facing operation label ("sync push"
// / "sync pull").
//
// IT RUNS BEFORE THE ARM DOES ANY WORK, which is the same placement the server's
// cloud receive fence argues for: a name this transfer can never address should
// not cost the caller a whole-graph serialize, an upload, or — on pull — a local
// apply that CREATES the .bin it was told to write. Pull is the direction that
// makes the fence load-bearing rather than merely tidy: OverwriteGraph applies to
// the flat (graph_type, name) it is handed, so an admitted "Design Patterns"
// would leave a local practice graph no read of the family can open.
//
// THE SERVER'S RECEIVE FENCE IS THE FAR HALF AND IS INDEPENDENT, not a chain
// this one can be trimmed in favor of: store.RefuseNonCanonicalGraphName admits
// only the combined graph's name for the practice family, so a push that reached
// the cloud would be answered 400 there. Two refusals, either of which holds on
// its own.
func refusePracticeSyncName(op, graph, name string) error {
	offending := unaddressablePracticeSyncName(graph, name)
	if offending == "" {
		return nil
	}
	return fmt.Errorf(
		"%s: practice graph name %q cannot be addressed - the practice family is ONE combined graph, "+
			"addressed with no name at all (or with %q, which is what that graph is called); %q names a graph no read of the family can reach",
		op, offending, workingset.DefaultInstanceName, offending)
}

// syncGraphSelector builds the LOCAL ExportGraph target for a sync push.
//
// It is manageGraphSelector for every family and every graph — the same builder
// the other five manage arms use, so a family's instance field stays declared in
// graphsel and nowhere else. There is no divergence left to express: the one
// practice name a push can address is the combined graph's, which graphsel
// composes as no instance field at all, and every other name is refused by
// refusePracticeSyncName before this builder runs.
//
// IT SURVIVES AS A NAMED FUNCTION RATHER THAN COLLAPSING INTO ITS CALLER because
// the push arm's target is the thing the silent drop above was measured on, and a
// reader tracing "which selector does a push send" should land on a function that
// says so.
func syncGraphSelector(graph, name string) *knowledgev1.GraphSelector {
	return manageGraphSelector(graph, name)
}
