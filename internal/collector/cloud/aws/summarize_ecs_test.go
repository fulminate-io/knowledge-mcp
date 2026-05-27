// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeECSCluster(t *testing.T) {
	got := summarizeECSCluster(cloud.ResourceSpec{
		Name: "prod", Region: "us-east-1",
		Metadata: map[string]string{"status": "ACTIVE"},
	})
	assert.Contains(t, got, "ECS cluster prod")
	assert.Contains(t, got, "status=ACTIVE")
}

func TestSummarizeECSCluster_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ECS cluster x", summarizeECSCluster(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeECSService(t *testing.T) {
	got := summarizeECSService(cloud.ResourceSpec{
		Name: "web", Region: "us-east-1",
		Metadata: map[string]string{"launch_type": "FARGATE", "desired_count": "3", "status": "ACTIVE"},
	})
	assert.Contains(t, got, "ECS service web")
	assert.Contains(t, got, "launch=FARGATE")
	assert.Contains(t, got, "desired=3")
	assert.Contains(t, got, "status=ACTIVE")
}

func TestSummarizeECSService_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ECS service x", summarizeECSService(cloud.ResourceSpec{Name: "x"}))
}
