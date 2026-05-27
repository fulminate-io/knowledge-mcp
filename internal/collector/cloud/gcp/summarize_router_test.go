// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeComputeRouter(t *testing.T) {
	assert.Equal(t, "Cloud Router r", summarizeComputeRouter(cloud.ResourceSpec{Name: "r"}))
}
