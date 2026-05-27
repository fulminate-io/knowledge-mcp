// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeMetricAlert(t *testing.T) {
	assert.Equal(t, "metric alert ma", summarizeMetricAlert(cloud.ResourceSpec{Name: "ma"}))
}
