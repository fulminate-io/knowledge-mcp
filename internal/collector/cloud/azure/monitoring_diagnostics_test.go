// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestDiagnosticSettingEdges(t *testing.T) {
	dsID := "/subscriptions/sub/providers/Microsoft.Insights/diagnosticSettings/ds1"
	storageID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/sink"
	workspaceID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.OperationalInsights/workspaces/la"
	ehRuleID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/ns/authorizationRules/rule"
	marketplaceID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Datadog/monitors/dd"

	t.Run("emits SINKS_TO for every configured destination", func(t *testing.T) {
		sid := storageID
		wid := workspaceID
		eid := ehRuleID
		mid := marketplaceID
		ds := &armmonitor.DiagnosticSettingsResource{
			ID: &dsID,
			Properties: &armmonitor.DiagnosticSettings{
				StorageAccountID:            &sid,
				WorkspaceID:                 &wid,
				EventHubAuthorizationRuleID: &eid,
				MarketplacePartnerID:        &mid,
			},
		}
		edges := diagnosticSettingEdges(ds)

		var sinks []string
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeSinksTo {
				assert.Equal(t, dsID, e.SourceID)
				sinks = append(sinks, e.TargetID)
			}
		}
		assert.ElementsMatch(t, []string{storageID, workspaceID, ehRuleID, marketplaceID}, sinks)
	})

	t.Run("skips nil and empty destinations", func(t *testing.T) {
		empty := ""
		sid := storageID
		ds := &armmonitor.DiagnosticSettingsResource{
			ID: &dsID,
			Properties: &armmonitor.DiagnosticSettings{
				StorageAccountID:            &sid,
				WorkspaceID:                 &empty,
				EventHubAuthorizationRuleID: nil,
			},
		}
		edges := diagnosticSettingEdges(ds)
		assert.Len(t, edges, 1)
		assert.Equal(t, storageID, edges[0].TargetID)
	})

	t.Run("returns nil when Properties nil", func(t *testing.T) {
		ds := &armmonitor.DiagnosticSettingsResource{ID: &dsID}
		edges := diagnosticSettingEdges(ds)
		assert.Nil(t, edges)
	})
}
