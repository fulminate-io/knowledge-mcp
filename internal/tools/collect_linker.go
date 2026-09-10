// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientlinker "github.com/fulminate-io/knowledge-mcp/internal/linker"
)

// collect_linker.go holds the post-collect CROSS-GRAPH LINKER tail: the
// collector-type trigger and the helper collectWork calls after a successful
// collect. Split out of collect.go to keep that file under the file-length cap,
// and split to THIS file rather than any other so the linker tail sits beside its
// sibling in collect_postpopulate.go — the two tails carry the same
// registered-custom gate, and a reader who finds one should find the other.

// postCollectLinkerType is the ONE collector type that triggers the post-collect
// cross-graph linker.
//
// THE TRIGGER FOLLOWS THE PASS'S INPUT. The linker used to be triggered by the
// cloud provider collects and then, after those went, by the CI/CD ones — an
// allowlist inherited from a server-side gate rather than derived from what the
// passes read. It has ONE pass left, the Dockerfile signal, and that pass reads
// CODE graphs on both sides of the relationship it discovers: a repo's own
// Dockerfile node and the repo's own file and package nodes. A cicd collect
// therefore fired a pass over data it had not touched and could not change,
// while the collect that DID change that data fired nothing. Retiring the cicd
// family removed the last name from the old allowlist and left the correction
// unavoidable: the trigger is the code collect.
//
// IT IS A CORRECTION, NOT A NEW CAPABILITY. No graph is read that was not read
// before, no edge is emitted that manage(operation:"link") did not already
// emit, and the pass is the same one — only the moment it runs moved to the
// moment its input changes.
var postCollectLinkerType = string(kgtypes.GraphCode)

// runPostCollectLinker runs the Dockerfile pass in-process after a successful
// collector.Collect of a code graph, SCOPED TO THE GRAPH JUST COLLECTED.
// Best-effort: slog.Warn on nil-caller, on a missing graph name, or on a run
// error, but the caller's textResult is returned unchanged so the linker tail
// never fails an otherwise-successful collect. Like the postpopulate tail it runs
// under a non-admitting operation, so reading the graph it links cannot earn it a
// place in the working set.
//
// THE SCOPE IS THE WHOLE POINT AND IT IS NOT AN OPTIMIZATION DETAIL.
// clientlinker.LinkDockerfiles enumerates every non-overlay code graph on the
// machine and pays two full keyset drains (NodeFile and NodePackage) in each, so
// wiring this tail to it would add 2N drains to every code collect on a machine
// holding N code repos — and this repository collects after every commit. The
// pass can only derive edges INSIDE one graph, so every drain but the collected
// graph's is read for nothing. clientlinker.LinkDockerfilesInGraph is the scoped
// entry point, and it carries the vocabulary cache and the overlay skip that used
// to live in the enumerating wrapper.
//
// manage(operation:"link") KEEPS THE SWEEP. Breadth is the manual operation's
// whole value: an operator asking for a link pass wants every graph, and that
// caller pays the enumeration deliberately. The two callers differ in breadth and
// in nothing else.
//
// AN EMPTY GRAPH NAME SKIPS AUDIBLY AND NEVER ENUMERATES. A scoped tail with no
// name is a wiring defect, and the two wrong answers are both available: falling
// back to the sweep would silently restore the fan-out the scope exists to
// prevent, and failing the collect would report a successful upload as a failure.
// It warns and does nothing, which is what a best-effort tail owes a defect it
// cannot repair.
//
// A REGISTERED-CUSTOM COLLECT RUNS NO LINKER AT ALL, and the guard is keyed on
// the REGISTERED-CUSTOM FACT rather than on the collector type, for the same
// reason its post-populate sibling is. The type check below keys on the type
// STRING, and a registration under the name "code" would present the identical
// string — so the type check alone admits it, and the pass would then read and
// write a built-in code graph on behalf of a family whose data none of it is.
//
// IT IS CAPABILITY ABSENCE, NOT WORK FAILURE — the same reading the postpopulate
// guard carries — and this tail is best-effort either way, so the skip is silent
// rather than an error. WHAT THE COLLECTOR CAN OWN INSTEAD: a proxy node standing
// for a foreign resource, and an ordinary edge from one of its own nodes to that
// proxy, are both expressible in the contract's existing node and edge fields. An
// edge whose other endpoint lives in another graph is NOT: the result is stamped
// with one family and the contract edge carries no graph selector, so such an id
// lands as a dangling edge inside the collector's own graph. The carrier for those
// is a separate contract revision.
//
// registeredCustom and collectedGraph are both threaded from collectWork rather
// than re-derived, so this gate and the post-populate gate a few lines below its
// call site read the SAME values from the SAME single resolution. A second
// derivation here could not tell a registration from a typo: the collector type
// string is identical on both paths, which is precisely the case the gate must
// tell apart.
func runPostCollectLinker(ctx context.Context, deps ClientDeps, collectorType, collectedGraph string, registeredCustom bool) {
	if registeredCustom {
		return
	}
	if collectorType != postCollectLinkerType {
		return
	}
	// Post-collect linker follows the data: under the locked model the collect
	// sink wrote to cloud when logged in (local otherwise), so the cross-graph
	// linker walks through the SAME login-routed GraphCaller — the
	// just-collected nodes live wherever the sink put them.
	gc := deps.GraphCaller()
	if gc == nil {
		slog.Warn("post-collect linker: GraphCaller unavailable (skipping)", "collector", collectorType)
		return
	}
	if collectedGraph == "" {
		slog.Warn("post-collect linker: no collected graph name (skipping; this pass is scoped to one graph and has no all-graphs fallback)",
			"collector", collectorType)
		return
	}
	ctx = graphclient.WithOperation(ctx, graphclient.OpPostCollectFanout)
	links, err := clientlinker.LinkDockerfilesInGraph(ctx, gc, clientlinker.LinkOptions{}, collectedGraph)
	if err != nil {
		slog.Warn("post-collect linker failed", "collector", collectorType, "graph", collectedGraph, "error", err)
		return
	}
	slog.Debug("post-collect linker complete", "collector", collectorType, "graph", collectedGraph, "links", links)
}
