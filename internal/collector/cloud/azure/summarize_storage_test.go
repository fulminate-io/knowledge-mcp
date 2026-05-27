// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeStorageAccount(t *testing.T) {
	assert.Equal(t, "Storage account sa", summarizeStorageAccount(cloud.ResourceSpec{Name: "sa"}))
}
