// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeLambdaFunction(t *testing.T) {
	got := summarizeLambdaFunction(cloud.ResourceSpec{
		Name: "myfn", Region: "us-east-1",
		Metadata: map[string]string{"runtime": "python3.11", "memory_size": "512", "function_url_auth_type": "NONE"},
	})
	assert.Contains(t, got, "Lambda function myfn")
	assert.Contains(t, got, "runtime=python3.11")
	assert.Contains(t, got, "memory=512MB")
	assert.Contains(t, got, "url-auth=NONE")
}

func TestSummarizeLambdaFunction_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Lambda function x", summarizeLambdaFunction(cloud.ResourceSpec{Name: "x"}))
}
