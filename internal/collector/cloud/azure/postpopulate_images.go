// SPDX-License-Identifier: Apache-2.0

package azure

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

// resolveAzureImageLineage creates USES_IMAGE edges from Azure App Service
// and Function App nodes to ACR repository nodes. It builds an index of ACR
// login servers, then parses LinuxFxVersion/WindowsFxVersion from site Content
// JSON to extract container images.
func resolveAzureImageLineage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	acrIndex, err := buildACRIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("azure image lineage: build ACR index: %w", err)
	}
	if len(acrIndex) == 0 {
		return nil
	}

	var edges []knowledgev1.Edge
	seen := make(map[string]struct{})

	// Query App Service and Function App nodes.
	for _, rt := range []string{"Microsoft.Web/sites", "Microsoft.Web/sites/functionapp"} {
		sites, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
			"type": string(kgtypes.NodeCloudResource),
			"meta": map[string]string{"resource_type": rt},
		})
		if err != nil {
			return fmt.Errorf("azure image lineage: query %s: %w", rt, err)
		}
		for _, node := range sites {
			img := extractAzureContainerImage(node.Content)
			if img == "" {
				continue
			}
			ref := cloud.ParseImageRef(img)
			targetID := matchACRImage(ref, acrIndex)
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
		return fmt.Errorf("azure image lineage: link batch: %w", err)
	}
	slog.Debug("azure postPopulate: created USES_IMAGE edges", "count", len(edges))
	return nil
}

// buildACRIndex queries ACR registry nodes and builds a map from login server
// hostname (e.g. "myregistry.azurecr.io") to node ID.
func buildACRIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]string, error) {
	registries, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "Microsoft.ContainerRegistry/registries"},
	})
	if err != nil {
		return nil, err
	}
	index := make(map[string]string, len(registries))
	for _, n := range registries {
		loginServer := extractACRLoginServer(n.Content)
		if loginServer != "" {
			index[strings.ToLower(loginServer)] = n.Id
		}
	}
	return index, nil
}

// extractACRLoginServer parses the ACR registry Content JSON to find the
// login server hostname (Properties.LoginServer).
func extractACRLoginServer(content string) string {
	if content == "" {
		return ""
	}
	var reg registryContent
	if err := json.Unmarshal([]byte(content), &reg); err != nil {
		return ""
	}
	return reg.Properties.LoginServer
}

// extractAzureContainerImage extracts the container image from an App Service
// or Function App site Content JSON. The image is in SiteConfig.LinuxFxVersion
// or SiteConfig.WindowsFxVersion, formatted as "DOCKER|<image>".
func extractAzureContainerImage(content string) string {
	if content == "" {
		return ""
	}
	var site siteContent
	if err := json.Unmarshal([]byte(content), &site); err != nil {
		return ""
	}
	if img := parseDockerFxVersion(site.Properties.SiteConfig.LinuxFxVersion); img != "" {
		return img
	}
	return parseDockerFxVersion(site.Properties.SiteConfig.WindowsFxVersion)
}

// parseDockerFxVersion extracts the image from "DOCKER|<image>" format.
func parseDockerFxVersion(fxVersion string) string {
	if fxVersion == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToUpper(fxVersion), "DOCKER|") {
		return ""
	}
	return fxVersion[7:] // strip "DOCKER|"
}

// matchACRImage returns the ACR registry node ID matching the given image ref.
func matchACRImage(ref cloud.ImageRef, acrIndex map[string]string) string {
	if ref.RegistryKind() != cloud.RegistryACR {
		return ""
	}
	return acrIndex[strings.ToLower(ref.Registry)]
}
