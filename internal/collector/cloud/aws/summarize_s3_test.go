// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeS3Bucket(t *testing.T) {
	got := summarizeS3Bucket(cloud.ResourceSpec{
		Name: "my-bucket", Region: "us-east-1",
		Metadata: map[string]string{"creation_date": "2024-01-01T00:00:00Z"},
	})
	assert.Contains(t, got, "S3 bucket my-bucket")
	assert.Contains(t, got, "created=2024-01-01T00:00:00Z")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeS3Bucket_EmptyMeta(t *testing.T) {
	got := summarizeS3Bucket(cloud.ResourceSpec{Name: "x"})
	assert.Equal(t, "S3 bucket x", got)
}
