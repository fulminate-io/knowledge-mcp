// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAPIGWHTTPAPI(t *testing.T) {
	got := summarizeAPIGWHTTPAPI(cloud.ResourceSpec{
		Name: "h", Region: "r",
		Metadata: map[string]string{"protocol_type": "HTTP"},
	})
	assert.Contains(t, got, "API Gateway HTTP API h")
	assert.Contains(t, got, "protocol=HTTP")
	assert.Contains(t, got, "in r")
}

func TestSummarizeAPIGWHTTPAPI_EmptyMeta(t *testing.T) {
	assert.Equal(t, "API Gateway HTTP API x", summarizeAPIGWHTTPAPI(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeAPIGWWSAPI(t *testing.T) {
	got := summarizeAPIGWWSAPI(cloud.ResourceSpec{Name: "w", Region: "r"})
	assert.Equal(t, "API Gateway WebSocket API w in r", got)
}
