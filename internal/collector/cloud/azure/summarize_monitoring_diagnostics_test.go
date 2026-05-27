// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDiagnosticSetting(t *testing.T) {
	assert.Equal(t, "diagnostic setting ds", summarizeDiagnosticSetting(cloud.ResourceSpec{Name: "ds"}))
}
