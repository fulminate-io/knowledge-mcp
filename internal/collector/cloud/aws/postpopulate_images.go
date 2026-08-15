// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveECSImageLineage creates USES_IMAGE edges from ECS service nodes to
// ECR repository nodes in the graph. It extracts container images from the
// service's Content JSON (the ECS DescribeServices API response embeds the
// task definition when present) and matches them against ECR repository nodes.
//
// Note: ECS services also get USES_IMAGE edges at collection time via
// ecrImageEdges() which constructs ARNs from image hostnames. This resolver
// runs in PostPopulate to validate those edges against actual graph nodes and
// add Evidence metadata (the full image reference).
func resolveECSImageLineage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	ecrIndex, err := buildECRIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("ecs image lineage: build ECR index: %w", err)
	}
	if len(ecrIndex) == 0 {
		return nil
	}

	services, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "ecs-service"},
	})
	if err != nil {
		return fmt.Errorf("ecs image lineage: query services: %w", err)
	}

	var edges []knowledgev1.Edge
	seen := make(map[string]struct{})

	for _, node := range services {
		images := extractECSContainerImages(node.Content)
		for _, img := range images {
			ref := cloud.ParseImageRef(img)
			if ref.RegistryKind() != cloud.RegistryECR {
				continue
			}
			targetID := matchECRImage(ref, ecrIndex)
			if targetID == "" {
				continue
			}
			key := node.Id + "|" + targetID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			edges = append(edges, knowledgev1.Edge{
				FromId:   node.Id,
				ToId:     targetID,
				Type:     string(kgtypes.EdgeUsesImage),
				Method:   "postpopulate:image-lineage",
				Evidence: ref.Full,
			})
		}
	}

	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return fmt.Errorf("ecs image lineage: link batch: %w", err)
	}
	slog.Debug("aws postPopulate: created ECS USES_IMAGE edges", "count", len(edges))
	return nil
}

// extractECSContainerImages extracts container image strings from an ECS
// service's Content JSON. ECS services store the DescribeServices response
// which includes container definitions when the task definition is inlined,
// or lists images via the Deployments field.
func extractECSContainerImages(content string) []string {
	if content == "" {
		return nil
	}
	var svc ecsServiceContent
	if err := json.Unmarshal([]byte(content), &svc); err != nil {
		return nil
	}
	// Primary path: TaskSets or Deployments may reference different task defs,
	// but the service-level taskDefinition + container overrides are in the
	// service response. The DescribeServices response doesn't include container
	// definitions directly. Extract from ContainerImages if present (future
	// collector enrichment), or fall back to Deployments metadata.
	var images []string

	// The ECS API response for DescribeServices does NOT include container
	// definitions inline. Container images are available only via
	// DescribeTaskDefinition which is called at collection time. The
	// ecrImageEdges function handles edge creation during collection.
	// This PostPopulate hook exists for forward compatibility when the
	// collector stores container images on service nodes.
	for _, d := range svc.Deployments {
		for _, ci := range d.ContainerImages {
			if ci.Image != "" {
				images = append(images, ci.Image)
			}
		}
	}
	return images
}

// ecsServiceContent is a minimal struct for parsing relevant fields from
// the ECS DescribeServices API response stored in Content.
type ecsServiceContent struct {
	TaskDefinition string              `json:"taskDefinition"`
	Deployments    []ecsDeploymentInfo `json:"deployments"`
}

type ecsDeploymentInfo struct {
	TaskDefinition  string              `json:"taskDefinition"`
	ContainerImages []ecsContainerImage `json:"containerImages"`
}

type ecsContainerImage struct {
	Image         string `json:"image"`
	ImageDigest   string `json:"imageDigest"`
	ContainerName string `json:"containerName"`
}

// buildECRIndex queries ECR repository nodes and indexes by account:region:repo.
func buildECRIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]string, error) {
	repos, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "ecr-repository"},
	})
	if err != nil {
		return nil, err
	}
	index := make(map[string]string, len(repos))
	for _, n := range repos {
		account, region, repo := parseECRArn(n.Id)
		if account != "" {
			index[account+":"+region+":"+repo] = n.Id
		}
	}
	return index, nil
}

// matchECRImage returns the ECR repository node ID matching the given image ref.
func matchECRImage(ref cloud.ImageRef, index map[string]string) string {
	return index[ecrIndexKey(ref)]
}

// ecrIndexKey builds the ECR index lookup key from a parsed image ref.
func ecrIndexKey(ref cloud.ImageRef) string {
	account, region := parseECRHostname(ref.Registry)
	if account == "" {
		return ""
	}
	return account + ":" + region + ":" + ref.Repository
}

// parseECRArn extracts account, region, and repo from an ECR ARN.
// ARN format: arn:aws:ecr:<region>:<account>:repository/<repo>
func parseECRArn(arn string) (account, region, repo string) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[2] != "ecr" {
		return "", "", ""
	}
	region = parts[3]
	account = parts[4]
	repoPath := parts[5]
	const prefix = "repository/"
	if strings.HasPrefix(repoPath, prefix) {
		repo = repoPath[len(prefix):]
	}
	return account, region, repo
}

// parseECRHostname extracts account ID and region from an ECR registry hostname.
// Format: <account>.dkr.ecr.<region>.amazonaws.com
func parseECRHostname(hostname string) (account, region string) {
	parts := strings.SplitN(hostname, ".", 6)
	if len(parts) < 6 {
		return "", ""
	}
	return parts[0], parts[3]
}
