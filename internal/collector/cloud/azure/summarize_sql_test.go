// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSQLServer(t *testing.T) {
	assert.Equal(t, "Azure SQL server s", summarizeSQLServer(cloud.ResourceSpec{Name: "s"}))
}

func TestSummarizeSQLDatabase(t *testing.T) {
	assert.Equal(t, "Azure SQL database d", summarizeSQLDatabase(cloud.ResourceSpec{Name: "d"}))
}
