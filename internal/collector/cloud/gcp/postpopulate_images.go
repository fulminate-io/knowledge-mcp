// SPDX-License-Identifier: Apache-2.0

package gcp

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

// resolveCloudRunImageLineage creates USES_IMAGE edges from Cloud Run service
// nodes to Artifact Registry repository nodes. It parses the service Content
// JSON to extract container images from the template, then matches against
// AR repository nodes in the graph. GCR images are also tried against AR
// nodes (Google is migrating GCR to Artifact Registry).
func resolveCloudRunImageLineage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	arIndex, err := buildARIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("cloud run image lineage: build AR index: %w", err)
	}
	if len(arIndex) == 0 {
		return nil
	}

	services, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:run:service"},
	})
	if err != nil {
		return fmt.Errorf("cloud run image lineage: query services: %w", err)
	}

	var edges []knowledgev1.Edge
	seen := make(map[string]struct{})

	for _, node := range services {
		images := extractCloudRunImages(node.Content)
		for _, img := range images {
			ref := cloud.ParseImageRef(img)
			targetID := matchARImage(ref, arIndex)
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
		return fmt.Errorf("cloud run image lineage: link batch: %w", err)
	}
	slog.Debug("gcp postPopulate: created Cloud Run USES_IMAGE edges", "count", len(edges))
	return nil
}

// extractCloudRunImages extracts container image strings from a Cloud Run
// service's Content JSON. The protobuf-JSON has template.containers[].image.
func extractCloudRunImages(content string) []string {
	if content == "" {
		return nil
	}
	var svc cloudRunServiceContent
	if err := json.Unmarshal([]byte(content), &svc); err != nil {
		return nil
	}
	var images []string
	for _, c := range svc.Template.Containers {
		if c.Image != "" {
			images = append(images, c.Image)
		}
	}
	return images
}

// cloudRunServiceContent is a minimal struct for parsing the Cloud Run
// service protobuf-JSON stored in Content.
type cloudRunServiceContent struct {
	Template cloudRunTemplate `json:"template"`
}

type cloudRunTemplate struct {
	Containers []cloudRunContainer `json:"containers"`
}

type cloudRunContainer struct {
	Image string `json:"image"`
}

// buildARIndex queries Artifact Registry nodes and indexes by project/repo.
func buildARIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]string, error) {
	repos, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:artifactregistry:repository"},
	})
	if err != nil {
		return nil, err
	}
	index := make(map[string]string, len(repos))
	for _, n := range repos {
		project, repo := parseARResourceID(n.Id)
		if project != "" {
			index[project+"/"+repo] = n.Id
		}
	}
	return index, nil
}

// matchARImage returns the AR repository node ID matching the given image ref.
// Supports both Artifact Registry and GCR (legacy) image formats.
func matchARImage(ref cloud.ImageRef, index map[string]string) string {
	switch ref.RegistryKind() {
	case cloud.RegistryArtifactRegistry:
		return matchARDirect(ref, index)
	case cloud.RegistryGCR:
		return matchGCRToAR(ref, index)
	default:
		return ""
	}
}

// matchARDirect matches an Artifact Registry image against the index.
// AR images: <region>-docker.pkg.dev/<project>/<repo>/<image>:<tag>
func matchARDirect(ref cloud.ImageRef, index map[string]string) string {
	parts := strings.SplitN(ref.Repository, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return index[parts[0]+"/"+parts[1]]
}

// matchGCRToAR tries matching a GCR image against AR nodes.
// GCR images: gcr.io/<project>/<image> -> try project/<image> in AR index.
func matchGCRToAR(ref cloud.ImageRef, index map[string]string) string {
	parts := strings.SplitN(ref.Repository, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return index[parts[0]+"/"+parts[1]]
}

// parseARResourceID extracts project and repo from an AR resource name.
// Format: projects/<project>/locations/<loc>/repositories/<repo>
func parseARResourceID(id string) (project, repo string) {
	parts := strings.Split(id, "/")
	var p, r string
	for i, seg := range parts {
		if seg == "projects" && i+1 < len(parts) {
			p = parts[i+1]
		}
		if seg == "repositories" && i+1 < len(parts) {
			r = parts[i+1]
		}
	}
	return p, r
}
