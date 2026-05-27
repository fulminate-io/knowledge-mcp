// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildNACLNode wraps a raw NACL JSON body into the cloud-resource
// node shape the resolver expects. ARN convention matches the NACL
// subcollector in networkacl.go. Region, account ID, and NACL ID are
// hardcoded — every call site uses the same values, and unparam flagged
// the unused parameters.
func buildNACLNode(t *testing.T, specJSON string) *knowledgev1.Node {
	t.Helper()
	const (
		region    = "us-east-1"
		accountID = "111111111111"
		naclID    = "acl-1"
	)
	arn := ec2ARN(region, accountID, "network-acl", naclID)
	n := &knowledgev1.Node{
		Id:         arn,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: naclID,
		Content:    specJSON,
	}
	kgtypes.SetValue(n, "resource_type", "network-acl")
	kgtypes.SetValue(n, "region", region)
	return n
}

// Subnet→NACL association and NACL→VPC edges are emitted at collect-time
// in networkACLLocalEdges (cmd/knowledge/internal/collector/cloud/aws/networkacl.go); the
// postpopulate buildNACLEdges path now produces only the rule-derived
// ALLOWS edges plus their CIDR sentinels.

func TestResolveNACL_AllowIngressRule(t *testing.T) {
	region := "us-east-1"
	acct := "111111111111"
	spec := `{
		"NetworkAclId": "acl-1",
		"VpcId": "vpc-1",
		"Associations": [
			{"SubnetId": "subnet-a", "NetworkAclId": "acl-1"}
		],
		"Entries": [
			{"RuleNumber": 100, "Protocol": "6", "RuleAction": "allow", "Egress": false, "CidrBlock": "10.0.0.0/8", "PortRange": {"From": 443, "To": 443}}
		]
	}`
	nodes := []*knowledgev1.Node{buildNACLNode(t, spec)}
	edges, cidrs := buildNACLEdges(nodes)

	require.Len(t, cidrs, 1)
	assert.Equal(t, "10.0.0.0/8", cidrs["aws:cidr:10.0.0.0/8"])
	// Only the rule-derived ALLOWS edge — association edges live in collect.
	require.Len(t, edges, 1)

	ruleEdge := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), ruleEdge.Type)
	subnetA := ec2ARN(region, acct, "subnet", "subnet-a")
	assert.Equal(t, subnetA, ruleEdge.FromId)
	assert.Equal(t, "aws:cidr:10.0.0.0/8", ruleEdge.ToId)
	assert.Equal(t, methodAWSNACL, ruleEdge.Method)
	assert.Contains(t, ruleEdge.Evidence, `"is_nacl":true`)
	assert.Contains(t, ruleEdge.Evidence, `"rule_number":100`)
	assert.Contains(t, ruleEdge.Evidence, `"protocol":"tcp"`)
	assert.Contains(t, ruleEdge.Evidence, `"port_from":443`)
}

func TestResolveNACL_DenyRuleSkipped(t *testing.T) {
	// Two rules — a deny at priority 50 and an allow at priority 100.
	// The resolver must NOT emit the deny.
	spec := `{
		"NetworkAclId": "acl-1",
		"VpcId": "vpc-1",
		"Associations": [
			{"SubnetId": "subnet-a", "NetworkAclId": "acl-1"}
		],
		"Entries": [
			{"RuleNumber": 50, "Protocol": "-1", "RuleAction": "deny", "Egress": false, "CidrBlock": "203.0.113.0/24"},
			{"RuleNumber": 100, "Protocol": "-1", "RuleAction": "allow", "Egress": false, "CidrBlock": "0.0.0.0/0"}
		]
	}`
	nodes := []*knowledgev1.Node{buildNACLNode(t, spec)}
	edges, cidrs := buildNACLEdges(nodes)

	// Expected: just the allow rule edge (no deny, no association).
	require.Len(t, edges, 1)
	assert.Contains(t, cidrs, "aws:cidr:0.0.0.0/0")
	assert.NotContains(t, cidrs, "aws:cidr:203.0.113.0/24")
}

func TestResolveNACL_EgressRule(t *testing.T) {
	spec := `{
		"NetworkAclId": "acl-1",
		"VpcId": "vpc-1",
		"Associations": [
			{"SubnetId": "subnet-a", "NetworkAclId": "acl-1"}
		],
		"Entries": [
			{"RuleNumber": 100, "Protocol": "-1", "RuleAction": "allow", "Egress": true, "CidrBlock": "0.0.0.0/0"}
		]
	}`
	nodes := []*knowledgev1.Node{buildNACLNode(t, spec)}
	edges, _ := buildNACLEdges(nodes)

	var ruleEdge *knowledgev1.Edge
	for i := range edges {
		if kgtypes.EdgeType(edges[i].Type) == kgtypes.EdgeAllowsEgressTo {
			ruleEdge = &edges[i]
		}
	}
	require.NotNil(t, ruleEdge)
	require.Equal(t, string(kgtypes.EdgeAllowsEgressTo), ruleEdge.Type)
	assert.Contains(t, ruleEdge.Evidence, `"egress":true`)
}

func TestResolveNACL_RuleOrdering(t *testing.T) {
	// Insert entries out of rule-number order; resolver sorts them.
	spec := `{
		"NetworkAclId": "acl-1",
		"VpcId": "vpc-1",
		"Associations": [
			{"SubnetId": "subnet-a", "NetworkAclId": "acl-1"}
		],
		"Entries": [
			{"RuleNumber": 200, "Protocol": "-1", "RuleAction": "allow", "Egress": false, "CidrBlock": "10.2.0.0/16"},
			{"RuleNumber": 100, "Protocol": "-1", "RuleAction": "allow", "Egress": false, "CidrBlock": "10.1.0.0/16"}
		]
	}`
	nodes := []*knowledgev1.Node{buildNACLNode(t, spec)}
	edges, _ := buildNACLEdges(nodes)

	// Filter to rule edges (ignore the association edge).
	var rules []*knowledgev1.Edge
	for i := range edges {
		if kgtypes.EdgeType(edges[i].Type) == kgtypes.EdgeAllowsIngressFrom {
			rules = append(rules, &edges[i])
		}
	}
	require.Len(t, rules, 2)
	// First emitted edge should correspond to rule 100 (lower priority).
	assert.Contains(t, rules[0].Evidence, `"rule_number":100`)
	assert.Contains(t, rules[1].Evidence, `"rule_number":200`)
}

func TestResolveNACL_ProtocolMapping(t *testing.T) {
	cases := map[string]string{
		"":   "",
		"-1": "",
		"1":  "icmp",
		"6":  "tcp",
		"17": "udp",
		"58": "icmpv6",
		"99": "99",
	}
	for in, want := range cases {
		p := in
		got := naclProtocolName(&p)
		assert.Equal(t, want, got, "in=%q", in)
	}
	// nil pointer → empty
	assert.Empty(t, naclProtocolName(nil))
}
