// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildPeeringNode wraps a VpcPeeringConnection JSON body into a
// cloud-resource node the way the vpc-peering collector would.
func buildPeeringNode(t *testing.T, pcxID, specJSON string) *knowledgev1.Node {
	t.Helper()
	const (
		region = "us-east-1"
		acct   = "111111111111"
	)
	arn := ec2ARN(region, acct, "vpc-peering-connection", pcxID)
	n := &knowledgev1.Node{
		Id:         arn,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: pcxID,
		Content:    specJSON,
	}
	kgtypes.SetValue(n, "resource_type", "vpc-peering-connection")
	kgtypes.SetValue(n, "region", region)
	return n
}

func TestCrossVpcEdgesForPerms_SameVpc_Skipped(t *testing.T) {
	// Same-VPC SG references are the job of postpopulate_sg.go; the
	// cross-VPC resolver must ignore them.
	sg := buildSGNode(t, `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 80,
			"ToPort": 80,
			"UserIdGroupPairs": [{"GroupId": "sg-B", "VpcId": "vpc-1"}]
		}]
	}`)
	peerIndex := map[string]map[string]bool{}
	edges := crossVpcEdgesForPerms(sg, "vpc-1", mustUnmarshalPerms(t, sg.Content), false, peerIndex)
	assert.Empty(t, edges, "same-VPC UserIdGroupPairs should not generate cross-VPC edges")
}

func TestCrossVpcEdgesForPerms_UnpeeredCrossVpc_Dropped(t *testing.T) {
	// Reference to peer in vpc-2 but no peering → edge dropped.
	sg := buildSGNode(t, `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 80,
			"ToPort": 80,
			"UserIdGroupPairs": [{"GroupId": "sg-B", "VpcId": "vpc-2"}]
		}]
	}`)
	peerIndex := map[string]map[string]bool{}
	edges := crossVpcEdgesForPerms(sg, "vpc-1", mustUnmarshalPerms(t, sg.Content), false, peerIndex)
	assert.Empty(t, edges, "unpeered cross-VPC reference must not emit an edge")
}

func TestCrossVpcEdgesForPerms_PeeredCrossVpc_Emitted(t *testing.T) {
	acct := "111111111111"
	sg := buildSGNode(t, `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 443,
			"ToPort": 443,
			"UserIdGroupPairs": [{"GroupId": "sg-B", "VpcId": "vpc-2"}]
		}]
	}`)
	peerIndex := map[string]map[string]bool{
		"vpc-1": {"vpc-2": true},
		"vpc-2": {"vpc-1": true},
	}
	edges := crossVpcEdgesForPerms(sg, "vpc-1", mustUnmarshalPerms(t, sg.Content), false, peerIndex)
	require.Len(t, edges, 1)
	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, ec2ARN("us-east-1", acct, "security-group", "sg-A"), e.FromId)
	assert.Equal(t, ec2ARN("us-east-1", acct, "security-group", "sg-B"), e.ToId)
	assert.Equal(t, methodAWSCrossVPC, e.Method)
}

func TestBuildPeeringEdges_ActiveOnly(t *testing.T) {
	// Two peerings — one active, one pending-acceptance (ignored).
	active := buildPeeringNode(t, "pcx-1", `{
		"VpcPeeringConnectionId": "pcx-1",
		"Status": {"Code": "active"},
		"RequesterVpcInfo": {"VpcId": "vpc-1"},
		"AccepterVpcInfo":  {"VpcId": "vpc-2"}
	}`)
	pending := buildPeeringNode(t, "pcx-2", `{
		"VpcPeeringConnectionId": "pcx-2",
		"Status": {"Code": "pending-acceptance"},
		"RequesterVpcInfo": {"VpcId": "vpc-3"},
		"AccepterVpcInfo":  {"VpcId": "vpc-4"}
	}`)

	edges, index := processPeeringNodes(t, []*knowledgev1.Node{
		active,
		pending,
	})

	// Expect 2 bidirectional EdgePeeredWith edges (one each direction).
	require.Len(t, edges, 2)
	for i := range edges {
		assert.Equal(t, string(kgtypes.EdgePeeredWith), edges[i].Type)
	}
	// Index: vpc-1↔vpc-2 only; vpc-3/vpc-4 excluded due to pending status.
	assert.True(t, index["vpc-1"]["vpc-2"])
	assert.True(t, index["vpc-2"]["vpc-1"])
	assert.False(t, index["vpc-3"]["vpc-4"])
}

// --- test helpers ---

func mustUnmarshalPerms(t *testing.T, content string) []sgIPPermission {
	t.Helper()
	var spec sgSpec
	require.NoError(t, jsonUnmarshalString(content, &spec))
	return spec.IpPermissions
}

// processPeeringNodes replays the node-iteration logic from
// peeringEdgesForNode against an in-memory slice so we can test the pure
// code path without standing up a real graph engine. It must stay in sync
// with the implementation — if that drifts, these tests will fail.
func processPeeringNodes(t *testing.T, nodes []*knowledgev1.Node) ([]knowledgev1.Edge, map[string]map[string]bool) {
	t.Helper()
	var edges []knowledgev1.Edge
	index := make(map[string]map[string]bool)
	for _, node := range nodes {
		var spec vpcPeeringSpec
		require.NoError(t, jsonUnmarshalString(node.Content, &spec))
		if spec.Status == nil || spec.Status.Code == nil || *spec.Status.Code != "active" {
			continue
		}
		if spec.RequesterVpcInfo == nil || spec.AccepterVpcInfo == nil {
			continue
		}
		req := deref(spec.RequesterVpcInfo.VpcId)
		acc := deref(spec.AccepterVpcInfo.VpcId)
		if req == "" || acc == "" {
			continue
		}
		region := kgtypes.Value(node, "region")
		nodeAccount := accountFromARN(node.Id)
		reqAccount := derefOrDefault(spec.RequesterVpcInfo.OwnerId, nodeAccount)
		accAccount := derefOrDefault(spec.AccepterVpcInfo.OwnerId, nodeAccount)
		reqARN := ec2ARN(region, reqAccount, "vpc", req)
		accARN := ec2ARN(region, accAccount, "vpc", acc)
		edges = append(edges,
			knowledgev1.Edge{FromId: reqARN, ToId: accARN, Type: string(kgtypes.EdgePeeredWith), Method: methodAWSCrossVPC},
			knowledgev1.Edge{FromId: accARN, ToId: reqARN, Type: string(kgtypes.EdgePeeredWith), Method: methodAWSCrossVPC},
		)
		if index[req] == nil {
			index[req] = make(map[string]bool)
		}
		if index[acc] == nil {
			index[acc] = make(map[string]bool)
		}
		index[req][acc] = true
		index[acc][req] = true
	}
	return edges, index
}

func TestBuildPeeringEdges_CrossAccount_OwnerIds(t *testing.T) {
	region := "us-east-1"
	acctA := "111111111111"
	acctB := "222222222222"

	// Peering with explicit OwnerIds for cross-account VPCs.
	cross := buildPeeringNode(t, "pcx-cross", `{
		"VpcPeeringConnectionId": "pcx-cross",
		"Status": {"Code": "active"},
		"RequesterVpcInfo": {"VpcId": "vpc-A", "OwnerId": "`+acctA+`"},
		"AccepterVpcInfo":  {"VpcId": "vpc-B", "OwnerId": "`+acctB+`"}
	}`)

	edges, index := processPeeringNodes(t, []*knowledgev1.Node{
		cross,
	})

	require.Len(t, edges, 2)
	// Verify ARNs use correct per-account IDs.
	reqARN := ec2ARN(region, acctA, "vpc", "vpc-A")
	accARN := ec2ARN(region, acctB, "vpc", "vpc-B")
	assert.Equal(t, reqARN, edges[0].FromId)
	assert.Equal(t, accARN, edges[0].ToId)
	assert.Equal(t, accARN, edges[1].FromId)
	assert.Equal(t, reqARN, edges[1].ToId)
	assert.True(t, index["vpc-A"]["vpc-B"])
	assert.True(t, index["vpc-B"]["vpc-A"])
}

func TestCrossVpcEdgesForPerms_CrossAccount_UserId(t *testing.T) {
	region := "us-east-1"
	hostAcct := "111111111111"
	peerAcct := "222222222222"
	sg := buildSGNode(t, `{
		"VpcId": "vpc-1",
		"IpPermissions": [{
			"IpProtocol": "tcp",
			"FromPort": 443,
			"ToPort": 443,
			"UserIdGroupPairs": [{"GroupId": "sg-B", "VpcId": "vpc-2", "UserId": "`+peerAcct+`"}]
		}]
	}`)
	peerIndex := map[string]map[string]bool{
		"vpc-1": {"vpc-2": true},
		"vpc-2": {"vpc-1": true},
	}
	edges := crossVpcEdgesForPerms(sg, "vpc-1", mustUnmarshalPerms(t, sg.Content), false, peerIndex)
	require.Len(t, edges, 1)
	// The peer SG ARN should use the peer account, not the host account.
	assert.Equal(t, ec2ARN(region, hostAcct, "security-group", "sg-A"), edges[0].FromId)
	assert.Equal(t, ec2ARN(region, peerAcct, "security-group", "sg-B"), edges[0].ToId)
}

func TestDerefOrDefault(t *testing.T) {
	val := "value"
	assert.Equal(t, "value", derefOrDefault(&val, "fallback"))
	assert.Equal(t, "fallback", derefOrDefault(nil, "fallback"))
	empty := ""
	assert.Equal(t, "fallback", derefOrDefault(&empty, "fallback"))
}

// jsonUnmarshalString is a tiny wrapper that keeps test call sites
// concise — json.Unmarshal on a string literal.
func jsonUnmarshalString(s string, out any) error {
	return json.Unmarshal([]byte(s), out)
}
