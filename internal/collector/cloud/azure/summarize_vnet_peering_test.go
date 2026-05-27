// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVNetPeering(t *testing.T) {
	assert.Equal(t, "VNet peering p", summarizeVNetPeering(cloud.ResourceSpec{Name: "p"}))
}
