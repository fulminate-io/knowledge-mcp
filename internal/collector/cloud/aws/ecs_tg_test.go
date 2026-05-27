// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBuildService_TargetGroupEdges(t *testing.T) {
	c := &ecsCollector{region: "us-east-1", accountID: "111111111111"}

	svc := ecstypes.Service{
		ServiceArn:  awssdk.String("arn:aws:ecs:us-east-1:111111111111:service/my-cluster/my-svc"),
		ServiceName: awssdk.String("my-svc"),
		LoadBalancers: []ecstypes.LoadBalancer{
			{
				TargetGroupArn: awssdk.String("arn:aws:elasticloadbalancing:us-east-1:111111111111:targetgroup/my-tg/abc"),
				ContainerName:  awssdk.String("web"),
				ContainerPort:  awssdk.Int32(8080),
			},
			{
				TargetGroupArn: awssdk.String("arn:aws:elasticloadbalancing:us-east-1:111111111111:targetgroup/my-tg2/def"),
				ContainerName:  awssdk.String("api"),
				ContainerPort:  awssdk.Int32(3000),
			},
		},
	}

	_, edges, err := c.buildService(svc)
	require.NoError(t, err)

	// Filter to only TARGETS edges (buildService also emits awsvpc and role edges).
	var tgEdges []cloud.EdgeSpec
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeTargets {
			tgEdges = append(tgEdges, e)
		}
	}

	require.Len(t, tgEdges, 2)

	assert.Equal(t, "arn:aws:ecs:us-east-1:111111111111:service/my-cluster/my-svc", tgEdges[0].SourceID)
	assert.Equal(t, "arn:aws:elasticloadbalancing:us-east-1:111111111111:targetgroup/my-tg/abc", tgEdges[0].TargetID)
	assert.Equal(t, "web", tgEdges[0].Metadata["container_name"])
	assert.Equal(t, "8080", tgEdges[0].Metadata["container_port"])

	assert.Equal(t, "arn:aws:elasticloadbalancing:us-east-1:111111111111:targetgroup/my-tg2/def", tgEdges[1].TargetID)
	assert.Equal(t, "api", tgEdges[1].Metadata["container_name"])
	assert.Equal(t, "3000", tgEdges[1].Metadata["container_port"])
}

func TestBuildService_NoLoadBalancers(t *testing.T) {
	c := &ecsCollector{region: "us-east-1", accountID: "111111111111"}

	svc := ecstypes.Service{
		ServiceArn:  awssdk.String("arn:aws:ecs:us-east-1:111111111111:service/my-cluster/my-svc"),
		ServiceName: awssdk.String("my-svc"),
	}

	_, edges, err := c.buildService(svc)
	require.NoError(t, err)

	// No TARGETS edges when no LoadBalancers configured.
	for _, e := range edges {
		assert.NotEqual(t, kgtypes.EdgeTargets, e.Relationship)
	}
}

func TestBuildService_NilTargetGroupArn(t *testing.T) {
	c := &ecsCollector{region: "us-east-1", accountID: "111111111111"}

	svc := ecstypes.Service{
		ServiceArn:  awssdk.String("arn:aws:ecs:us-east-1:111111111111:service/my-cluster/my-svc"),
		ServiceName: awssdk.String("my-svc"),
		LoadBalancers: []ecstypes.LoadBalancer{
			{ContainerName: awssdk.String("web"), ContainerPort: awssdk.Int32(80)},
		},
	}

	_, edges, err := c.buildService(svc)
	require.NoError(t, err)

	for _, e := range edges {
		assert.NotEqual(t, kgtypes.EdgeTargets, e.Relationship)
	}
}
