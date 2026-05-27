// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeStorageFileShare(t *testing.T) {
	assert.Equal(t, "Storage file share fs", summarizeStorageFileShare(cloud.ResourceSpec{Name: "fs"}))
}
