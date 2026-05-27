// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeKeyVault(t *testing.T) {
	assert.Equal(t, "Key Vault kv", summarizeKeyVault(cloud.ResourceSpec{Name: "kv"}))
}
