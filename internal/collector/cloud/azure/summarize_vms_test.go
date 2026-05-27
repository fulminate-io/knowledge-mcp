// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureVM(t *testing.T) {
	assert.Equal(t, "Azure VM vm", summarizeAzureVM(cloud.ResourceSpec{Name: "vm"}))
}
