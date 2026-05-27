// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAPIMService(t *testing.T) {
	assert.Equal(t, "API Management service s", summarizeAPIMService(cloud.ResourceSpec{Name: "s"}))
}

func TestSummarizeAPIMAPI(t *testing.T) {
	assert.Equal(t, "API Management API a", summarizeAPIMAPI(cloud.ResourceSpec{Name: "a"}))
}
