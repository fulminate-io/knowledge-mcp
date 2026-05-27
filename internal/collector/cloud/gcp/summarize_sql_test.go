// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudSQLInstance(t *testing.T) {
	assert.Equal(t, "Cloud SQL instance i", summarizeCloudSQLInstance(cloud.ResourceSpec{Name: "i"}))
}
