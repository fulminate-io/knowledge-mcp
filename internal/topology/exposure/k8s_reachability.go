// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability.go implements the KubernetesReachabilityAnalyzer — a
// cloud-graph analyzer that computes pod↔pod reachability across a Kubernetes
// cluster by walking pre-resolved NetworkPolicy edges and emits findings for
// notable patterns. Phase 3 Step 1 adds the struct, Run method, init()
// registration, and the classifyReachability stub. Phase 4 fleshes out the
// classifier. The architecture spec is preserved below for downstream phases.
//
// PURPOSE. Compute pod↔pod reachability by walking the directional
// NetworkPolicy edges added by Phase 1 Step 1 (EdgeAllowsIngressFrom and
// EdgeAllowsEgressTo), classify the resulting reachability matrix, and emit
// findings for: isolated pods (no peers in either direction), over-exposed
// pods (allowed from entire namespace or cluster), asymmetric reachability
// (A→B allowed but B→A blocked when symmetry was intended), fully-open
// namespaces, and world-exposure via ipBlock CIDR peers.
//
// EDGE MODEL. The collector (cloud/k8s/postpopulate*.go) pre-resolves every
// NetworkPolicy's podSelector/namespaceSelector/ipBlock rules into directional
// edges at index time — the hybrid approach locked in the plan's OQ answer.
// topology/ walks those generic edges and never calls K8s selector code.
// topology/ DOES re-parse NetworkPolicy JSON for ipBlock CIDR classification
// via a local anonymous struct so ipBlock world-exposure analysis stays out of
// the collector's hot path; that re-parse is the ONLY NetworkPolicy-aware
// logic in this file.
//
// SEMANTICS. Standard K8s NetworkingV1 NetworkPolicy rules:
//   - A pod NOT selected by any policy has all-allow on that direction
//     (default-allow).
//   - A pod selected by any ingress policy has default-deny ingress — only
//     explicit EdgeAllowsIngressFrom peers may reach it.
//   - A pod selected by any egress policy has default-deny egress — the pod
//     may only reach explicit EdgeAllowsEgressTo peers.
//   - For src→dst to be reachable BOTH sides must allow: src's egress rules
//     must permit dst AND dst's ingress rules must permit src.
//   - An empty namespaceSelector {} matches ALL namespaces including
//     unlabeled ones; any match-criteria selector does NOT match unlabeled
//     namespaces (K8s selector-matching rule).
//
// PORT / PROTOCOL MATCHING. Reachability is keyed by (src, dst, protocol,
// port) tuples. Phase 2.5 extends the directional edges with (protocol,
// port_from, port_to, named_port) metadata on every edge. The reachability
// index performs range-intersection matching so a policy allowing TCP 80-90
// correctly reaches a pod listening on TCP 80. Named ports resolve at
// collector time via the owning pod's container spec.
//
// SERVICE COMPOSITION. Phase 4.5 adds the canReachService helper that walks
// SELECTS edges from a Service to its backing pods and ORs the per-pod
// reachability results together. The public_exposure analyzer consumes this
// helper to stitch Pod → Service chains into multi-hop findings without
// duplicating selector logic.
//
// ADMIN NETWORK POLICY. Phase 5.5 honors AdminNetworkPolicy (ANP) priority
// ordering:
//   - ANP Allow overrides a NetworkPolicy Deny.
//   - ANP Deny overrides a NetworkPolicy Allow.
//   - ANP Pass falls through to standard NetworkPolicy evaluation.
//
// ANP edges live on dedicated edge types so the walker can evaluate them
// ahead of NetworkPolicy edges without a second graph pass.
//
// PERFORMANCE. Hard cap at 1000 pods per cluster. Exceeding the cap emits a
// single "reachability_skipped" notice Finding and returns without building
// the full reachability matrix. The O(P^2) matrix is otherwise tractable
// because edges are pre-resolved and the walker is a single hash-lookup per
// (src, dst) pair.
//
// FINDINGS. The analyzer emits classified findings:
//   - "reachability_isolated" — pod with no allowed peers in either direction
//   - "reachability_over_exposed" — pod reachable from entire namespace/cluster
//   - "reachability_asymmetric" — A→B allowed, B→A blocked
//   - "reachability_namespace_fully_open" — namespace with no default-deny
//   - "reachability_ipblock_world_exposed" — ingress from 0.0.0.0/0
//
// Plus ONE "reachability_matrix" raw-data finding carrying the computed
// (src, dst) table so downstream consumers (public_exposure) can reuse the
// result without re-running the analyzer.
//
// CONFIGURABLE FLAGS via Request.Extra:
//   - "k8s_emit_asymmetric" — values "info" (default) | "warning" | "suppress"
//     control how asymmetric reachability is surfaced, per the plan's user
//     decision on noise tuning.
//
// LAYERING. topology/ MUST NOT import cloud/k8s/ — all K8s-specific
// NetworkPolicy selector resolution lives in cloud/k8s/postpopulate*.go. The
// analyzer walks only generic graph edges and only re-parses NetworkPolicy
// node content via a local anonymous struct for ipBlock classification.
// topology/ also must not import codegraph/, thought/, or tools/ per the
// package's invariant.

// KubernetesReachabilityAnalyzer implements topology.Analyzer for K8s
// NetworkPolicy reachability analysis. Zero-value usable; classification
// rules are wired up in classifyReachability (Phase 4 fleshes it out).
type KubernetesReachabilityAnalyzer struct{}

// Name returns the analyzer's stable identifier. Findings emitted by Run
// carry this in their Algorithm field — downstream dedup and surfacing
// depend on the name staying stable across releases.
func (KubernetesReachabilityAnalyzer) Name() string { return "k8s_reachability" }

// Run executes the K8s NetworkPolicy reachability analyzer against a single
// cloud account. Behavior:
//
//   - req.Graph MUST be GraphCloud; any other graph type returns an
//     error. Reachability analysis is cloud-specific because it walks the
//     NetworkPolicy edges the K8s collector emits during postPopulate.
//   - req.Caller must not be nil.
//   - The analyzer binds a per-account cloud reader to req.Name. All
//     reachability queries run within a single account.
//   - buildReachabilityIndex walks the pods and pre-resolved NetworkPolicy
//     edges for the scoped graph and returns a lookup index keyed by
//     (src, dst, protocol, port). Exceeding the reachabilityPodCap hard
//     cap returns a sentinel (skipped=true) that classifyReachability
//     detects and surfaces as a single reachability_skipped notice
//     finding.
//   - classifyReachability turns the index into Findings. Phase 3 ships a
//     stub; Phase 4 fleshes out the real classification rules.
//   - req.TopK (when > 0) caps the number of returned findings.
//
// Supported req.Extra flags:
//
//   - "k8s_emit_asymmetric" — controls how asymmetric reachability findings
//     (A→B allowed but B→A blocked) surface. Accepted values:
//     "info" (default) emits findings at SeverityInfo,
//     "warning" bumps them to SeverityWarning,
//     "suppress" skips the asymmetric classifier entirely and emits zero
//     findings in that category.
//     Invalid values fall back to "info" with a single slog.Warn log line,
//     so misconfigured callers notice without the analyzer refusing to run.
//
// Emitted finding categories (see k8s_reachability_findings.go and sibling
// files for the classifier implementations):
//
//   - "reachability_isolated" — pod with no reachable peers in either direction
//   - "reachability_over_exposed" — pod reachable from every namespace peer
//   - "reachability_asymmetric" — A→B allowed, B→A blocked
//   - "reachability_namespace_fully_open" — namespace with no intra-NS narrowing
//   - "reachability_ipblock_world_exposed" — ingress from 0.0.0.0/0 etc.
//   - "reachability_matrix" — single raw-data finding downstream analyzers
//     query by name ("reachability_matrix") to reuse the computed matrix
//     without re-running this analyzer.
//
// Sub-classifiers that key off (protocol, port) set Metadata["protocol"] and
// Metadata["port"] on emitted findings when per-port distinctions matter; a
// pod-pair whose reachability result is uniform across the probe set emits a
// single port-less finding instead.
func (a KubernetesReachabilityAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/k8s_reachability: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/k8s_reachability: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/k8s_reachability: req.Caller must not be nil")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	index, err := buildReachabilityIndex(ctx, scoped)
	if err != nil {
		return nil, fmt.Errorf("topology/k8s_reachability: build index: %w", err)
	}

	findings, err := classifyReachability(ctx, req, scoped, index)
	if err != nil {
		return nil, err
	}

	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// classifyReachability lives in k8s_reachability_findings.go (Phase 4). It
// dispatches to sub-classifiers that key off the reachabilityIndex and the
// req.Extra["k8s_emit_asymmetric"] flag. When the index was short-circuited
// by the hard pod cap, classifyReachability returns a single reachability_skipped
// notice finding. The matrix emitter and the ipBlock world-exposure classifier
// are called from the same entry point.

// init self-registers the KubernetesReachabilityAnalyzer with the topology
// registry so callers (the dream topology phase, tests, and the `query` tool)
// can look it up by name without importing this file directly.
func init() {
	Register(KubernetesReachabilityAnalyzer{})
}
