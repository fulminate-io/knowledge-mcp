// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeKMSKey(t *testing.T) {
	got := summarizeKMSKey(cloud.ResourceSpec{
		Name: "alias/aws/s3", Region: "us-east-1",
		Metadata: map[string]string{"KeyManager": "AWS", "key_state": "Enabled", "key_usage": "ENCRYPT_DECRYPT"},
	})
	assert.Contains(t, got, "KMS key alias/aws/s3")
	assert.Contains(t, got, "manager=AWS")
	assert.Contains(t, got, "state=Enabled")
	assert.Contains(t, got, "usage=ENCRYPT_DECRYPT")
}

func TestSummarizeKMSKey_EmptyMeta(t *testing.T) {
	assert.Equal(t, "KMS key x", summarizeKMSKey(cloud.ResourceSpec{Name: "x"}))
}
