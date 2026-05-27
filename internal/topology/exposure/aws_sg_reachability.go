// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// aws_sg_reachability.go implements the AWSSGReachabilityAnalyzer — a
// cloud-graph analyzer that computes resource↔resource reachability across an
// AWS account by walking the pre-resolved security-group, network-ACL, and
// cross-VPC edges emitted by the cloud/aws postpopulate phase, and classifies
// the results into findings.
//
// PURPOSE. Surface network-layer exposure problems (world-open privileged
// ports, transitive SG chains, wide public CIDR ranges, orphaned resources)
// without the operator having to hand-walk the SG rule graph. The analyzer is
// the AWS companion to KubernetesReachabilityAnalyzer — both emit the same
// shape of findings so downstream composers (public_exposure) can treat cloud
// and cluster reachability uniformly.
//
// EDGE MODEL. Agent-side collectors pre-resolve every SG rule, NACL entry,
// and cross-VPC SG reference into directional store edges at index time (the
// hybrid approach locked in the plan's OQ answer). This analyzer walks only
// the generic graph and never imports cloud/aws — the ONLY cloud-specific
// logic here is parsing edge Evidence via a local anonymous JSON struct so
// we can recover protocol, port range, CIDR, and is_nacl flags without
// reaching into the collector package. Edges consumed:
//
//   - EdgeAllowsIngressFrom     — SG → peer (SG or CIDR) for inbound rules
//   - EdgeAllowsEgressTo        — SG → peer (SG or CIDR) for outbound rules
//   - EdgeUsesSecurityGroup     — resource → SG attachment
//   - EdgeUsesSubnet            — resource → subnet membership
//   - EdgeAssociatedWithSubnet  — subnet → NACL association
//   - EdgePeeredWith            — VPC ↔ VPC via an active peering connection
//   - EdgeRoutesVia             — subnet/VPC → Transit Gateway attachment
//   - EdgeExposedVia            — service → VPC endpoint (PrivateLink)
//
// Edge metadata (Evidence) shape — a JSON object with fields {protocol,
// port_from, port_to, cidr, is_nacl, egress, rule_number}. Empty evidence
// is the canonical fully-open signal (the collector emits no JSON when a
// rule has no ports[] clause and no CIDR).
//
// SEMANTICS. Reachability for a (src, dst, protocol, port) tuple is
// evaluated as a series of three filters, ALL of which must allow the
// packet:
//
//  1. SG layer — src's SG egress rules must permit dst (or dst's SG
//     ingress rules must permit src, whichever the collector emitted). The
//     SG layer uses the EdgeAllowsIngressFrom / EdgeAllowsEgressTo edges
//     WITHOUT the is_nacl flag on their Evidence.
//  2. NACL layer — the subnet-scoped NACL edges (is_nacl=true) for both
//     the src subnet (egress) and the dst subnet (ingress) must permit the
//     packet. A packet leaving a subnet without a matching egress NACL ALLOW
//     entry is dropped; a packet entering a subnet without a matching
//     ingress NACL ALLOW entry is dropped.
//  3. Cross-VPC layer — when src and dst live in different VPCs, a peering
//     (EdgePeeredWith), Transit Gateway attachment (EdgeRoutesVia), or
//     PrivateLink endpoint (EdgeExposedVia) must connect the two VPCs. The
//     collector already filters cross-VPC SG references against active
//     peerings, so the edge's existence is sufficient proof.
//
// Stateful return traffic is implicit: AWS SGs are stateful, so a reply to
// an allowed connection does not need its own reverse rule. The analyzer
// only evaluates the initiating direction.
//
// PERFORMANCE. Hard cap at sgReachabilityResourceCap resources per account
// (5000 matches the plan decision). Exceeding the cap emits a single
// reachability_skipped notice Finding and returns without building the full
// index. Classifiers are edge-driven — none performs a quadratic pair walk.
// The matrix emitter iterates (src, dst, port) pairs but short-circuits at
// matrixMaxEntries.
//
// FINDINGS. The analyzer emits classified findings:
//
//   - aws_sg_world_open_privileged — 0.0.0.0/0 ingress on SSH/RDP/DB ports
//   - aws_sg_transitive_chain      — SG A → SG B → SG C escalation paths
//   - aws_sg_wide_cidr             — non-0.0.0.0/0 but wide public CIDR
//   - aws_sg_isolated              — resource with no ingress or egress
//
// Plus one aws_sg_reachability_matrix raw-data finding carrying the computed
// (src, dst, proto, port, via) table so downstream consumers (public_exposure)
// can reuse the result without re-running the analyzer.
//
// CONFIGURABLE FLAGS via Request.Extra:
//   - "emit_matrix" — "false" skips the matrix emitter (useful on large
//     accounts where even the truncated matrix is wasteful).
//
// LAYERING. topology/ MUST NOT import cloud/aws/ — all AWS-specific rule
// resolution lives in cloud/aws/postpopulate*.go. The analyzer walks only
// generic graph edges and re-parses edge Evidence via a LOCAL anonymous
// struct for SG rule metadata. topology/ also must not import codegraph/,
// thought/, or tools/ per the package's invariant.

// AWSSGReachabilityAnalyzer implements topology.Analyzer for AWS Security
// Group + NACL + cross-VPC reachability. Zero-value usable; classification
// rules live in classifyAWSSGReachability.
type AWSSGReachabilityAnalyzer struct{}

// Name returns the analyzer's stable identifier. Findings emitted by Run
// carry this in their Algorithm field — downstream dedup and surfacing
// depend on the name staying stable across releases.
func (AWSSGReachabilityAnalyzer) Name() string { return "aws_sg_reachability" }

// Run executes the AWS SG reachability analyzer against a single cloud
// account. Behavior:
//
//   - req.Graph MUST be GraphCloud; any other graph type returns an
//     error. Reachability analysis is cloud-specific because it walks the
//     SG / NACL / peering edges the AWS collector emits during postPopulate.
//   - req.Caller must not be nil.
//   - Run binds a per-account cloud reader to req.Name. All reachability
//     queries run within a single account.
//   - buildSGReachabilityIndex walks the scoped graph and returns a lookup
//     index keyed by (src, dst, protocol, port). Exceeding the hard cap
//     returns a sentinel (skipped=true) that classifyAWSSGReachability
//     detects and surfaces as a single reachability_skipped notice
//     finding.
//   - classifyAWSSGReachability turns the index into Findings.
//   - Findings are sorted deterministically (critical → info, then by
//     primary evidence) and capped by req.TopK when non-zero.
func (a AWSSGReachabilityAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/aws_sg_reachability: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability: req.Caller must not be nil")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	index, err := buildSGReachabilityIndex(ctx, scoped)
	if err != nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability: build index: %w", err)
	}

	findings, err := classifyAWSSGReachability(ctx, req, index)
	if err != nil {
		return nil, err
	}

	sortSGReachabilityFindings(findings)
	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// sortSGReachabilityFindings orders findings deterministically: severity
// (critical → info), then by first evidence ID, then by title. Reuses the
// package-level severityOrder map so the ordering contract matches every
// other reachability analyzer.
func sortSGReachabilityFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		si := severityOrder[findings[i].Severity]
		sj := severityOrder[findings[j].Severity]
		if si != sj {
			return si < sj
		}
		pi := primaryEvidence(findings[i])
		pj := primaryEvidence(findings[j])
		if pi != pj {
			return pi < pj
		}
		return findings[i].Title < findings[j].Title
	})
}

// init self-registers the AWSSGReachabilityAnalyzer with the topology
// registry so callers (the dream topology phase, tests, and the `query`
// tool) can look it up by name without importing this file directly.
func init() {
	Register(AWSSGReachabilityAnalyzer{})
}
