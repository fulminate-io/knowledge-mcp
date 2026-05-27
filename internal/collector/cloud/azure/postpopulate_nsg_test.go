// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildNSGNode builds an NSG-shaped cloud resource node with the given raw
// JSON spec. The spec JSON must be the content body produced by
// json.Marshal(armnetwork.SecurityGroup).
func buildNSGNode(t *testing.T, spec string) *knowledgev1.Node {
	t.Helper()
	const nsgID = "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A"
	n := &knowledgev1.Node{
		Id:         nsgID,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "nsg-A",
		Content:    spec,
	}
	kgtypes.SetValue(n, "resource_type", "Microsoft.Network/networkSecurityGroups")
	return n
}

// collectNSGEdgeKeys turns a []knowledgev1.Edge into a map keyed by
// "from->to:type" for order-independent assertions. Values are pointers into
// the edges backing array (knowledgev1.Edge value-embeds the proto MessageState, so
// copying it by value is copylocks-forbidden).
func collectNSGEdgeKeys(edges []knowledgev1.Edge) map[string]*knowledgev1.Edge {
	out := make(map[string]*knowledgev1.Edge, len(edges))
	for i := range edges {
		e := &edges[i]
		key := e.FromId + "->" + e.ToId + ":" + e.Type
		out[key] = e
	}
	return out
}

func TestResolveNSG_InboundAllowRule(t *testing.T) {
	nsgID := "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A"
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Allow",
					"direction": "Inbound",
					"protocol": "Tcp",
					"sourceAddressPrefix": "10.0.0.0/8",
					"destinationPortRange": "443"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "10.0.0.0/8", cidrs["azure:cidr:10.0.0.0/8"])

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, nsgID, e.FromId)
	assert.Equal(t, "azure:cidr:10.0.0.0/8", e.ToId)
	assert.Equal(t, methodAzureNSGRule, e.Method)
	assert.Contains(t, e.Evidence, `"protocol":"tcp"`)
	assert.Contains(t, e.Evidence, `"port_from":443`)
	assert.Contains(t, e.Evidence, `"port_to":443`)
	assert.Contains(t, e.Evidence, `"cidr":"10.0.0.0/8"`)
}

func TestResolveNSG_OutboundAllowRule(t *testing.T) {
	nsgID := "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A"
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Allow",
					"direction": "Outbound",
					"protocol": "Tcp",
					"destinationAddressPrefix": "0.0.0.0/0",
					"destinationPortRange": "443"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "0.0.0.0/0", cidrs["azure:cidr:0.0.0.0/0"])

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsEgressTo), e.Type)
	assert.Equal(t, nsgID, e.FromId)
	assert.Equal(t, "azure:cidr:0.0.0.0/0", e.ToId)
	assert.Contains(t, e.Evidence, `"egress":true`)
}

func TestResolveNSG_DenyRuleSkipped(t *testing.T) {
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Deny",
					"direction": "Inbound",
					"protocol": "Tcp",
					"sourceAddressPrefix": "10.0.0.0/8",
					"destinationPortRange": "22"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	assert.Empty(t, edges, "Deny rules should not produce edges")
	assert.Empty(t, cidrs)
}

func TestResolveNSG_MultipleRules(t *testing.T) {
	nsgID := "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A"
	spec := `{
		"properties": {
			"securityRules": [
				{
					"properties": {
						"access": "Allow",
						"direction": "Inbound",
						"protocol": "Tcp",
						"sourceAddressPrefix": "10.0.0.0/8",
						"destinationPortRange": "22"
					}
				},
				{
					"properties": {
						"access": "Allow",
						"direction": "Outbound",
						"protocol": "*",
						"destinationAddressPrefix": "0.0.0.0/0",
						"destinationPortRange": "*"
					}
				},
				{
					"properties": {
						"access": "Deny",
						"direction": "Inbound",
						"protocol": "Tcp",
						"sourceAddressPrefix": "192.168.0.0/16",
						"destinationPortRange": "3389"
					}
				}
			]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 2, "expected 2 edges (1 inbound Allow + 1 outbound Allow, Deny skipped)")
	require.Len(t, cidrs, 2)

	byKey := collectNSGEdgeKeys(edges)
	assert.Contains(t, byKey, nsgID+"->azure:cidr:10.0.0.0/8:ALLOWS_INGRESS_FROM")
	assert.Contains(t, byKey, nsgID+"->azure:cidr:0.0.0.0/0:ALLOWS_EGRESS_TO")
}

func TestResolveNSG_WildcardAddress(t *testing.T) {
	// Source address "*" normalizes to "0.0.0.0/0".
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Allow",
					"direction": "Inbound",
					"protocol": "Tcp",
					"sourceAddressPrefix": "*",
					"destinationPortRange": "80"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "0.0.0.0/0", cidrs["azure:cidr:0.0.0.0/0"])
	assert.Equal(t, "azure:cidr:0.0.0.0/0", edges[0].ToId)
}

func TestResolveNSG_EmptyContent(t *testing.T) {
	n := &knowledgev1.Node{
		Id:   "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A",
		Type: string(kgtypes.NodeCloudResource),
	}
	kgtypes.SetValue(n, "resource_type", "Microsoft.Network/networkSecurityGroups")
	edges, cidrs := buildNSGRuleEdges([]*knowledgev1.Node{n})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}

func TestResolveNSG_MalformedContent(t *testing.T) {
	n := &knowledgev1.Node{
		Id:      "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Network/networkSecurityGroups/nsg-A",
		Type:    string(kgtypes.NodeCloudResource),
		Content: "not json",
	}
	kgtypes.SetValue(n, "resource_type", "Microsoft.Network/networkSecurityGroups")
	edges, cidrs := buildNSGRuleEdges([]*knowledgev1.Node{n})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}

func TestResolveNSG_ProtocolNormalization(t *testing.T) {
	// Protocol "*" normalizes to empty string in metadata.
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Allow",
					"direction": "Inbound",
					"protocol": "*",
					"sourceAddressPrefix": "10.0.0.0/8",
					"destinationPortRange": "443"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, _ := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 1)
	// Protocol "*" normalizes to empty, so evidence should NOT contain protocol.
	assert.NotContains(t, edges[0].Evidence, `"protocol":"*"`)
}

func TestResolveNSG_AddressPrefixes(t *testing.T) {
	// sourceAddressPrefixes[] array (multiple CIDRs) -> one edge per CIDR.
	spec := `{
		"properties": {
			"securityRules": [{
				"properties": {
					"access": "Allow",
					"direction": "Inbound",
					"protocol": "Tcp",
					"sourceAddressPrefixes": ["10.0.0.0/8", "172.16.0.0/12"],
					"destinationPortRange": "443"
				}
			}]
		}
	}`
	nodes := []*knowledgev1.Node{buildNSGNode(t, spec)}
	edges, cidrs := buildNSGRuleEdges(nodes)

	require.Len(t, edges, 2, "one edge per CIDR in sourceAddressPrefixes")
	require.Len(t, cidrs, 2)
	assert.Equal(t, "10.0.0.0/8", cidrs["azure:cidr:10.0.0.0/8"])
	assert.Equal(t, "172.16.0.0/12", cidrs["azure:cidr:172.16.0.0/12"])
}
