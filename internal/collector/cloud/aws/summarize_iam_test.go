// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeIAMGroup(t *testing.T) {
	got := summarizeIAMGroup(cloud.ResourceSpec{Name: "Admins", Metadata: map[string]string{"path": "/admins/"}})
	assert.Contains(t, got, "IAM group Admins")
	assert.Contains(t, got, "path=/admins/")
}

func TestSummarizeIAMGroup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "IAM group x", summarizeIAMGroup(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeIAMPolicy(t *testing.T) {
	got := summarizeIAMPolicy(cloud.ResourceSpec{Name: "ReadOnlyAccess", Metadata: map[string]string{"attachment_count": "5", "path": "/"}})
	assert.Contains(t, got, "IAM policy ReadOnlyAccess")
	assert.Contains(t, got, "attachments=5")
}

func TestSummarizeIAMRole(t *testing.T) {
	got := summarizeIAMRole(cloud.ResourceSpec{Name: "MyRole"})
	assert.Equal(t, "IAM role MyRole", got)
}

func TestSummarizeIAMUser(t *testing.T) {
	got := summarizeIAMUser(cloud.ResourceSpec{Name: "alice"})
	assert.Equal(t, "IAM user alice", got)
}
