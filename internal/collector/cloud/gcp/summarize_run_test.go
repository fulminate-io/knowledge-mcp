// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudRunService(t *testing.T) {
	assert.Equal(t, "Cloud Run service r", summarizeCloudRunService(cloud.ResourceSpec{Name: "r"}))
}
