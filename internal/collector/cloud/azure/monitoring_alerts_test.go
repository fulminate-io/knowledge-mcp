// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestMetricAlertEdges(t *testing.T) {
	alertID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/metricAlerts/my-alert"
	vmID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/my-vm"

	t.Run("emits MONITORS for each scope", func(t *testing.T) {
		alert := &armmonitor.MetricAlertResource{
			ID: &alertID,
			Properties: &armmonitor.MetricAlertProperties{
				Scopes: []*string{&vmID},
			},
		}
		edges := metricAlertEdges(alert)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeMonitors, edges[0].Relationship)
		assert.Equal(t, alertID, edges[0].SourceID)
		assert.Equal(t, vmID, edges[0].TargetID)
	})

	t.Run("no edge when no scopes", func(t *testing.T) {
		alert := &armmonitor.MetricAlertResource{
			ID:         &alertID,
			Properties: &armmonitor.MetricAlertProperties{},
		}
		edges := metricAlertEdges(alert)
		assert.Empty(t, edges)
	})

	t.Run("no edge when nil properties", func(t *testing.T) {
		alert := &armmonitor.MetricAlertResource{
			ID: &alertID,
		}
		edges := metricAlertEdges(alert)
		assert.Empty(t, edges)
	})
}
