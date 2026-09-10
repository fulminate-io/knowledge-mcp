// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// collect_postpopulate.go holds the post-collect PostPopulate orchestrator the
// collect work unit (builtinCollectWork, collect_detach.go) tail-calls: the
// collector-type → graph-type mapping and the breadth dispatch. Split out of
// collect.go to keep that file under the file-length cap; the two behaviors it
// carries beyond that relocation are the breadth dispatch and the failure
// surfacing, both documented on runPostCollectPostPopulate below.

// postPopulateGraphType maps a BUILT-IN collector type onto the store graph type
// whose named graphs the family's PostPopulate hook reads + writes. The codesync
// collector ("code") backs onto GraphCode, and it is the only entry left: the
// three CI/CD provider rows went with the built-in cicd collector and its graph
// family. A collector type with no entry has no postpopulate hook and is a no-op,
// which is what every registered custom collector type is: a contrib collector
// owns its own enrichment and runs it inside its own process, so there is no
// client-side hook to dispatch.
//
// THE MAP IS KEPT AS A MAP AT ONE ENTRY DELIBERATELY. Its shape is the
// collector-type → graph-type correspondence, and that correspondence is not the
// identity: a collector named for a provider can fill a family named for the
// domain, which is exactly what the removed rows did. Collapsing it to a
// conversion would compile today and mint an unmatchable graph type the next time
// a collector's name and its family's name differ.
var postPopulateGraphType = map[string]kgtypes.GraphType{
	"code": kgtypes.GraphCode,
}

// runPostCollectPostPopulate fires the registered PostPopulate hook for the
// collector family after a successful collect (code) over the GraphCaller wire
// seam, and errors when asked-for enrichment did not happen.
//
// IT IS NOT BEST-EFFORT (unlike its sibling runPostCollectLinker): the error
// rides builtinCollectWork into collectWaitOrDetach, which converts it into an
// error TOOL RESULT (the result the caller reads — NOT the collector's
// CollectResult, built before this tail). The split is CAPABILITY ABSENCE (no
// hook, no GraphCaller — nothing to fail) vs WORK FAILURE (bad mapping, failed
// enumeration, hook error, and the two BREADTH-DISPATCH WIRING DEFECTS below — a
// scoped hook with no collected graph name, an unrecognized breadth), UNIFORM
// across families with no carve-out.
//
// A wiring defect is on the WORK-FAILURE side because the enrichment the collect
// asked for demonstrably did not happen: it is a defect in this binary's own
// registration, not a capability the deployment lacks, so it fails the collect
// rather than skipping audibly.
//
// The hook key is the COLLECTOR TYPE (a.Type) — the registered keys are exactly
// the collector Name() values, NOT a graph-name prefix. The map above is the
// whole set and is the thing to read; no count is repeated here, because a
// count in a comment about a live map is what rots first.
//
// HOW WIDELY THE HOOK FIRES IS NOT ASSUMED HERE: breadth is DECLARED per hook at
// registration (postpopulate.Register) and this orchestrator dispatches on the
// value postpopulate.Lookup returns, never on the collector type.
//   - postpopulate.BreadthFamilyBroad: enumerate every graph of the family's
//     graph type via postpopulate.ListGraphNames and fire the hook against each,
//     because one collect of such a family can cascade several graphs and the
//     collected graph alone is not the whole subject; each of these hooks
//     self-filters by graph CONTENT (the resolveClusterLinkage-style silent
//     no-op), enriching the graphs it owns and no-opping on the rest. All-graphs
//     enumeration + idempotent re-run mirrors clientlinker.RunAll. NO COMPILED-IN
//     HOOK DECLARES THIS BREADTH TODAY: the ones that did went with the built-in
//     cloud collectors. The arm stays because breadth is declared per hook at
//     registration, not inferred from the type.
//   - postpopulate.BreadthScoped (the code hook): fire ONCE, against the graph that
//     was just collected. A code collect produces exactly one graph and the hook
//     body (LinkStepsToCode) inspects neither graph name nor content before it
//     reads, so enumerating the family would do a full per-graph read against every
//     other code graph on the machine for no derived edges.
//
// The fan-out also runs under a non-admitting operation, so firing against a graph
// of the type cannot earn it a place in the working set.
//
// A REGISTERED-CUSTOM COLLECT RUNS NO COMPILED-IN HOOK AT ALL, and that is the
// third arm of the capability-absence side. The hook key is the collector type,
// so a collect dispatched to a REGISTRATION under a name a compiled-in collector
// also holds presents the identical string here: postpopulate.Lookup finds the
// internal hook and the family-broad arm below would enumerate every graph of
// that hook's family and write into each — graphs the collect never touched and
// whose provider it is not. The registration's family is its own, so the only
// enrichment a compiled-in hook could do for it is enrichment of someone else's
// data.
//
// IT IS CAPABILITY ABSENCE, NOT WORK FAILURE: no enrichment was asked for by
// this collect and none was skipped, so it returns nil beside the no-hook case
// rather than failing the collect. The guard is keyed on the REGISTERED-CUSTOM
// FACT and on NO FAMILY, deliberately: a guard written against one family's
// names would leave every other family's names shadowable, and which families
// have compiled-in hooks is a fact about the map above rather than about this
// rule.
func runPostCollectPostPopulate(ctx context.Context, deps ClientDeps, collectorType, collectedGraph string, registeredCustom bool) error {
	if registeredCustom {
		return nil
	}
	hook, ok := postpopulate.Lookup(collectorType)
	if !ok {
		// web, pdf, logs, any sub-collector type: nothing to enrich, nothing to fail.
		return nil
	}
	graphType, ok := postPopulateGraphType[collectorType]
	if !ok {
		// A hook IS registered but has no graph-type mapping — a wiring defect.
		return fmt.Errorf("post-collect postpopulate: collector %q registers a hook but has no graph-type mapping", collectorType)
	}
	// Post-collect postpopulate follows the data: under the locked model the
	// collect sink wrote to cloud when logged in (local otherwise), so the
	// enrichment re-reads through the SAME login-routed GraphCaller — the
	// just-collected nodes live wherever the sink put them.
	gc := deps.GraphCaller()
	if gc == nil {
		// Degraded/test clients legitimately have none: failing here would fail
		// collects in configurations that never had enrichment at all.
		slog.Warn("post-collect postpopulate: GraphCaller unavailable (skipping)", "collector", collectorType)
		return nil
	}
	ctx = graphclient.WithOperation(ctx, graphclient.OpPostCollectFanout)
	var names []string
	switch hook.Breadth {
	case postpopulate.BreadthScoped:
		if collectedGraph == "" {
			// A scoped hook with no collected graph name is a wiring defect, not
			// a case to serve: falling back to enumeration would silently restore
			// the cross-graph fan-out the declaration exists to prevent, and
			// skipping would report a collect whose enrichment never ran as a
			// success. Fail it, like every other way this fanout does no work.
			return fmt.Errorf("post-collect postpopulate: collector %q declares a scoped hook but the collect supplied no graph name", collectorType)
		}
		names = []string{collectedGraph}
	case postpopulate.BreadthFamilyBroad:
		enumerated, err := postpopulate.ListGraphNames(ctx, gc, graphType)
		if err != nil {
			// Enumeration failing means the fanout did none of its work.
			return fmt.Errorf("post-collect postpopulate: enumerate %s graphs for collector %q: %w", graphType, collectorType, err)
		}
		names = enumerated
	default:
		// Reached only by a future third Breadth constant added to the type AND to
		// Register's validation without updating this switch. There is no unit-test
		// catcher for that shape — this ERROR is the detection mechanism, converting
		// a silent skip into a failed collect: the hook declared a breadth this
		// orchestrator cannot dispatch, so none of its enrichment ran. The adjacent
		// shape — a breadth value never added to Register's validation — cannot arrive
		// here at all: an outside package can construct a Hook value but cannot
		// deliver one, because Lookup reads only what Register wrote and Register
		// panics at init on a breadth outside the two constants
		// (TestRegister_PanicsOnUnknownBreadth).
		return fmt.Errorf("post-collect postpopulate: collector %q declares unknown breadth %q", collectorType, hook.Breadth)
	}
	// Attempt EVERY graph: stopping at the first failure would hide the rest.
	var failures []error
	for _, name := range names {
		if err := hook.Fn(ctx, gc, name); err != nil {
			failures = append(failures, fmt.Errorf("post-collect postpopulate: collector %q graph %q: %w", collectorType, name, err))
		}
	}
	return errors.Join(failures...)
}
