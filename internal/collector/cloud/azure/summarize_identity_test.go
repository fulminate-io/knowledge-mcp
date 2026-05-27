// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeUserAssignedIdentity(t *testing.T) {
	assert.Equal(t, "user-assigned managed identity id", summarizeUserAssignedIdentity(cloud.ResourceSpec{Name: "id"}))
}
