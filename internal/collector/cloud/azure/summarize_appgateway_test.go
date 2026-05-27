// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAppGateway(t *testing.T) {
	assert.Equal(t, "Application Gateway g", summarizeAppGateway(cloud.ResourceSpec{Name: "g"}))
}
