// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAzureVMSS(t *testing.T) {
	assert.Equal(t, "VM Scale Set ss", summarizeAzureVMSS(cloud.ResourceSpec{Name: "ss"}))
}
