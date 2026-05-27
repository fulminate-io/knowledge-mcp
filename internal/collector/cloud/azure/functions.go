// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

type functionsCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newFunctionsCollector(cred azcore.TokenCredential, subID string) *functionsCollector {
	return &functionsCollector{cred: cred, subscriptionID: subID}
}

func (c *functionsCollector) Name() string { return "azure-functions" }

func (c *functionsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armappservice.NewWebAppsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-functions: client: %w", err)
	}

	var result cloud.SubCollectorResult
	seenProxies := map[string]bool{}

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-functions: list: %w", err)
		}

		for _, site := range page.Value {
			if site.ID == nil || site.Name == nil {
				continue
			}
			if !isFunctionApp(site.Kind) {
				continue
			}

			content, err := json.Marshal(buildSiteContent(site))
			if err != nil {
				return result, fmt.Errorf("azure-functions: marshal site content: %w", err)
			}

			result.Resources = append(result.Resources, funcResourceSpec(site, content))
			edges, proxies := funcEdges(site, c.subscriptionID, seenProxies)
			result.Edges = append(result.Edges, edges...)
			result.Resources = append(result.Resources, proxies...)
			c.collectFunctionTriggers(ctx, client, site, seenProxies, &result)
		}
	}

	return result, nil
}

// isFunctionApp returns true when the site's Kind field indicates a function
// app. Azure sets Kind to "functionapp", "functionapp,linux", etc.
func isFunctionApp(kind *string) bool {
	if kind == nil {
		return false
	}
	for part := range strings.SplitSeq(strings.ToLower(*kind), ",") {
		if strings.TrimSpace(part) == "functionapp" {
			return true
		}
	}
	return false
}

func funcResourceSpec(site *armappservice.Site, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *site.ID,
		Name:         *site.Name,
		ResourceType: "Microsoft.Web/sites/functionapp",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if site.Location != nil {
		spec.Region = *site.Location
	}
	if site.Kind != nil {
		spec.Metadata["kind"] = *site.Kind
	}
	funcPropertiesMetadata(site.Properties, spec.Metadata)
	return spec
}

func funcPropertiesMetadata(p *armappservice.SiteProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.State != nil {
		meta["state"] = *p.State
	}
	if p.DefaultHostName != nil {
		meta["defaultHostName"] = *p.DefaultHostName
	}
	if p.HTTPSOnly != nil {
		meta["httpsOnly"] = fmt.Sprintf("%t", *p.HTTPSOnly)
	}
}

// funcEdges extracts edges from a function app: managed identity (ASSUMES_ROLE),
// VNet integration (USES_SUBNET), and Key Vault references (MOUNTS_SECRET +
// proxy vault nodes for unresolvable vault names).
func funcEdges(
	site *armappservice.Site, subscriptionID string, seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec

	// Managed identity → ASSUMES_ROLE.
	edges = append(edges, webAppIdentityEdges(*site.ID, site.Identity)...)

	// VNet integration → USES_SUBNET.
	if e := webAppSubnetEdge(*site.ID, site.Properties); e != nil {
		edges = append(edges, *e)
	}

	// Key Vault references in app settings → MOUNTS_SECRET + proxy vault nodes.
	if site.Properties != nil && site.Properties.SiteConfig != nil {
		kvEdges, kvProxies := webAppKeyVaultRefEdges(
			*site.ID, subscriptionID, site.Properties.SiteConfig.AppSettings, seenProxies,
		)
		edges = append(edges, kvEdges...)
		proxies = append(proxies, kvProxies...)
	}

	return edges, proxies
}
