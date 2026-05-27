// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureFlowLog(t *testing.T) {
	assert.Equal(t, "network flow log fl", summarizeAzureFlowLog(cloud.ResourceSpec{Name: "fl"}))
}
