// SPDX-License-Identifier: Apache-2.0

// router_admission.go — the working-set admission recorder on the routed-call
// chokepoint.
//
// It lives HERE, on Router.Execute, rather than in a decorator around the tools
// GraphCaller. GraphCaller is a one-method interface (Execute only) that every
// intercept type-asserts UP from to reach the carrier seams it needs — Stats,
// Index, MetadataStats, ExportGraph. A struct EMBEDDING that interface
// promotes only Execute, so all sixteen of those upgrades would start returning
// ok=false and degrade SILENTLY; the worst of them returns nil rows and kills the
// whole manage(status) coverage table. Router carries those seams as explicit
// forwarders precisely so the upgrades succeed, so recording here keeps every
// seam intact by construction and leaves no forwarding list to rot.
//
// The compensating cost is stated rather than hidden: Router.Execute is the
// funnel background and user traffic SHARE, so there is no structural exclusion
// keeping the pipeline's writeback out — the operation partition
// (AdmitsWorkingSet) is the sole mechanism, and its parity test pins that term
// by name.

package graphclient

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// AttachWorkingSet installs the admission recorder. The admitter is taken as a
// FUNC rather than as a *workingset.Set so graphclient needs no dependency on
// the package that owns the set, and so a test can attach a recording closure.
// A Router with no admitter recorded nothing, which is the default-deny
// direction: it under-admits rather than silently admitting everything.
func (r *Router) AttachWorkingSet(admit func(gt kgtypes.GraphType, name, reason string)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admitGraph = admit
}

// recordAdmission admits req's target graph when BOTH halves of the gate agree:
//
//  1. the ctx-stamped operation is one that may admit at all. An unstamped ctx
//     denies. This is checked FIRST so a denied operation costs one map lookup
//     and never touches the target.
//
//  2. the request addresses a concrete graph INSTANCE. This is the structural
//     half: a catalog enumeration compiles to a target carrying the graph TYPE
//     and no instance key, so it cannot admit what it enumerates — for every
//     family whose instance key is repo / account / language.
//
// THE SINGLE-INSTANCE FAMILIES ARE THE NAMED EXCEPTIONS to (2), and there are
// two: knowledge and checks. Normalize collapses their empty instance to
// "default", so a type-only target for either, under an admitting operation, DOES
// admit. That is correct — each has exactly one instance, so a type-only target
// IS that instance and a user read of it is a direct interaction. The two arrive
// at it from opposite directions: knowledge is written both as "" and as
// "default" by existing code, while checks carries NO instance field at all, so
// "" is the only spelling any reader can send. It does mean the operation
// partition stays load-bearing for both instead of being backstopped by
// structure.
//
// CHECKS WAS MISSING FROM THAT COLLAPSE AND THE COST WAS SILENT: every read of
// the graph was structurally unable to admit it, so the catalog loop registered
// no collector and its nodes stayed unembedded through every drain while this
// gate's operation half passed cleanly.
//
// Reads admit as well as writes: the rule names "some kind of mcp query like
// search, mutate, collect", so the recorder reads the request's Target and does
// not branch on the query/mutation oneof.
// IT TAKES THE ALREADY-RESOLVED (gt, name) rather than the request, and that
// split is deliberate. Resolving the family is SELECTOR VALIDATION, which is
// boundary work that belongs ahead of the dispatch; enrolling the member is
// ENROLLMENT, which belongs after one. Threading the resolved pair through means
// the family is resolved EXACTLY ONCE per Execute and the two concerns cannot
// drift onto different readings of the same selector.
//
// Every remaining early return here is a legitimate NON-ADMISSION rather than a
// swallowed error: a nil Router, the empty graph type standing for a nil target,
// an operation outside the working-set partition, an unattached admit hook, and
// an instance name workingset.Normalize declines.
func (r *Router) recordAdmission(ctx context.Context, gt kgtypes.GraphType, name string) {
	if r == nil || gt == "" {
		return
	}
	op, ok := OperationFromContext(ctx)
	if !ok || !AdmitsWorkingSet(op) {
		return
	}
	r.mu.Lock()
	admit := r.admitGraph
	r.mu.Unlock()
	if admit == nil {
		return
	}
	if _, ok := workingset.Normalize(gt, name); !ok {
		return
	}
	admit(gt, name, string(op))
}

// resolveAdmissionTarget is the ONE place this package resolves a graph family,
// and it is BOUNDARY VALIDATION: a target naming a family this binary cannot
// honor is bad input and is refused before any work is done.
//
// THE NIL-TARGET CASE IS SPLIT OUT FIRST, and that is the load-bearing detail.
// graphsel.InstanceKeyOf reports !ok for TWO different reasons — a nil selector,
// and a set-but-unrecognized family — and only the second is an error. A nil
// Target is the KNOWLEDGE DEFAULT: engine.mutateTarget returns nil for the
// all-empty case, which is how almost every knowledge write addresses its graph.
// Folding the two together would turn ordinary traffic into a hard error, so the
// nil target is answered here, ahead of InstanceKeyOf. Past that guard, !ok means
// exactly one thing and the error can say so.
//
// THE MESSAGE NAMES THE NUMERIC VALUE, not the condition, because an operator
// cannot act on "some family". It deliberately does NOT name the legacy `graph`
// string: falling back to that string for a family the binary does not know is
// the precise defect the typed enum exists to make loud, and quoting it in the
// refusal invites exactly that reading.
//
// A nil request and a nil target both resolve to the empty graph type with NO
// error — nothing to admit, nothing wrong.
func resolveAdmissionTarget(req *knowledgev1.ExecuteRequest) (kgtypes.GraphType, string, error) {
	if req == nil || req.GetTarget() == nil {
		return "", "", nil
	}
	sel := req.GetTarget()
	gt, name, ok := graphsel.InstanceKeyOf(sel)
	if !ok {
		return "", "", fmt.Errorf(
			"graph selector names family %d, which this binary does not recognize",
			int32(sel.GetFamily()))
	}
	return gt, name, nil
}
