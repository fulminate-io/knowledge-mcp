// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// aws_sg_reachability_index_types.go holds the type declarations the AWS
// SG reachability index materializes. Extracted from
// aws_sg_reachability_index.go to keep both files under the 300-line soft
// cap. Behavior lives in aws_sg_reachability_index.go (build/walk).

// sgReachabilityResourceCap is the hard cap on resources per account.
// Exceeding the cap short-circuits index construction. Declared as a var
// (not const) so tests can lower it to exercise the sentinel path without
// allocating 5001 fixture nodes.
var sgReachabilityResourceCap = 5000

// sgEdgeMetadata is the LOCAL anonymous JSON struct mirroring the
// cloud/aws sgRuleMetadata schema. Kept in this package so topology/ does
// not import cloud/aws/. Field tags must stay in sync with the collector's
// encoder or metadata decoding silently breaks.
type sgEdgeMetadata struct {
	Protocol   string `json:"protocol,omitempty"`
	PortFrom   int    `json:"port_from,omitempty"`
	PortTo     int    `json:"port_to,omitempty"`
	CIDR       string `json:"cidr,omitempty"`
	IsNACL     bool   `json:"is_nacl,omitempty"`
	Egress     bool   `json:"egress,omitempty"`
	RuleNumber int    `json:"rule_number,omitempty"`
}

// parseSGEdgeMetadata decodes an edge's Evidence field into sgEdgeMetadata.
// An empty string is the canonical fully-open signal (the collector emits
// no JSON when the rule has no ports[] clause and no CIDR) and yields a
// zero-value sgEdgeMetadata that matches any query.
func parseSGEdgeMetadata(evidence string) sgEdgeMetadata {
	if evidence == "" {
		return sgEdgeMetadata{}
	}
	var meta sgEdgeMetadata
	if err := json.Unmarshal([]byte(evidence), &meta); err != nil {
		return sgEdgeMetadata{}
	}
	return meta
}

// allowedRange is the decoded form of one SG or NACL allow rule: protocol
// plus a numeric port range plus (optionally) a CIDR the rule scoped to.
// Zero PortFrom and PortTo mean "all ports". Empty Protocol means "any
// protocol". Empty CIDR means "rule is SG-peer-scoped, not CIDR-scoped".
type allowedRange struct {
	Protocol string
	PortFrom int
	PortTo   int
	CIDR     string
}

// resourceInfo holds the per-resource state the reachability filter
// functions consult: the resource identity, its VPC + subnet, and the
// list of SG ARNs the resource attaches to. Mirrors the K8s podInfo
// shape so downstream code composing cloud + cluster reachability stays
// uniform.
type resourceInfo struct {
	// ID is the resource's ARN — the same key used by index.resources.
	ID string

	// Type is the resource_type metadata value (ec2-instance, rds-instance,
	// lambda-function, elbv2-load-balancer, elasticache-cluster,
	// opensearch-domain, efs-file-system). Classifiers key off this to
	// pick severities per-attachment-type.
	Type string

	// VPC is the VPC identifier the resource lives in, read from the
	// node's "vpc_id" metadata (may be the bare ID like "vpc-123" or an
	// ARN, depending on the collector). Empty for VPC-less resources.
	VPC string

	// Subnet is the primary subnet identifier the resource attaches to.
	// Used as the NACL layer lookup key. Empty when the resource is not
	// subnet-scoped.
	Subnet string

	// SGs holds the ARNs of the security groups the resource attaches to
	// via EdgeUsesSecurityGroup. Order is stable (sorted by ARN) so
	// downstream output is deterministic.
	SGs []string

	// AllowsIngressFrom maps peer ID (SG ARN or CIDR sentinel) → list of
	// allowed ranges. Aggregated across all SGs attached to this
	// resource. Consulted by ingressSGAllows when evaluating the ingress leg.
	//
	// Retained alongside the pre-built ingressBySGPeer / ingressCIDRIndex
	// because the world-open and wide-CIDR classifiers walk it directly
	// to enumerate CIDR sentinels with their original peer IDs (the
	// pre-built index drops peer keys for the CIDR side because the
	// reachability hot path only needs port/protocol matching).
	AllowsIngressFrom map[string][]allowedRange

	// AllowsEgressTo maps peer ID → list of allowed ranges. Aggregated
	// across all SGs attached to this resource. Retained for the same
	// reason as AllowsIngressFrom — the isolated-resource classifier
	// reads it to detect zero-rule resources.
	AllowsEgressTo map[string][]allowedRange

	// ingressBySGPeer is the per-SG-peer pre-indexed lookup consulted by
	// ingressSGAllows. Keyed by peer SG ARN; each value is an
	// sgPortIndex over the rules that name that peer. Replaces the
	// per-call walk over AllowsIngressFrom[srcSG]. Empty when the
	// resource has no SG-keyed ingress rules.
	ingressBySGPeer map[string]*sgPortIndex

	// ingressCIDRIndex is the pre-indexed lookup over the union of
	// CIDR-keyed ingress rules across every attached SG. Replaces the
	// map iteration in ingressSGAllows that picked out CIDR sentinels.
	// Zero-value when the resource has no CIDR ingress rules.
	ingressCIDRIndex sgPortIndex

	// egressBySGPeer is the egress counterpart to ingressBySGPeer —
	// keyed by peer SG ARN, each entry is an sgPortIndex.
	egressBySGPeer map[string]*sgPortIndex

	// egressCIDRIndex is the egress counterpart to ingressCIDRIndex.
	egressCIDRIndex sgPortIndex

	// hasEgressRules is true when at least one egress entry was added
	// to either egressBySGPeer or egressCIDRIndex. The default-allow
	// fast path in egressSGAllows keys off this flag instead of
	// re-deriving "len(AllowsEgressTo) == 0" — preserves the existing
	// AWS-default-allow semantics without iterating maps.
	hasEgressRules bool

	// hasIngressRules is the ingress counterpart — used by
	// ingressSGAllows to fast-fail (default-deny) without iterating.
	hasIngressRules bool
}

// sgReachabilityIndex is the per-account lookup structure the analyzer
// walks during classification. Zero-valued fields mean "not yet
// populated"; the skipped flag short-circuits all downstream processing.
type sgReachabilityIndex struct {
	// resources maps resource ARN → resourceInfo. Populated on normal
	// builds; nil when skipped is true.
	resources map[string]*resourceInfo

	// sgs caches security-group nodes so classifiers can cite SGs in
	// findings without re-querying the scoped graph. Keyed by SG ARN.
	sgs map[string]*knowledgev1.Node

	// sgIngress maps SG ARN → list of peer ID + allowed range entries
	// for every inbound allow edge rooted at that SG. Populated by
	// walking EdgeAllowsIngressFrom edges with Evidence is_nacl=false.
	sgIngress map[string][]sgAllowEntry

	// sgEgress maps SG ARN → list of peer ID + allowed range entries
	// for every outbound allow edge rooted at that SG.
	sgEgress map[string][]sgAllowEntry

	// naclIngress maps subnet ARN → list of NACL allow entries
	// (is_nacl=true) on the ingress direction.
	naclIngress map[string][]sgAllowEntry

	// naclEgress maps subnet ARN → list of NACL allow entries on the
	// egress direction.
	naclEgress map[string][]sgAllowEntry

	// vpcPeerings maps VPC ID → set of peer VPC IDs reachable via an
	// active peering connection, TGW attachment, or endpoint.
	vpcPeerings map[string]map[string]bool

	// skipped is true when the builder short-circuited because the
	// resource count exceeded sgReachabilityResourceCap.
	skipped bool

	// resourceCount is the total number of reachability-eligible
	// resources seen in the scoped graph.
	resourceCount int
}

// sgAllowEntry is one decoded allow edge: the peer endpoint and the
// parsed metadata. Classifiers iterate the per-SG slices directly rather
// than walking the raw edge list a second time.
type sgAllowEntry struct {
	// PeerID is the edge's ToID — either another SG's ARN or a CIDR
	// sentinel node ID of the form "aws:cidr:<cidr>".
	PeerID string
	// Range is the parsed port/protocol/CIDR envelope.
	Range allowedRange
	// IsNACL is true when the underlying edge Evidence had is_nacl=true.
	IsNACL bool
}

// sgReachabilityResourceTypes is the set of resource_type values that
// can attach to a security group. Used to filter the scoped graph's
// cloud resources down to the analyzer's universe.
var sgReachabilityResourceTypes = map[string]bool{
	"ec2-instance":        true,
	"rds-instance":        true,
	"rds-cluster":         true,
	"lambda-function":     true,
	"elbv2-load-balancer": true,
	"elasticache-cluster": true,
	"opensearch-domain":   true,
	"efs-file-system":     true,
}
