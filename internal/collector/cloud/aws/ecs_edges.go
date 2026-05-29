// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// processTaskDefinition fetches a task definition and extracts IAM role edges
// and container image cascade targets.
func (c *ecsCollector) processTaskDefinition(ctx context.Context, svcARN, taskDefARN string, seenTargets map[string]struct{}) ([]cloud.EdgeSpec, []cloud.CollectTarget, error) {
	desc, err := c.client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: awssdk.String(taskDefARN),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ecs: describe task definition %s: %w", taskDefARN, err)
	}

	var edges []cloud.EdgeSpec
	td := desc.TaskDefinition

	// Task role → ASSUMES_ROLE
	if td.TaskRoleArn != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     svcARN,
			TargetID:     awssdk.ToString(td.TaskRoleArn),
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "task_role"},
		})
	}

	// Execution role → ASSUMES_ROLE
	if td.ExecutionRoleArn != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     svcARN,
			TargetID:     awssdk.ToString(td.ExecutionRoleArn),
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "execution_role"},
		})
	}

	// Container image → USES_IMAGE edges + cascade targets for ECR registries.
	edges = append(edges, ecrImageEdges(svcARN, td.ContainerDefinitions)...)
	targets := extractECSImageTargets(td.ContainerDefinitions, seenTargets)

	return edges, targets, nil
}

// extractECSImageTargets parses ECS container images and returns cascade targets
// for ECR registries. Same pattern as k8s extractImageTargets().
func extractECSImageTargets(containers []ecstypes.ContainerDefinition, seen map[string]struct{}) []cloud.CollectTarget {
	var targets []cloud.CollectTarget

	for _, ctr := range containers {
		img := awssdk.ToString(ctr.Image)
		if img == "" {
			continue
		}

		// Split image into registry/repo:tag
		parts := strings.SplitN(img, "/", 3)
		if len(parts) < 2 {
			continue // no registry prefix
		}
		registry := parts[0]

		// ECR: <account>.dkr.ecr.<region>.amazonaws.com/repo
		if strings.Contains(registry, ".dkr.ecr.") && strings.HasSuffix(registry, ".amazonaws.com") {
			accountID, _, _ := strings.Cut(registry, ".")
			key := "aws:" + accountID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, cloud.CollectTarget{Collector: "aws", ID: accountID})
		}
	}

	return targets
}

// ecrImageEdges returns USES_IMAGE edges from sourceARN to each ECR repository
// referenced in the container definitions. Non-ECR images are skipped.
func ecrImageEdges(sourceARN string, containers []ecstypes.ContainerDefinition) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	seen := make(map[string]struct{})
	for _, ctr := range containers {
		img := awssdk.ToString(ctr.Image)
		if img == "" {
			continue
		}
		// ECR images: <account>.dkr.ecr.<region>.amazonaws.com/repo[:tag]
		parts := strings.SplitN(img, "/", 2)
		if len(parts) < 2 {
			continue
		}
		registry := parts[0]
		if !strings.Contains(registry, ".dkr.ecr.") || !strings.HasSuffix(registry, ".amazonaws.com") {
			continue
		}
		// Parse account and region from registry hostname.
		rParts := strings.SplitN(registry, ".", 6)
		if len(rParts) < 6 {
			continue
		}
		accountID, region := rParts[0], rParts[3]

		// Strip tag/digest from repo name.
		repo := parts[1]
		if idx := strings.IndexAny(repo, ":@"); idx >= 0 {
			repo = repo[:idx]
		}

		repoARN := fmt.Sprintf("arn:aws:ecr:%s:%s:repository/%s", region, accountID, repo)
		if _, ok := seen[repoARN]; ok {
			continue
		}
		seen[repoARN] = struct{}{}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sourceARN,
			TargetID:     repoARN,
			Relationship: kgtypes.EdgeUsesImage,
		})
	}
	return edges
}

// awsvpcEdges extracts subnet and security group edges from ECS awsvpc config.
func (c *ecsCollector) awsvpcEdges(sourceARN string, netCfg *ecstypes.NetworkConfiguration) []cloud.EdgeSpec {
	if netCfg == nil || netCfg.AwsvpcConfiguration == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, subnetID := range netCfg.AwsvpcConfiguration.Subnets {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: sourceARN, TargetID: ec2ARN(c.region, c.accountID, "subnet", subnetID), Relationship: kgtypes.EdgeUsesSubnet,
		})
	}
	for _, sgID := range netCfg.AwsvpcConfiguration.SecurityGroups {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: sourceARN, TargetID: ec2ARN(c.region, c.accountID, "security-group", sgID), Relationship: kgtypes.EdgeUsesSecurityGroup,
		})
	}
	return edges
}
