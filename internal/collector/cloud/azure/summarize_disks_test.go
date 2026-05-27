// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureDisk(t *testing.T) {
	assert.Equal(t, "Azure managed disk d", summarizeAzureDisk(cloud.ResourceSpec{Name: "d"}))
}
