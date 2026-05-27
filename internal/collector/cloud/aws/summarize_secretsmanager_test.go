// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSecretsManagerSecret(t *testing.T) {
	got := summarizeSecretsManagerSecret(cloud.ResourceSpec{
		Name: "prod/db", Region: "us-east-1",
		Metadata: map[string]string{"description": "DB password", "kms_key_id": "key-1"},
	})
	assert.Contains(t, got, "Secrets Manager secret prod/db")
	assert.Contains(t, got, "(DB password)")
	assert.Contains(t, got, "kms-encrypted")
}

func TestSummarizeSecretsManagerSecret_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Secrets Manager secret x", summarizeSecretsManagerSecret(cloud.ResourceSpec{Name: "x"}))
}
