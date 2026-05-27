// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestParseTriggerBindings(t *testing.T) {
	const (
		appID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/sites/my-func"
		subID = "sub"
	)

	t.Run("extracts serviceBusTrigger and emits queue sentinel proxy", func(t *testing.T) {
		config := map[string]any{
			"bindings": []any{
				map[string]any{
					"type":       "serviceBusTrigger",
					"direction":  "in",
					"queueName":  "my-queue",
					"connection": "SB_CONN",
				},
			},
		}
		seen := map[string]bool{}
		edges, proxies := parseTriggerBindings(appID, subID, config, seen)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
		assert.Equal(t, appID, edges[0].SourceID)
		assert.Equal(t, "azure:servicebus:queue:my-queue", edges[0].TargetID)
		require.Len(t, proxies, 1)
		assert.Equal(t, "azure:servicebus:queue:my-queue", proxies[0].ID)
		assert.Equal(t, "azure:servicebus:queue", proxies[0].ResourceType)
		assert.Equal(t, "my-queue", proxies[0].Name)
		assert.Equal(t, "false", proxies[0].Metadata["collected"])
		assert.Equal(t, subID, proxies[0].Metadata["subscription_id"])
		assert.True(t, seen["azure:servicebus:queue:my-queue"])
	})

	t.Run("extracts eventHubTrigger", func(t *testing.T) {
		config := map[string]any{
			"bindings": []any{
				map[string]any{
					"type":         "eventHubTrigger",
					"direction":    "in",
					"eventHubName": "my-hub",
				},
			},
		}
		edges, proxies := parseTriggerBindings(appID, subID, config, map[string]bool{})
		require.Len(t, edges, 1)
		assert.Equal(t, "azure:eventhub:hub:my-hub", edges[0].TargetID)
		require.Len(t, proxies, 1)
		assert.Equal(t, "azure:eventhub:hub", proxies[0].ResourceType)
	})

	t.Run("seenProxies dedupes proxy across multiple functions", func(t *testing.T) {
		config := map[string]any{
			"bindings": []any{
				map[string]any{
					"type":      "serviceBusTrigger",
					"direction": "in",
					"queueName": "shared-queue",
				},
			},
		}
		seen := map[string]bool{}

		_, proxies1 := parseTriggerBindings(appID, subID, config, seen)
		require.Len(t, proxies1, 1)

		_, proxies2 := parseTriggerBindings(appID, subID, config, seen)
		assert.Empty(t, proxies2, "shared seenProxies suppresses duplicate proxy")
	})

	t.Run("skips non-trigger bindings", func(t *testing.T) {
		config := map[string]any{
			"bindings": []any{
				map[string]any{
					"type":      "http",
					"direction": "out",
				},
			},
		}
		edges, proxies := parseTriggerBindings(appID, subID, config, map[string]bool{})
		assert.Empty(t, edges)
		assert.Empty(t, proxies)
	})

	t.Run("handles nil config", func(t *testing.T) {
		edges, proxies := parseTriggerBindings(appID, subID, nil, map[string]bool{})
		assert.Empty(t, edges)
		assert.Empty(t, proxies)
	})
}
