// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractObjectIDFromGroupNode(t *testing.T) {
	t.Run("valid group node ID", func(t *testing.T) {
		assert.Equal(t, "abc-123", extractObjectIDFromGroupNode("azure:aad:group/abc-123"))
	})

	t.Run("wrong prefix", func(t *testing.T) {
		assert.Empty(t, extractObjectIDFromGroupNode("azure:aad:principal/abc-123"))
	})

	t.Run("empty", func(t *testing.T) {
		assert.Empty(t, extractObjectIDFromGroupNode(""))
	})

	t.Run("prefix only", func(t *testing.T) {
		// No object ID after prefix.
		assert.Empty(t, extractObjectIDFromGroupNode("azure:aad:group/"))
	})
}

func TestBuildAADGroupIndex_Extract(t *testing.T) {
	// Test the extraction logic in isolation — the actual buildAADGroupIndex
	// function queries the DB which is tested at integration level.
	nodeIDs := []string{
		"azure:aad:group/group-a-id",
		"azure:aad:group/group-b-id",
		"azure:aad:principal/not-a-group",
	}
	index := make(map[string]string)
	for _, id := range nodeIDs {
		objectID := extractObjectIDFromGroupNode(id)
		if objectID != "" {
			index[objectID] = id
		}
	}
	assert.Len(t, index, 2)
	assert.Equal(t, "azure:aad:group/group-a-id", index["group-a-id"])
	assert.Equal(t, "azure:aad:group/group-b-id", index["group-b-id"])
	_, ok := index["not-a-group"]
	assert.False(t, ok)
}
