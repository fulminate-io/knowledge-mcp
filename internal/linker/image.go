// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// workloadTypes are the Kubernetes resource types that contain container images.
var workloadTypes = []string{
	"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob",
}

// LinkImageTargets scans cloud workload nodes for container images, extracts
// image names, and matches them against code repository names. For each match
// it emits a mutate(link, link_graph:"linkage") call with the BUILDS edge so
// the server's handleLink path auto-creates the cross-graph proxies and
// writes the edge with metadata.
//
// Client-side port of pkg/linker.LinkImageTargets. The parsing helpers
// (extractImageName, extractContainerImages) are ported verbatim — they're
// pure functions over content.
func LinkImageTargets(ctx context.Context, gc GraphCaller, opts LinkOptions) (int, error) {
	if gc == nil {
		return 0, nil
	}

	codeRepos, err := listCodeRepos(ctx, gc)
	if err != nil {
		return 0, fmt.Errorf("list code repos: %w", err)
	}
	if len(codeRepos) == 0 {
		return 0, nil
	}

	workloadSet := make(map[string]bool, len(workloadTypes))
	for _, wt := range workloadTypes {
		workloadSet[wt] = true
	}

	cloudGraphs, err := listCloudGraphs(ctx, gc)
	if err != nil {
		return 0, fmt.Errorf("list cloud graphs: %w", err)
	}

	linkCount := 0
	for _, cg := range cloudGraphs {
		n, lerr := linkImagesInCloudGraph(ctx, gc, opts, cg, codeRepos, workloadSet)
		if lerr != nil {
			// Best-effort per cloud graph.
			continue
		}
		linkCount += n
	}
	return linkCount, nil
}

// linkImagesInCloudGraph scans a single cloud graph for workload nodes and
// matches their container images against code repository names.
func linkImagesInCloudGraph(ctx context.Context, gc GraphCaller, opts LinkOptions, cloudGraphName string, codeRepos map[string]bool, workloadSet map[string]bool) (int, error) {
	nodes, err := queryCloudResources(ctx, gc, cloudGraphName)
	if err != nil {
		return 0, err
	}

	linkCount := 0
	for _, node := range nodes {
		if !workloadSet[kgtypes.Value(node, "resource_type")] {
			continue
		}
		for _, img := range extractContainerImages(node.Content) {
			imageName := extractImageName(img)
			if imageName == "" || !codeRepos[imageName] {
				continue
			}
			evidence := fmt.Sprintf("image %s matches repo %s", img, imageName)
			// Issue the link with foreign IDs; the server's handleLink
			// auto-creates the cross-graph proxies via ResolveOrProxy and
			// writes the edge in the linkage graph.
			if err := emitLink(ctx, gc, opts, node.Id, imageName, "BUILDS", "tier1-image", evidence, 0.9); err != nil {
				continue
			}
			linkCount++
		}
	}
	return linkCount, nil
}

// extractImageName extracts the repository/project name from a container image
// reference. Ported verbatim from pkg/linker/image.go.
//
//	gcr.io/project/myapp:v1.2.3    -> myapp
//	docker.io/library/nginx:1.25   -> nginx
//	myapp:latest                   -> myapp
//	ghcr.io/org/repo/service@sha256:abc123 -> service
func extractImageName(image string) string {
	if idx := strings.Index(image, "@"); idx != -1 {
		image = image[:idx]
	}
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		afterColon := image[idx+1:]
		if !strings.Contains(afterColon, "/") {
			image = image[:idx]
		}
	}
	if idx := strings.LastIndex(image, "/"); idx != -1 {
		image = image[idx+1:]
	}
	return image
}

// extractContainerImages parses the raw JSON content of a Kubernetes workload
// node and returns all container image references found in containers and
// initContainers. Ported verbatim from pkg/linker/image.go.
func extractContainerImages(content string) []string {
	if content == "" {
		return nil
	}

	type containerSpec struct {
		Image string `json:"image"`
	}
	type podSpec struct {
		Containers     []containerSpec `json:"containers"`
		InitContainers []containerSpec `json:"initContainers"`
	}

	var workload struct {
		Spec struct {
			Template struct {
				Spec podSpec `json:"spec"`
			} `json:"template"`
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec podSpec `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}

	if err := json.Unmarshal([]byte(content), &workload); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var images []string

	collect := func(ps podSpec) {
		for _, c := range ps.Containers {
			if c.Image != "" && !seen[c.Image] {
				seen[c.Image] = true
				images = append(images, c.Image)
			}
		}
		for _, c := range ps.InitContainers {
			if c.Image != "" && !seen[c.Image] {
				seen[c.Image] = true
				images = append(images, c.Image)
			}
		}
	}

	collect(workload.Spec.Template.Spec)
	collect(workload.Spec.JobTemplate.Spec.Template.Spec)

	return images
}

// listCodeRepos returns the set of indexed code graph names (excluding
// @branch overlays). Enumerated via fetchGraphNames (query mode:modules →
// RETURN_MODE_GRAPH_NAMES over the Execute carrier seam).
func listCodeRepos(ctx context.Context, gc GraphCaller) (map[string]bool, error) {
	repos, err := fetchGraphNames(ctx, gc, "code")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(repos))
	for _, r := range repos {
		if !strings.Contains(r, "@") {
			out[r] = true
		}
	}
	return out, nil
}

// listCloudGraphs returns the set of indexed cloud graph names.
func listCloudGraphs(ctx context.Context, gc GraphCaller) ([]string, error) {
	return fetchGraphNames(ctx, gc, "cloud")
}

// queryCloudResources returns NodeCloudResource entries from a named cloud
// graph via the Execute carrier seam (browseNodesViaEngine → nodes_json
// carrier). Structurally identical to the code type-browse, differing only in
// the graph value (cloud) + the name selector.
func queryCloudResources(ctx context.Context, gc GraphCaller, cloudGraphName string) ([]*knowledgev1.Node, error) {
	nodes, err := browseNodesViaEngine(ctx, gc, map[string]any{
		"graph": "cloud",
		"name":  cloudGraphName,
		"type":  string(kgtypes.NodeCloudResource),
		"limit": 0,
	})
	if err != nil {
		return nil, fmt.Errorf("query cloud resources: %w", err)
	}
	return nodes, nil
}
