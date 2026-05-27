// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestAPIMCollector_Name(t *testing.T) {
	c := &apimCollector{}
	assert.Equal(t, "azure-apim", c.Name())
}

func TestAPIMBackendEdge(t *testing.T) {
	const (
		apimID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ApiManagement/service/my-apim"
		subID  = "sub"
	)

	t.Run("emits sentinel proxy keyed on host", func(t *testing.T) {
		seen := map[string]bool{}
		edge, proxy, ok := apimBackendEdge(apimID, "https://my-backend.example.com/v1/orders", subID, seen)
		require.True(t, ok)
		assert.Equal(t, apimID, edge.SourceID)
		assert.Equal(t, "azure:apim:backend:my-backend.example.com", edge.TargetID)
		assert.Equal(t, kgtypes.EdgeRoutesTo, edge.Relationship)
		assert.Equal(t, "https://my-backend.example.com/v1/orders", edge.Metadata["serviceUrl"])
		require.NotNil(t, proxy)
		assert.Equal(t, "azure:apim:backend", proxy.ResourceType)
		assert.Equal(t, "my-backend.example.com", proxy.Name)
		assert.Equal(t, "false", proxy.Metadata["collected"])
		assert.Equal(t, subID, proxy.Metadata["subscription_id"])
		assert.Equal(t, "my-backend.example.com", proxy.Metadata["host"])
	})

	t.Run("seenProxies dedupes proxy across multiple APIs", func(t *testing.T) {
		seen := map[string]bool{}
		_, proxy1, ok := apimBackendEdge(apimID, "https://shared.example.com/a", subID, seen)
		require.True(t, ok)
		require.NotNil(t, proxy1)

		_, proxy2, ok := apimBackendEdge(apimID, "https://shared.example.com/b", subID, seen)
		require.True(t, ok)
		assert.Nil(t, proxy2, "second call against same host returns nil proxy")
	})

	t.Run("normalizes host casing", func(t *testing.T) {
		edge, _, ok := apimBackendEdge(apimID, "https://My-Backend.Example.COM/", subID, map[string]bool{})
		require.True(t, ok)
		assert.Equal(t, "azure:apim:backend:my-backend.example.com", edge.TargetID)
	})

	t.Run("rejects URL without host", func(t *testing.T) {
		_, _, ok := apimBackendEdge(apimID, "not a url", subID, map[string]bool{})
		assert.False(t, ok)
	})
}
