// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeGatewayClass(t *testing.T) {
	assert.Equal(t, "GatewayClass gc", summarizeGatewayClass(cloud.ResourceSpec{Name: "gc"}))
}

func TestSummarizeGateway(t *testing.T) {
	assert.Equal(t, "Gateway g", summarizeGateway(cloud.ResourceSpec{Name: "g"}))
}
