// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAppServiceSite(t *testing.T) {
	assert.Equal(t, "App Service site s", summarizeAppServiceSite(cloud.ResourceSpec{Name: "s"}))
}
