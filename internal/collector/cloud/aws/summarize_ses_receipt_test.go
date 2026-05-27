// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSESReceiptRule(t *testing.T) {
	got := summarizeSESReceiptRule(cloud.ResourceSpec{
		Name: "rule-1", Region: "us-east-1",
		Metadata: map[string]string{"enabled": "true", "scan_enabled": "true"},
	})
	assert.Contains(t, got, "SES receipt rule rule-1")
	assert.Contains(t, got, "enabled")
	assert.Contains(t, got, "spam-scan")
}

func TestSummarizeSESReceiptRule_EmptyMeta(t *testing.T) {
	assert.Equal(t, "SES receipt rule x", summarizeSESReceiptRule(cloud.ResourceSpec{Name: "x"}))
}
