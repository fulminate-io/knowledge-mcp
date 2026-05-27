// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveImageLineage creates USES_IMAGE edges from workload nodes to
// container registry nodes (ECR, ACR, Artifact Registry). It runs as part
// of the K8s postPopulate pipeline after all nodes have been written.
func resolveImageLineage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	index, err := buildRegistryIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("image lineage: build registry index: %w", err)
	}
	if index.empty() {
		return nil
	}

	edges, err := resolveWorkloadImages(ctx, gc, graphName, index)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return nil
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return fmt.Errorf("image lineage: link batch: %w", err)
	}
	slog.Debug("postPopulate: created USES_IMAGE edges", "count", len(edges))
	return nil
}

// resolveWorkloadImages queries all supported workload types and returns
// USES_IMAGE edges for container images that match known registries.
func resolveWorkloadImages(ctx context.Context, gc postpopulate.GraphCaller, graphName string, index *registryIndex) ([]knowledgev1.Edge, error) {
	workloadTypes := []string{
		"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob",
	}
	var allEdges []knowledgev1.Edge
	seen := make(map[string]struct{}) // dedup by fromID+toID

	for _, wt := range workloadTypes {
		nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery(wt))
		if err != nil {
			return nil, fmt.Errorf("image lineage: query %s: %w", wt, err)
		}
		for _, node := range nodes {
			images := extractContainerImages(node.Content, wt)
			edges := matchImages(node.Id, images, index, seen)
			allEdges = append(allEdges, edges...)
		}
	}
	return allEdges, nil
}

// matchImages resolves parsed image refs against the registry index and returns
// USES_IMAGE edges. The seen map deduplicates by (workloadID, registryNodeID).
func matchImages(workloadID string, images []string, index *registryIndex, seen map[string]struct{}) []knowledgev1.Edge {
	var edges []knowledgev1.Edge
	for _, img := range images {
		ref := cloud.ParseImageRef(img)
		targetID := index.match(ref)
		if targetID == "" {
			continue
		}
		key := workloadID + "|" + targetID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, knowledgev1.Edge{
			FromId:   workloadID,
			ToId:     targetID,
			Type:     string(kgtypes.EdgeUsesImage),
			Method:   "postpopulate:image-lineage",
			Evidence: ref.Full,
		})
	}
	return edges
}

// extractContainerImages extracts container image strings from a workload
// node's Content JSON. It uses generic JSON traversal to find the container
// arrays at the appropriate path for each workload type.
func extractContainerImages(content, workloadType string) []string {
	if content == "" {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	var podSpec map[string]json.RawMessage
	switch workloadType {
	case "CronJob":
		podSpec = drillJSON(raw, "spec", "jobTemplate", "spec", "template", "spec")
	default:
		// Deployment, StatefulSet, DaemonSet, Job all use spec.template.spec
		podSpec = drillJSON(raw, "spec", "template", "spec")
	}
	if podSpec == nil {
		return nil
	}

	var images []string
	images = append(images, extractImagesFromContainerArray(podSpec["containers"])...)
	images = append(images, extractImagesFromContainerArray(podSpec["initContainers"])...)
	return images
}

// extractImagesFromContainerArray parses a JSON array of containers and
// extracts the "image" field from each.
func extractImagesFromContainerArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var containers []struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(raw, &containers); err != nil {
		return nil
	}
	var images []string
	for _, c := range containers {
		if c.Image != "" {
			images = append(images, c.Image)
		}
	}
	return images
}

// drillJSON traverses nested JSON objects by the given keys.
func drillJSON(obj map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	cur := obj
	for _, k := range keys {
		raw, ok := cur[k]
		if !ok {
			return nil
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil
		}
		cur = next
	}
	return cur
}
