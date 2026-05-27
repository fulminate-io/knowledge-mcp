// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildSGNode is a test helper that builds a security-group-shaped
// cloud resource node with the given raw JSON spec. Region, account ID,
// and SG ID are hardcoded — every call site uses the same values, and
// unparam flagged the unused parameters. The spec JSON must be the
// content body produced by aws-sdk-go-v2's ec2types.SecurityGroup
// (IpPermissions etc).
func buildSGNode(t *testing.T, spec string) *knowledgev1.Node {
	t.Helper()
	const (
		region    = "us-east-1"
		accountID = "111111111111"
		sgID      = "sg-A"
	)
	arn := ec2ARN(region, accountID, "security-group", sgID)
	n := &knowledgev1.Node{
		Id:         arn,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: sgID,
		Content:    spec,
	}
	kgtypes.SetValue(n, "resource_type", "security-group")
	kgtypes.SetValue(n, "region", region)
	return n
}

// collectSGEdgeKeys turns a []knowledgev1.Edge into a map keyed by
// "from→to:type" for order-independent assertions.
func collectSGEdgeKeys(edges []knowledgev1.Edge) map[string]*knowledgev1.Edge {
	out := make(map[string]*knowledgev1.Edge, len(edges))
	for i := range edges {
		e := &edges[i]
		key := e.FromId + "→" + e.ToId + ":" + e.Type
		out[key] = e
	}
	return out
}

func TestResolveSG_SGToSGRule(t *testing.T) {
	// SG A ingress from SG B → expect EdgeAllowsIngressFrom from A to B.
	region := "us-east-1"
	acct := "111111111111"
	spec := `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 443,
			"ToPort": 443,
			"UserIdGroupPairs": [{"GroupId": "sg-B"}]
		}]
	}`
	nodes := []*knowledgev1.Node{buildSGNode(t, spec)}
	edges, cidrs := buildSGRuleEdges(nodes)

	assert.Empty(t, cidrs, "SG-to-SG rule should not create CIDR sentinels")
	require.Len(t, edges, 1, "exactly one ingress edge expected")

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, ec2ARN(region, acct, "security-group", "sg-A"), e.FromId)
	assert.Equal(t, ec2ARN(region, acct, "security-group", "sg-B"), e.ToId)
	assert.Equal(t, methodAWSSGRule, e.Method)
	assert.Contains(t, e.Evidence, `"protocol":"tcp"`)
	assert.Contains(t, e.Evidence, `"port_from":443`)
	assert.Contains(t, e.Evidence, `"port_to":443`)
}

func TestResolveSG_CidrRule(t *testing.T) {
	// SG A ingress from 0.0.0.0/0 → expect EdgeAllowsIngressFrom to a
	// sentinel CIDR node plus that sentinel in the cidrNodes map.
	region := "us-east-1"
	acct := "111111111111"
	spec := `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 22,
			"ToPort": 22,
			"IpRanges": [{"CidrIp": "0.0.0.0/0"}]
		}]
	}`
	nodes := []*knowledgev1.Node{buildSGNode(t, spec)}
	edges, cidrs := buildSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "0.0.0.0/0", cidrs["aws:cidr:0.0.0.0/0"])

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, ec2ARN(region, acct, "security-group", "sg-A"), e.FromId)
	assert.Equal(t, "aws:cidr:0.0.0.0/0", e.ToId)
	assert.Contains(t, e.Evidence, `"cidr":"0.0.0.0/0"`)
	assert.Contains(t, e.Evidence, `"port_from":22`)
}

func TestResolveSG_EgressRule(t *testing.T) {
	// SG A egress to SG B → expect EdgeAllowsEgressTo with egress:true.
	region := "us-east-1"
	acct := "111111111111"
	spec := `{
		"VpcId": "vpc-1",
		"IpPermissionsEgress": [{
			"IpProtocol": "-1",
			"UserIdGroupPairs": [{"GroupId": "sg-B"}]
		}]
	}`
	nodes := []*knowledgev1.Node{buildSGNode(t, spec)}
	edges, _ := buildSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsEgressTo), e.Type)
	assert.Equal(t, ec2ARN(region, acct, "security-group", "sg-A"), e.FromId)
	assert.Equal(t, ec2ARN(region, acct, "security-group", "sg-B"), e.ToId)
	assert.Contains(t, e.Evidence, `"egress":true`)
	// Protocol "-1" normalizes to empty string, so the evidence should
	// NOT contain a protocol field.
	assert.NotContains(t, e.Evidence, `"protocol":"-1"`)
}

func TestResolveSG_MultipleRules(t *testing.T) {
	// SG A: ingress 22 from CIDR, 443 from SG B, and 0/0 egress.
	region := "us-east-1"
	acct := "111111111111"
	spec := `{
		"VpcId": "vpc-1",
		"IpPermissions": [
			{
				"IpProtocol": "tcp",
				"FromPort": 22,
				"ToPort": 22,
				"IpRanges": [{"CidrIp": "10.0.0.0/8"}]
			},
			{
				"IpProtocol": "tcp",
				"FromPort": 443,
				"ToPort": 443,
				"UserIdGroupPairs": [{"GroupId": "sg-B"}]
			}
		],
		"IpPermissionsEgress": [{
			"IpProtocol": "-1",
			"IpRanges": [{"CidrIp": "0.0.0.0/0"}]
		}]
	}`
	nodes := []*knowledgev1.Node{buildSGNode(t, spec)}
	edges, cidrs := buildSGRuleEdges(nodes)

	require.Len(t, edges, 3, "expected 3 edges (2 ingress + 1 egress)")
	require.Len(t, cidrs, 2, "expected 2 distinct CIDR sentinels")
	assert.Equal(t, "10.0.0.0/8", cidrs["aws:cidr:10.0.0.0/8"])
	assert.Equal(t, "0.0.0.0/0", cidrs["aws:cidr:0.0.0.0/0"])

	byKey := collectSGEdgeKeys(edges)
	sgA := ec2ARN(region, acct, "security-group", "sg-A")
	sgB := ec2ARN(region, acct, "security-group", "sg-B")
	assert.Contains(t, byKey, sgA+"→aws:cidr:10.0.0.0/8:ALLOWS_INGRESS_FROM")
	assert.Contains(t, byKey, sgA+"→"+sgB+":ALLOWS_INGRESS_FROM")
	assert.Contains(t, byKey, sgA+"→aws:cidr:0.0.0.0/0:ALLOWS_EGRESS_TO")
}

func TestResolveSG_IPv6Range(t *testing.T) {
	spec := `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 443,
			"ToPort": 443,
			"Ipv6Ranges": [{"CidrIpv6": "::/0"}]
		}]
	}`
	nodes := []*knowledgev1.Node{buildSGNode(t, spec)}
	edges, cidrs := buildSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "::/0", cidrs["aws:cidr:::/0"])
	e := &edges[0]
	assert.Equal(t, "aws:cidr:::/0", e.ToId)
}

func TestResolveSG_EmptyContent(t *testing.T) {
	// Node with empty content should be skipped without error.
	n := &knowledgev1.Node{
		Id:   ec2ARN("us-east-1", "111111111111", "security-group", "sg-A"),
		Type: string(kgtypes.NodeCloudResource),
	}
	kgtypes.SetValue(n, "resource_type", "security-group")
	edges, cidrs := buildSGRuleEdges([]*knowledgev1.Node{n})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}

func TestResolveSG_MalformedContent(t *testing.T) {
	// Garbage content should be skipped without propagating the error.
	n := &knowledgev1.Node{
		Id:      ec2ARN("us-east-1", "111111111111", "security-group", "sg-A"),
		Type:    string(kgtypes.NodeCloudResource),
		Content: "not json",
	}
	kgtypes.SetValue(n, "resource_type", "security-group")
	edges, cidrs := buildSGRuleEdges([]*knowledgev1.Node{n})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}

func TestARNParseHelpers(t *testing.T) {
	arn := "arn:aws:ec2:us-east-2:222222222222:security-group/sg-X"
	assert.Equal(t, "us-east-2", regionFromARN(arn))
	assert.Equal(t, "222222222222", accountFromARN(arn))
}
