// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_reap_invoker.go carries the balance verdict's server-side half: the
// Index RPC that removes dead vectors.

// indexRPC is the narrow Index seam the reap needs, asserted off the client's graph
// caller the same way the coverage read asserts its stats seam. A caller that does not
// satisfy it (a router-less fixture, degraded headless mode) yields NO reaper, and the
// verdict then REPORTS an imbalance rather than concluding one — an unhealed gap is not
// evidence of a defect.
type indexRPC interface {
	Index(ctx context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error)
}

// rpcReapInvoker implements ReapInvoker over the Index RPC.
type rpcReapInvoker struct{ ix indexRPC }

// ReapDeadVectors asks the server to remove this graph's dead vectors and returns how
// many it removed.
//
// THE OBSERVED GAP RIDES AS A PARAM because the server cannot compute it: the imbalance
// is measured against a LOCAL resident count. The server uses it for ONE decision —
// whether the cheap tier left enough owed to justify escalating to the orphan anti-join
// — and owns both that decision and its no-progress bound. The verdict asks for a reap;
// it does not select a tier.
//
// THE RETURNED COUNT IS FOR THE LOG LINE, NOT FOR THE VERDICT. The caller RE-READS its
// operands afterwards rather than subtracting this number, precisely so a reap whose own
// accounting is wrong still yields a defect instead of a fabricated balance.
func (r rpcReapInvoker) ReapDeadVectors(
	ctx context.Context, gt kgtypes.GraphType, name string, gap int,
) (int, error) {
	target := graphsel.GraphSelectorFor(gt, name, false)
	if gt == kgtypes.GraphKnowledge && name == "" {
		// The DEFAULT knowledge graph addresses as an empty selector rather than by
		// name, the same special case the coverage read carries.
		target = &knowledgev1.GraphSelector{Graph: ""}
	}
	resp, err := r.ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    target,
		Operation: knowledgev1.IndexRequest_INDEX_OP_REAP_DEAD_VECTORS,
		Params:    map[string]string{"gap": fmt.Sprintf("%d", gap)},
	})
	if err != nil {
		return 0, fmt.Errorf("bootstrap.ReapDeadVectors %s/%s: %w", gt, name, err)
	}
	return int(resp.GetAffectedCount()), nil
}

// reapInvokerFor builds the reap invoker for this client, or nil when the graph caller
// carries no Index seam.
//
// NIL IS A REAL STATE AND IT DECLINES RATHER THAN DEFAULTING. Without a reap the verdict
// cannot tell a dead-vector inflation from a genuine shortfall, so it reports the
// imbalance and concludes nothing — which is the honest answer, and the reason
// evaluateBalanceAtQuiescence checks for a nil reaper before declaring anything.
func reapInvokerFor(c *client) ReapInvoker {
	ix, ok := c.GraphCaller().(indexRPC)
	if !ok {
		return nil
	}
	return rpcReapInvoker{ix: ix}
}

// attachBalanceVerdict wires the QUIESCENCE-EDGE balance verdict onto the pipeline:
// once BOTH axes have drained at the current collect epoch, form the exact
// resident-versus-vectors verdict, let the dead-vector reap heal the direction it can
// repair, RE-READ, and report only a surviving imbalance as a defect — driving one
// rebuild when that survivor is a genuine shortfall.
//
// THE TWO DRIVERS ARE INSTALLED HERE RATHER THAN RESOLVED INSIDE THE CLOSURE, so the
// Index-seam type assertion and the rebuild driver's construction happen ONCE at wiring
// time instead of on every quiescence edge.
//
// IT IS A FUNCTION rather than three lines at the call site because that caller is at
// its length gate; the grouping is also honest — these three writes are one wiring
// decision and would drift apart if a later edit touched only one of them.
//
// THE CALLER'S SEGMENT-MANAGER GUARD STILL APPLIES: without a manager there is no
// resident operand to read, so this is never reached and the balance edge no-ops.
func attachBalanceVerdict(p *pipeline.Pipeline, c *client) {
	c.reaper = reapInvokerFor(c)
	c.rebuild = rebuildDriverFor(c)
	c.repairArm = repairArmDriverFor(c)
	p.AttachBalanceFactory(c.buildBalanceFactory())
}

// repairArmDriverFor builds the verdict's BOUNDED remedy — the arm that ships only the
// uncovered ids rather than rebuilding the corpus.
//
// IT MAKES THE SAME CALL, WITH THE SAME ARGUMENTS, that the periodic backstop's own
// dependency makes at clientRepairDeps.Repair. That is deliberate: the demand-driven
// route and the sweep route reach the bounded arm through one call shape, so they
// cannot drift into repairing differently, and RepairUncoveredSegments' own per-graph
// single-flight is what keeps them from colliding on one graph.
func repairArmDriverFor(c *client) repairDriver {
	return func(ctx context.Context, gt kgtypes.GraphType, name string) (tools.RepairOutcome, error) {
		return tools.RepairUncoveredSegments(ctx, c.PipelineScanner(), c.segmentMgr, gt, name)
	}
}

// rebuildDriverFor builds the verdict's repair for a shortfall the reap could not close.
//
// IT IS THE SAME DRIVER THE HEAL PATH USES — tools.RebuildSegments over the same
// login-routed scanner seam and the same segment manager as shipper — so the two arms
// cannot drift into rebuilding differently, and RebuildSegments' own single-flight
// (shared with the manual rebuild_segments op) is what keeps a balance-driven rebuild
// from racing a heal-driven one.
//
// FROM SCRATCH, for the reason the heal path gives: the verdict fires precisely because
// the local corpus is short, so scoping the scan to what changed recently would rebuild
// a slice of a corpus that is missing and never converge.
func rebuildDriverFor(c *client) rebuildDriver {
	return func(ctx context.Context, gt kgtypes.GraphType, name string) error {
		out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), gt, name, true)
		if err != nil {
			return err
		}
		// published is logged beside the counts because they answer different questions:
		// the counts say what was BUILT AND SHIPPED, published says whether the manifest
		// swap that makes those blobs the live set LANDED. A rebuild that ships
		// everything and publishes nothing restored no coverage.
		slog.Info("bootstrap: rebuilt segments to repair a balance deficit that survived the dead-vector reap",
			"graph_type", gt, "name", name, "ran", out.Ran, "scanned", out.Scanned,
			"built", out.Built, "partial", out.Partial, "published", out.Published)
		return nil
	}
}
