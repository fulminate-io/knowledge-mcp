// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeNetworkACL(t *testing.T) {
	got := summarizeNetworkACL(cloud.ResourceSpec{
		Name: "acl-1", Region: "us-east-1",
		Metadata: map[string]string{"vpc_id": "vpc-abc", "is_default": "true"},
	})
	assert.Contains(t, got, "network ACL acl-1")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "default")
}

func TestSummarizeNetworkACL_EmptyMeta(t *testing.T) {
	assert.Equal(t, "network ACL x", summarizeNetworkACL(cloud.ResourceSpec{Name: "x"}))
}
