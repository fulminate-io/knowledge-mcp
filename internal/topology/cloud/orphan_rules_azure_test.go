// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const azureAcct = "00000000-0000-0000-0000-000000000000"

// TestAzureRulesRegistered asserts the v1 Azure rule is present.
func TestAzureRulesRegistered(t *testing.T) {
	_, ok := lookupOrphanRule("Microsoft.Network/loadBalancers")
	assert.True(t, ok, "expected orphan rule registered for Microsoft.Network/loadBalancers")
}

// --- Microsoft.Network/loadBalancers ---

func TestAzureLoadBalancerRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	lbID := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb-1"
	fx.AddCloudResource(azureAcct, lbID, "lb-1", "Microsoft.Network/loadBalancers", nil)

	orphan, conf, _, err := azureLoadBalancerRule(context.Background(), fx, azureAcct, fx.orphanGraphFor(t, azureAcct), fx.nodeFor(t, azureAcct, lbID))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
}

func TestAzureLoadBalancerRule_HasBackends_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	lbID := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb-2"
	nicID := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic-2"

	fx.AddCloudResource(azureAcct, lbID, "lb-2", "Microsoft.Network/loadBalancers", nil)
	fx.AddCloudResource(azureAcct, nicID, "nic-2", "Microsoft.Network/networkInterfaces", nil)
	fx.AddEdge(azureAcct, lbID, nicID, kgtypes.EdgeTargets)

	orphan, _, _, err := azureLoadBalancerRule(context.Background(), fx, azureAcct, fx.orphanGraphFor(t, azureAcct), fx.nodeFor(t, azureAcct, lbID))
	require.NoError(t, err)
	assert.False(t, orphan)
}
