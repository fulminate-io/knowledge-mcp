// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVNet(t *testing.T) {
	assert.Equal(t, "VNet vn", summarizeVNet(cloud.ResourceSpec{Name: "vn"}))
}

func TestSummarizeVNetSubnet(t *testing.T) {
	assert.Equal(t, "VNet subnet s", summarizeVNetSubnet(cloud.ResourceSpec{Name: "s"}))
}
