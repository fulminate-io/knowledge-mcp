// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeGKECluster(t *testing.T) {
	assert.Equal(t, "GKE cluster c", summarizeGKECluster(cloud.ResourceSpec{Name: "c"}))
}
