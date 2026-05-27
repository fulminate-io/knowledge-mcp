// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSynapseWorkspace(t *testing.T) {
	assert.Equal(t, "Synapse workspace w", summarizeSynapseWorkspace(cloud.ResourceSpec{Name: "w"}))
}
