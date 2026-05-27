// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEcrImageEdges(t *testing.T) {
	svcARN := "arn:aws:ecs:us-east-1:111111111111:service/my-cluster/my-svc"
	containers := []ecstypes.ContainerDefinition{
		{Image: awssdk.String("111111111111.dkr.ecr.us-east-1.amazonaws.com/my-app:latest")},
		{Image: awssdk.String("111111111111.dkr.ecr.us-east-1.amazonaws.com/sidecar@sha256:abc123")},
		{Image: awssdk.String("nginx:latest")},                                           // non-ECR, skipped
		{Image: awssdk.String("")},                                                       // empty, skipped
		{Image: awssdk.String("111111111111.dkr.ecr.us-east-1.amazonaws.com/my-app:v2")}, // dedup
	}

	edges := ecrImageEdges(svcARN, containers)
	require.Len(t, edges, 2) // my-app + sidecar, nginx skipped, my-app deduped

	assert.Equal(t, svcARN, edges[0].SourceID)
	assert.Equal(t, "arn:aws:ecr:us-east-1:111111111111:repository/my-app", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesImage, edges[0].Relationship)

	assert.Equal(t, "arn:aws:ecr:us-east-1:111111111111:repository/sidecar", edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesImage, edges[1].Relationship)
}

func TestEcrImageEdges_NoECR(t *testing.T) {
	containers := []ecstypes.ContainerDefinition{
		{Image: awssdk.String("nginx:latest")},
		{Image: awssdk.String("gcr.io/my-project/my-app:latest")},
	}
	edges := ecrImageEdges("arn:aws:ecs:us-east-1:111:service/c/s", containers)
	assert.Empty(t, edges)
}
