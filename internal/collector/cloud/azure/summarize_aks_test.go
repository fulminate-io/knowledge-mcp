// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAKSCluster(t *testing.T) {
	assert.Equal(t, "AKS cluster c in eastus", summarizeAKSCluster(cloud.ResourceSpec{Name: "c", Region: "eastus"}))
}
