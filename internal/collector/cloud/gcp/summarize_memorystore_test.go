// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRedisInstance(t *testing.T) {
	assert.Equal(t, "Memorystore Redis instance r", summarizeRedisInstance(cloud.ResourceSpec{Name: "r"}))
}
