// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestExtractAzureContainerImage(t *testing.T) {
	t.Run("extracts from LinuxFxVersion DOCKER format", func(t *testing.T) {
		content := `{"properties":{"siteConfig":{"linuxFxVersion":"DOCKER|myregistry.azurecr.io/myapp:v1"}}}`
		got := extractAzureContainerImage(content)
		assert.Equal(t, "myregistry.azurecr.io/myapp:v1", got)
	})

	t.Run("returns empty for non-Docker LinuxFxVersion", func(t *testing.T) {
		content := `{"properties":{"siteConfig":{"linuxFxVersion":"PYTHON|3.9"}}}`
		got := extractAzureContainerImage(content)
		assert.Empty(t, got)
	})

	t.Run("returns empty for missing siteConfig", func(t *testing.T) {
		content := `{"properties":{}}`
		got := extractAzureContainerImage(content)
		assert.Empty(t, got)
	})

	t.Run("returns empty for empty content", func(t *testing.T) {
		got := extractAzureContainerImage("")
		assert.Empty(t, got)
	})
}

func TestMatchACRImage(t *testing.T) {
	acrIndex := map[string]string{
		"myregistry.azurecr.io": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/myregistry",
	}

	t.Run("matches ACR image", func(t *testing.T) {
		ref := cloud.ParseImageRef("myregistry.azurecr.io/myapp:v1")
		got := matchACRImage(ref, acrIndex)
		assert.Equal(t, acrIndex["myregistry.azurecr.io"], got)
	})

	t.Run("no match for non-ACR image", func(t *testing.T) {
		ref := cloud.ParseImageRef("docker.io/library/nginx:latest")
		got := matchACRImage(ref, acrIndex)
		assert.Empty(t, got)
	})

	t.Run("no match for unknown ACR", func(t *testing.T) {
		ref := cloud.ParseImageRef("other.azurecr.io/app:v1")
		got := matchACRImage(ref, acrIndex)
		assert.Empty(t, got)
	})
}

func TestExtractACRLoginServer(t *testing.T) {
	t.Run("extracts login server from Content", func(t *testing.T) {
		content := `{"properties":{"loginServer":"myregistry.azurecr.io"}}`
		got := extractACRLoginServer(content)
		assert.Equal(t, "myregistry.azurecr.io", got)
	})

	t.Run("returns empty for missing loginServer", func(t *testing.T) {
		content := `{"properties":{}}`
		got := extractACRLoginServer(content)
		assert.Empty(t, got)
	})
}
