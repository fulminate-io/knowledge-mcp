// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAPIGWRestAPI(t *testing.T) {
	got := summarizeAPIGWRestAPI(cloud.ResourceSpec{
		Name: "my-api", Region: "us-east-1",
		Metadata: map[string]string{"endpoint_type": "REGIONAL"},
	})
	assert.Contains(t, got, "API Gateway REST API my-api")
	assert.Contains(t, got, "endpoint=REGIONAL")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeAPIGWRestAPI_EmptyMeta(t *testing.T) {
	assert.Equal(t, "API Gateway REST API x", summarizeAPIGWRestAPI(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeAPIGWDomain(t *testing.T) {
	got := summarizeAPIGWDomain(cloud.ResourceSpec{
		Name: "api.example.com", Region: "us-east-1",
		Metadata: map[string]string{"endpoint_type": "EDGE", "status": "AVAILABLE"},
	})
	assert.Contains(t, got, "API Gateway domain api.example.com")
	assert.Contains(t, got, "endpoint=EDGE")
	assert.Contains(t, got, "status=AVAILABLE")
}

func TestSummarizeAPIGWDomain_EmptyMeta(t *testing.T) {
	assert.Equal(t, "API Gateway domain x", summarizeAPIGWDomain(cloud.ResourceSpec{Name: "x"}))
}
