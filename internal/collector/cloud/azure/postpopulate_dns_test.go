// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestResolveDNSRecordTargets_IPRewrite(t *testing.T) {
	// Simulate: DNS A record ROUTES_TO raw IP, LB has matching frontend IP.
	ipIndex := map[string]string{
		"10.0.0.1": "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/loadBalancers/lb1",
	}

	recordID := "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/dnsZones/example.com/A/app"
	toID := "10.0.0.1"

	// Not already an Azure resource ID.
	assert.False(t, isAzureResourceID(toID))

	// Matches in index.
	resolved := ipIndex[toID]
	assert.Equal(t, "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/loadBalancers/lb1", resolved)

	// Construct new edge.
	newEdge := knowledgev1.Edge{
		FromId:   recordID,
		ToId:     resolved,
		Type:     string(kgtypes.EdgeRoutesTo),
		Method:   "postpopulate:dns-resolve",
		Evidence: toID,
	}
	assert.Equal(t, recordID, newEdge.FromId)
	assert.Contains(t, newEdge.ToId, "loadBalancers/lb1")
}

func TestResolveDNSRecordTargets_AlreadyResolved(t *testing.T) {
	toID := "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/loadBalancers/lb1"
	assert.True(t, isAzureResourceID(toID))
}

func TestResolveDNSRecordTargets_NoMatch(t *testing.T) {
	ipIndex := map[string]string{
		"10.0.0.1": "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/loadBalancers/lb1",
	}

	resolved := ipIndex["192.168.1.1"]
	assert.Empty(t, resolved)
}

func TestParseLBFrontendIPs(t *testing.T) {
	content := `{
		"properties": {
			"frontendIPConfigurations": [
				{"properties": {"privateIPAddress": "10.0.0.1"}},
				{"properties": {"privateIPAddress": "10.0.0.2"}},
				{"properties": {}}
			]
		}
	}`
	ips := parseLBFrontendIPs(content)
	require.Len(t, ips, 2)
	assert.Equal(t, "10.0.0.1", ips[0])
	assert.Equal(t, "10.0.0.2", ips[1])
}

func TestParseLBFrontendIPs_Empty(t *testing.T) {
	assert.Nil(t, parseLBFrontendIPs(""))
	assert.Nil(t, parseLBFrontendIPs("{invalid"))
	assert.Empty(t, parseLBFrontendIPs(`{"properties":{"frontendIPConfigurations":[]}}`))
}

func TestIsAzureResourceID(t *testing.T) {
	assert.True(t, isAzureResourceID("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"))
	assert.False(t, isAzureResourceID("10.0.0.1"))
	assert.False(t, isAzureResourceID("example.com"))
}
