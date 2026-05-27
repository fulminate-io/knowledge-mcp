// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeNSG(t *testing.T) {
	assert.Equal(t, "NSG n", summarizeNSG(cloud.ResourceSpec{Name: "n"}))
}

func TestSummarizeNSGRule(t *testing.T) {
	assert.Equal(t, "NSG rule r", summarizeNSGRule(cloud.ResourceSpec{Name: "r"}))
}
