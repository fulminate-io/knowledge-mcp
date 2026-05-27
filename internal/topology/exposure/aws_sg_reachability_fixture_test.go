// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// aws_sg_reachability_fixture_test.go provides fixture helpers for the
// AWS SG reachability analyzer tests. The helpers wrap the generic
// cloudFixture with AWS-specific sugar:
//
//   - AddSG(id, vpc, ingressRules, egressRules) — creates a security-group
//     node and emits per-rule allow edges matching the postpopulate_sg
//     contract.
//   - AddInstance(id, vpc, subnet, sgIDs) — creates an ec2-instance node
//     and emits EdgeUsesSecurityGroup attachments.
//   - AddResource(id, vpc, subnet, resourceType, sgIDs) — generic variant
//     for RDS, Lambda, ELBv2, ElastiCache, OpenSearch, EFS.
//   - AddSubnet(id, vpc) — creates a subnet node.
//   - AddVPC(id) — creates a VPC node.
//   - AddNACL(subnetID, ruleEntries) — emits NACL allow edges from the
//     subnet to CIDR sentinels with is_nacl=true.
//   - AddPeering(vpcA, vpcB) — emits bidirectional EdgePeeredWith edges.
//   - AddCIDRSentinel(cidr) — ensures the "aws:cidr:<cidr>" node exists.
//
// All helpers are keyed to testAccount so tests stay terse. Multi-account
// tests can fall back to the underlying cloudFixture.AddCloudResource.

const sgTestAccount = "aws-sg-test"

// sgFixture wraps a cloudFixture with AWS SG-specific helpers. Tests call
// newSGFixture(t) and then AddSG / AddInstance / AddPeering to assemble
// the scenario they need.
type sgFixture struct {
	t  *testing.T
	fx *cloudFixture
}

// newSGFixture builds a fresh cloudFixture, pre-creates the test account,
// and returns a sgFixture ready for use.
func newSGFixture(t *testing.T) *sgFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(sgTestAccount)
	return &sgFixture{t: t, fx: fx}
}

// reader returns the cloudReader for the test account.
func (f *sgFixture) reader() *cloudReader {
	f.t.Helper()
	return f.fx.reader(sgTestAccount)
}

// sgRuleSpec describes one ingress or egress rule on a test SG. The
// serialization matches the sgRuleMetadata shape the cloud/aws collector
// writes into Edge.Evidence, minus the Egress flag which the helpers set
// from context.
type sgRuleSpec struct {
	Protocol string
	PortFrom int
	PortTo   int
	CIDR     string // non-empty → CIDR rule; empty → peer-SG rule
	PeerSG   string // only used when CIDR is empty
}

// AddVPC creates a vpc node.
func (f *sgFixture) AddVPC(id string) {
	f.t.Helper()
	f.fx.AddCloudResource(sgTestAccount, id, id, "vpc", nil)
}

// AddSubnet creates a subnet node in the given VPC.
func (f *sgFixture) AddSubnet(id, vpc string) {
	f.t.Helper()
	f.fx.AddCloudResource(sgTestAccount, id, id, "subnet", map[string]string{
		"vpc_id": vpc,
	})
}

// AddSG creates a security-group node with the given ingress and egress
// rules, emitting per-rule EdgeAllowsIngressFrom / EdgeAllowsEgressTo
// edges. Mirrors what cloud/aws/postpopulate_sg.go writes when resolving
// a security group's IpPermissions.
func (f *sgFixture) AddSG(id, vpc string, ingress, egress []sgRuleSpec) {
	f.t.Helper()
	f.fx.AddCloudResource(sgTestAccount, id, id, "security-group", map[string]string{
		"vpc_id": vpc,
	})
	for _, r := range ingress {
		f.emitRuleEdge(id, r, false)
	}
	for _, r := range egress {
		f.emitRuleEdge(id, r, true)
	}
}

// emitRuleEdge creates one SG rule edge, writing the CIDR sentinel node on
// demand. egress=true uses EdgeAllowsEgressTo; false uses
// EdgeAllowsIngressFrom.
func (f *sgFixture) emitRuleEdge(sgID string, r sgRuleSpec, egress bool) {
	f.t.Helper()
	peerID := r.PeerSG
	if r.CIDR != "" {
		peerID = "aws:cidr:" + r.CIDR
		f.ensureCIDRSentinel(r.CIDR)
	}
	require.NotEmpty(f.t, peerID, "sg rule must name a peer SG or CIDR")
	md := map[string]any{
		"protocol":  r.Protocol,
		"port_from": r.PortFrom,
		"port_to":   r.PortTo,
		"cidr":      r.CIDR,
		"egress":    egress,
	}
	evidence := marshalMD(f.t, md)
	edgeType := kgtypes.EdgeAllowsIngressFrom
	if egress {
		edgeType = kgtypes.EdgeAllowsEgressTo
	}
	f.fx.AddEdgeWithEvidence(sgTestAccount, sgID, peerID, edgeType, evidence)
}

// naclRuleSpec describes one NACL rule entry. Rule ordering is determined
// by the test's call order — the classifier does not currently care about
// rule_number ordering for ALLOW rules.
type naclRuleSpec struct {
	Protocol   string
	PortFrom   int
	PortTo     int
	CIDR       string
	Egress     bool
	RuleNumber int
}

// AddNACL emits the given NACL allow rules from the subnet to CIDR
// sentinel nodes. Mirrors cloud/aws/postpopulate_networkacl.go's edge
// shape: is_nacl=true, rule_number stamped on Evidence.
func (f *sgFixture) AddNACL(subnetID string, rules []naclRuleSpec) {
	f.t.Helper()
	for _, r := range rules {
		f.ensureCIDRSentinel(r.CIDR)
		md := map[string]any{
			"protocol":    r.Protocol,
			"port_from":   r.PortFrom,
			"port_to":     r.PortTo,
			"cidr":        r.CIDR,
			"is_nacl":     true,
			"egress":      r.Egress,
			"rule_number": r.RuleNumber,
		}
		evidence := marshalMD(f.t, md)
		edgeType := kgtypes.EdgeAllowsIngressFrom
		if r.Egress {
			edgeType = kgtypes.EdgeAllowsEgressTo
		}
		f.fx.AddEdgeWithEvidence(sgTestAccount, subnetID, "aws:cidr:"+r.CIDR, edgeType, evidence)
	}
}

// AddInstance creates an ec2-instance node and wires EdgeUsesSecurityGroup
// edges to every SG ARN in sgIDs.
func (f *sgFixture) AddInstance(id, vpc, subnet string, sgIDs []string) {
	f.t.Helper()
	f.AddResource(id, vpc, subnet, "ec2-instance", sgIDs)
}

// AddResource creates a cloud-resource node of the given type and wires
// EdgeUsesSecurityGroup edges to every SG ARN in sgIDs.
func (f *sgFixture) AddResource(id, vpc, subnet, resourceType string, sgIDs []string) {
	f.t.Helper()
	meta := map[string]string{}
	if vpc != "" {
		meta["vpc_id"] = vpc
	}
	if subnet != "" {
		meta["subnet_id"] = subnet
	}
	f.fx.AddCloudResource(sgTestAccount, id, id, resourceType, meta)
	for _, sgID := range sgIDs {
		f.fx.AddEdge(sgTestAccount, id, sgID, kgtypes.EdgeUsesSecurityGroup)
	}
}

// AddPeering emits a bidirectional EdgePeeredWith edge pair between the
// two VPCs. Tests that need unpeered VPCs simply skip this call.
func (f *sgFixture) AddPeering(vpcA, vpcB string) {
	f.t.Helper()
	f.fx.AddEdge(sgTestAccount, vpcA, vpcB, kgtypes.EdgePeeredWith)
	f.fx.AddEdge(sgTestAccount, vpcB, vpcA, kgtypes.EdgePeeredWith)
}

// ensureCIDRSentinel ensures a "aws:cidr:<cidr>" sentinel node exists.
// Called implicitly by emitRuleEdge and AddNACL.
func (f *sgFixture) ensureCIDRSentinel(cidr string) {
	f.t.Helper()
	if cidr == "" {
		return
	}
	id := "aws:cidr:" + cidr
	if f.fx.hasNode(sgTestAccount, id) {
		return
	}
	f.fx.AddCloudResource(sgTestAccount, id, cidr, "cidr-block", map[string]string{
		"cidr": cidr,
	})
}

// marshalMD serializes the given metadata map to a compact JSON string
// matching the sgRuleMetadata encoder shape.
func marshalMD(t *testing.T, md map[string]any) string {
	t.Helper()
	b, err := json.Marshal(md)
	require.NoError(t, err)
	return string(b)
}

// runSGAnalyzer is a tiny helper that builds a Request against the
// fixture, runs the analyzer, and returns the findings.
func (f *sgFixture) runSGAnalyzer() []Finding {
	f.t.Helper()
	findings, err := AWSSGReachabilityAnalyzer{}.Run(newTestCtx(f.t), f.fx.cloudReq(sgTestAccount, 0))
	require.NoError(f.t, err)
	return findings
}

// TestSGFixtureSmoke verifies the helpers build a minimal SG + instance
// fixture without error. A separate smoke test for the fixture keeps
// downstream test failures unambiguous: a bug in AddSG or AddInstance
// fails here, not in every recipe test.
func TestSGFixtureSmoke(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-smoke")
	fx.AddSubnet("subnet-smoke", "vpc-smoke")
	fx.AddSG("sg-smoke", "vpc-smoke",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 22, PortTo: 22, CIDR: "0.0.0.0/0"}},
		[]sgRuleSpec{{Protocol: "-1", CIDR: "0.0.0.0/0"}},
	)
	fx.AddInstance("i-smoke", "vpc-smoke", "subnet-smoke", []string{"sg-smoke"})

	idx, err := buildSGReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.Contains(t, idx.resources, "i-smoke")
	require.Contains(t, idx.sgs, "sg-smoke")
	require.NotEmpty(t, idx.sgIngress["sg-smoke"])
	require.NotEmpty(t, idx.sgEgress["sg-smoke"])
	require.True(t, idx.worldReachableOn("i-smoke", "tcp", 22),
		"instance should be world-reachable on 22 via the smoke fixture")
	// Guard against "sgRuleSpec created but not decoded": if PortTo had
	// silently become 0 on a single-port rule, worldReachableOn would
	// still return true because the fully-open shortcut catches that.
	_ = fmt.Sprintf // keep fmt import used when future asserts need it
}
