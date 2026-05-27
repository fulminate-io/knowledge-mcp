// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudNAT(t *testing.T) {
	assert.Equal(t, "Cloud NAT n", summarizeCloudNAT(cloud.ResourceSpec{Name: "n"}))
}
