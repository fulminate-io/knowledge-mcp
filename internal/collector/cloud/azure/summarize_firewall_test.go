// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureFirewall(t *testing.T) {
	assert.Equal(t, "Azure Firewall f", summarizeAzureFirewall(cloud.ResourceSpec{Name: "f"}))
}
