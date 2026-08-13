// SPDX-License-Identifier: Apache-2.0

// router_admission.go — the working-set admission recorder on the routed-call
// chokepoint.
//
// It lives HERE, on Router.Execute, rather than in a decorator around the tools
// GraphCaller. GraphCaller is a one-method interface (Execute only) that every
// intercept type-asserts UP from to reach the carrier seams it needs — Stats,
// Index, MetadataStats, Hive, ExportGraph. A struct EMBEDDING that interface
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
// THE KNOWLEDGE FAMILY IS THE NAMED EXCEPTION to (2). Normalize collapses the
// knowledge empty instance to "default", so a type-only knowledge target under
// an admitting operation DOES admit knowledge/default. That is correct —
// knowledge is single-instance, so a type-only knowledge target IS the default
// instance, and a user query against the knowledge graph is a direct
// interaction. It does mean the operation partition stays load-bearing for
// knowledge instead of being backstopped by structure.
//
// Reads admit as well as writes: the rule names "some kind of mcp query like
// search, mutate, collect", so the recorder reads the request's Target and does
// not branch on the query/mutation oneof.
func (r *Router) recordAdmission(ctx context.Context, req *knowledgev1.ExecuteRequest) {
	if r == nil || req == nil {
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
	gt, name, ok := graphsel.InstanceKeyOf(req.GetTarget())
	if !ok {
		return
	}
	if _, ok := workingset.Normalize(gt, name); !ok {
		return
	}
	admit(gt, name, string(op))
}
