// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCosmosDB(t *testing.T) {
	assert.Equal(t, "Cosmos DB account a", summarizeCosmosDB(cloud.ResourceSpec{Name: "a"}))
}
