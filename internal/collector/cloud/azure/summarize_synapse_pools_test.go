// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSynapseSQLPool(t *testing.T) {
	assert.Equal(t, "Synapse SQL pool p", summarizeSynapseSQLPool(cloud.ResourceSpec{Name: "p"}))
}

func TestSummarizeSynapseSparkPool(t *testing.T) {
	assert.Equal(t, "Synapse Spark pool p", summarizeSynapseSparkPool(cloud.ResourceSpec{Name: "p"}))
}
