// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeFunctionApp(t *testing.T) {
	assert.Equal(t, "Azure Function App fa", summarizeFunctionApp(cloud.ResourceSpec{Name: "fa"}))
}
