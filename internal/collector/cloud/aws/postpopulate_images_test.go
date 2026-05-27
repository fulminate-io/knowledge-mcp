// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractECSContainerImages_WithDeployments(t *testing.T) {
	// ECS DescribeServices response with containerImages in deployments
	// (available in newer ECS API versions).
	content := `{
		"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5",
		"deployments": [{
			"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5",
			"containerImages": [
				{"containerName": "app", "image": "111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1"},
				{"containerName": "sidecar", "image": "111.dkr.ecr.us-east-1.amazonaws.com/sidecar:latest"}
			]
		}]
	}`
	images := extractECSContainerImages(content)
	require.Len(t, images, 2)
	assert.Equal(t, "111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1", images[0])
	assert.Equal(t, "111.dkr.ecr.us-east-1.amazonaws.com/sidecar:latest", images[1])
}

func TestExtractECSContainerImages_NoDeploymentImages(t *testing.T) {
	// Older ECS API or services without containerImages in deployments.
	content := `{
		"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5",
		"deployments": [{
			"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5"
		}]
	}`
	images := extractECSContainerImages(content)
	assert.Empty(t, images)
}

func TestExtractECSContainerImages_EmptyContent(t *testing.T) {
	images := extractECSContainerImages("")
	assert.Nil(t, images)
}

func TestExtractECSContainerImages_InvalidJSON(t *testing.T) {
	images := extractECSContainerImages("{invalid")
	assert.Nil(t, images)
}

func TestMatchECRImage(t *testing.T) {
	index := map[string]string{
		"111:us-east-1:my-app": "arn:aws:ecr:us-east-1:111:repository/my-app",
	}
	ref := cloud.ParseImageRef("111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1")
	got := matchECRImage(ref, index)
	assert.Equal(t, "arn:aws:ecr:us-east-1:111:repository/my-app", got)
}

func TestMatchECRImage_NoMatch(t *testing.T) {
	index := map[string]string{
		"111:us-east-1:my-app": "arn:aws:ecr:us-east-1:111:repository/my-app",
	}
	ref := cloud.ParseImageRef("222.dkr.ecr.us-west-2.amazonaws.com/other:v1")
	got := matchECRImage(ref, index)
	assert.Empty(t, got)
}

func TestMatchECRImage_NonECR(t *testing.T) {
	index := map[string]string{}
	ref := cloud.ParseImageRef("nginx:latest")
	got := matchECRImage(ref, index)
	assert.Empty(t, got)
}

func TestParseECRArn_AWS(t *testing.T) {
	account, region, repo := parseECRArn("arn:aws:ecr:us-east-1:123456789:repository/my-app")
	assert.Equal(t, "123456789", account)
	assert.Equal(t, "us-east-1", region)
	assert.Equal(t, "my-app", repo)
}

func TestParseECRArn_Nested(t *testing.T) {
	account, region, repo := parseECRArn("arn:aws:ecr:eu-west-1:999:repository/team/service")
	assert.Equal(t, "999", account)
	assert.Equal(t, "eu-west-1", region)
	assert.Equal(t, "team/service", repo)
}

func TestParseECRArn_InvalidAWS(t *testing.T) {
	account, _, _ := parseECRArn("not-an-arn")
	assert.Empty(t, account)
}

func TestParseECRHostname(t *testing.T) {
	account, region := parseECRHostname("123456789.dkr.ecr.us-east-1.amazonaws.com")
	assert.Equal(t, "123456789", account)
	assert.Equal(t, "us-east-1", region)
}

func TestParseECRHostname_Invalid(t *testing.T) {
	account, _ := parseECRHostname("not-ecr-host")
	assert.Empty(t, account)
}

func TestEcrIndexKey(t *testing.T) {
	ref := cloud.ParseImageRef("111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1")
	key := ecrIndexKey(ref)
	assert.Equal(t, "111:us-east-1:my-app", key)
}

func TestResolveECSImageLineage_EndToEnd(t *testing.T) {
	// Build a service content with containerImages and an ECR index,
	// verify the edges are created correctly.
	svcContent := `{
		"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5",
		"deployments": [{
			"taskDefinition": "arn:aws:ecs:us-east-1:111:task-definition/my-task:5",
			"containerImages": [
				{"containerName": "app", "image": "111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1"},
				{"containerName": "nginx", "image": "nginx:latest"}
			]
		}]
	}`
	images := extractECSContainerImages(svcContent)
	require.Len(t, images, 2)

	index := map[string]string{
		"111:us-east-1:my-app": "arn:aws:ecr:us-east-1:111:repository/my-app",
	}

	// Simulate edge creation logic from resolveECSImageLineage.
	var edges []knowledgev1.Edge
	seen := make(map[string]struct{})
	for _, img := range images {
		ref := cloud.ParseImageRef(img)
		if ref.RegistryKind() != cloud.RegistryECR {
			continue
		}
		targetID := matchECRImage(ref, index)
		if targetID == "" {
			continue
		}
		key := "svc-arn|" + targetID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, knowledgev1.Edge{
			FromId:   "svc-arn",
			ToId:     targetID,
			Type:     string(kgtypes.EdgeUsesImage),
			Method:   "postpopulate:image-lineage",
			Evidence: ref.Full,
		})
	}

	require.Len(t, edges, 1)
	assert.Equal(t, "svc-arn", edges[0].FromId)
	assert.Equal(t, "arn:aws:ecr:us-east-1:111:repository/my-app", edges[0].ToId)
	assert.Equal(t, string(kgtypes.EdgeUsesImage), edges[0].Type)
	assert.Equal(t, "111.dkr.ecr.us-east-1.amazonaws.com/my-app:v1", edges[0].Evidence)
}
