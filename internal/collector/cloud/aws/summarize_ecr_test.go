// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeECRRepository(t *testing.T) {
	got := summarizeECRRepository(cloud.ResourceSpec{
		Name: "myapp", Region: "us-east-1",
		Metadata: map[string]string{"image_tag_mutability": "IMMUTABLE", "encryption_type": "AES256"},
	})
	assert.Contains(t, got, "ECR repository myapp")
	assert.Contains(t, got, "mutability=IMMUTABLE")
	assert.Contains(t, got, "encryption=AES256")
}

func TestSummarizeECRRepository_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ECR repository x", summarizeECRRepository(cloud.ResourceSpec{Name: "x"}))
}
