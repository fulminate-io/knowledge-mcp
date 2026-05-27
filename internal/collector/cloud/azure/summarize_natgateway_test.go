// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureNATGateway(t *testing.T) {
	assert.Equal(t, "NAT gateway ng", summarizeAzureNATGateway(cloud.ResourceSpec{Name: "ng"}))
}
