// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFlowLogEdges(t *testing.T) {
	flID := "/subscriptions/sub/resourceGroups/NetworkWatcherRG/providers/Microsoft.Network/networkWatchers/NW/flowLogs/fl1"
	nsgID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/myNSG"
	storageID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/flowlogstore"
	workspaceID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.OperationalInsights/workspaces/la"

	t.Run("emits MONITORS for NSG target", func(t *testing.T) {
		nid := nsgID
		fl := &armnetwork.FlowLog{
			ID: &flID,
			Properties: &armnetwork.FlowLogPropertiesFormat{
				TargetResourceID: &nid,
			},
		}
		edges := flowLogEdges(fl)
		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeMonitors && e.TargetID == nsgID {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("emits SINKS_TO for storage and workspace", func(t *testing.T) {
		sid := storageID
		wid := workspaceID
		fl := &armnetwork.FlowLog{
			ID: &flID,
			Properties: &armnetwork.FlowLogPropertiesFormat{
				StorageID: &sid,
				FlowAnalyticsConfiguration: &armnetwork.TrafficAnalyticsProperties{
					NetworkWatcherFlowAnalyticsConfiguration: &armnetwork.TrafficAnalyticsConfigurationProperties{
						WorkspaceResourceID: &wid,
					},
				},
			},
		}
		edges := flowLogEdges(fl)

		var sinks []string
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeSinksTo {
				sinks = append(sinks, e.TargetID)
			}
		}
		assert.Contains(t, sinks, storageID)
		assert.Contains(t, sinks, workspaceID)
	})

	t.Run("returns nil when Properties nil", func(t *testing.T) {
		fl := &armnetwork.FlowLog{ID: &flID}
		edges := flowLogEdges(fl)
		assert.Nil(t, edges)
	})
}

func TestResourceGroupFromID(t *testing.T) {
	cases := map[string]string{
		"/subscriptions/sub/resourceGroups/myRG/providers/Microsoft.Network/networkWatchers/nw": "myRG",
		"/subscriptions/sub/resourcegroups/myRG/providers/X/y/z":                                "myRG",
		"/bogus": "",
		"":       "",
	}
	for id, want := range cases {
		assert.Equal(t, want, resourceGroupFromID(id), "id=%s", id)
	}
}
