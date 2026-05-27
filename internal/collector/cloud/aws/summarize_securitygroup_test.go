// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSecurityGroup(t *testing.T) {
	got := summarizeSecurityGroup(cloud.ResourceSpec{
		Name: "web-sg", Region: "us-east-1",
		Metadata: map[string]string{"vpc_id": "vpc-abc", "description": "web servers"},
	})
	assert.Contains(t, got, "security group web-sg")
	assert.Contains(t, got, "vpc=vpc-abc")
	assert.Contains(t, got, "(web servers)")
}

func TestSummarizeSecurityGroup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "security group x", summarizeSecurityGroup(cloud.ResourceSpec{Name: "x"}))
}
