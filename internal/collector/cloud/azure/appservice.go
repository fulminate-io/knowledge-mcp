// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

type appServiceCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newAppServiceCollector(cred azcore.TokenCredential, subID string) *appServiceCollector {
	return &appServiceCollector{cred: cred, subscriptionID: subID}
}

func (c *appServiceCollector) Name() string { return "azure-appservice" }

func (c *appServiceCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armappservice.NewWebAppsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-appservice: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-appservice: list: %w", err)
		}

		for _, site := range page.Value {
			if site.ID == nil || site.Name == nil {
				continue
			}
			// Skip function apps — those are handled by functionsCollector.
			if isFunctionApp(site.Kind) {
				continue
			}

			content, err := json.Marshal(buildSiteContent(site))
			if err != nil {
				return result, fmt.Errorf("azure-appservice: marshal site content: %w", err)
			}

			result.Resources = append(result.Resources, appSvcResourceSpec(site, content))
			result.Edges = append(result.Edges, appSvcEdges(site)...)
		}
	}

	return result, nil
}

func appSvcResourceSpec(site *armappservice.Site, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *site.ID,
		Name:         *site.Name,
		ResourceType: "Microsoft.Web/sites",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if site.Location != nil {
		spec.Region = *site.Location
	}
	if site.Kind != nil {
		spec.Metadata["kind"] = *site.Kind
	}
	appSvcPropertiesMetadata(site.Properties, spec.Metadata)
	return spec
}

func appSvcPropertiesMetadata(p *armappservice.SiteProperties, meta map[string]string) {
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

// appSvcEdges extracts edges from an App Service site: managed identity
// (ASSUMES_ROLE) and VNet integration (USES_SUBNET).
func appSvcEdges(site *armappservice.Site) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Managed identity → ASSUMES_ROLE.
	edges = append(edges, webAppIdentityEdges(*site.ID, site.Identity)...)

	// VNet integration → USES_SUBNET.
	if e := webAppSubnetEdge(*site.ID, site.Properties); e != nil {
		edges = append(edges, *e)
	}

	return edges
}
