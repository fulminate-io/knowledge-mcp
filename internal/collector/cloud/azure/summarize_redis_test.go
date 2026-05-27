// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureCache(t *testing.T) {
	assert.Equal(t, "Azure Cache for Redis c", summarizeAzureCache(cloud.ResourceSpec{Name: "c"}))
}
