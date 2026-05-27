// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeELBv2LoadBalancer(t *testing.T) {
	got := summarizeELBv2LoadBalancer(cloud.ResourceSpec{
		Name: "alb-1", Region: "us-east-1",
		Metadata: map[string]string{"type": "application", "scheme": "internet-facing", "vpc_id": "vpc-abc"},
	})
	assert.Contains(t, got, "ELBv2 load balancer alb-1")
	assert.Contains(t, got, "type=application")
	assert.Contains(t, got, "scheme=internet-facing")
	assert.Contains(t, got, "vpc=vpc-abc")
}

func TestSummarizeELBv2LoadBalancer_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ELBv2 load balancer x", summarizeELBv2LoadBalancer(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeELBv2TargetGroup(t *testing.T) {
	got := summarizeELBv2TargetGroup(cloud.ResourceSpec{
		Name: "tg-1", Region: "us-east-1",
		Metadata: map[string]string{"protocol": "HTTP", "port": "80", "target_type": "instance", "vpc_id": "vpc-abc"},
	})
	assert.Contains(t, got, "ELBv2 target group tg-1")
	assert.Contains(t, got, "proto=HTTP")
	assert.Contains(t, got, "port=80")
	assert.Contains(t, got, "target=instance")
}

func TestSummarizeELBv2TargetGroup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "ELBv2 target group x", summarizeELBv2TargetGroup(cloud.ResourceSpec{Name: "x"}))
}
