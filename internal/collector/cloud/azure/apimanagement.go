// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/apimanagement/armapimanagement/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type apimCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newAPIMCollector(cred azcore.TokenCredential, subID string) *apimCollector {
	return &apimCollector{cred: cred, subscriptionID: subID}
}

func (c *apimCollector) Name() string { return "azure-apim" }

func (c *apimCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	svcClient, err := armapimanagement.NewServiceClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-apim: service client: %w", err)
	}

	apiClient, err := armapimanagement.NewAPIClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-apim: api client: %w", err)
	}

	var result cloud.SubCollectorResult
	seenProxies := map[string]bool{}
	seenBackendEdges := map[string]bool{}

	pager := svcClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-apim: list: %w", err)
		}

		for _, svc := range page.Value {
			if svc.ID == nil || svc.Name == nil {
				continue
			}

			content, err := json.Marshal(buildAPIMServiceContent(svc))
			if err != nil {
				return result, fmt.Errorf("azure-apim: marshal service content: %w", err)
			}

			result.Resources = append(result.Resources, apimResourceSpec(svc, content))
			result.Edges = append(result.Edges, apimEdges(svc)...)

			rg := parseResourceGroup(*svc.ID)
			if rg == "" {
				continue
			}
			c.collectAPIs(ctx, apiClient, rg, svc, seenProxies, seenBackendEdges, &result)
		}
	}

	return result, nil
}

// collectAPIs lists APIs within an APIM instance and emits ROUTES_TO edges
// for each API that has a backend service URL. The edge target is a sentinel
// proxy keyed on the URL host (a postpopulate resolver can later promote to
// the real App Service / AKS / external resource ID by hostname match).
// seenBackendEdges dedupes within an APIM instance when multiple APIs share
// a backend host.
func (c *apimCollector) collectAPIs(
	ctx context.Context,
	client *armapimanagement.APIClient,
	resourceGroup string,
	svc *armapimanagement.ServiceResource,
	seenProxies map[string]bool,
	seenBackendEdges map[string]bool,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByServicePager(resourceGroup, *svc.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break // best-effort — continue with other instances
		}

		for _, api := range page.Value {
			if api.ID == nil || api.Name == nil {
				continue
			}

			content, err := json.Marshal(buildAPIMAPIContent(api))
			if err != nil {
				break // best-effort — collectAPIs is void-return; abort this instance's APIs
			}

			result.Resources = append(result.Resources, apimAPIResourceSpec(api, svc, content))

			if api.Properties == nil || api.Properties.ServiceURL == nil || *api.Properties.ServiceURL == "" {
				continue
			}
			edge, proxy, ok := apimBackendEdge(*svc.ID, *api.Properties.ServiceURL, c.subscriptionID, seenProxies)
			if !ok {
				continue
			}
			edgeKey := edge.SourceID + "|" + edge.TargetID
			if seenBackendEdges[edgeKey] {
				continue
			}
			seenBackendEdges[edgeKey] = true
			result.Edges = append(result.Edges, edge)
			if proxy != nil {
				result.Resources = append(result.Resources, *proxy)
			}
		}
	}
}

// apimBackendSentinelID returns the sentinel ID for an APIM backend keyed on
// the URL host. Hostnames are stable across deployments where ServiceURL
// paths often vary; a postpopulate resolver can match host → real backend
// (App Service site default hostname, custom domain, AKS LB FQDN) once the
// relevant subcollectors are wired.
func apimBackendSentinelID(host string) string {
	return "azure:apim:backend:" + host
}

// apimBackendEdge builds the ROUTES_TO edge + sentinel proxy for an APIM
// backend URL. Returns ok=false when the URL is unparseable or has no host.
func apimBackendEdge(
	apimID, serviceURL, subscriptionID string,
	seenProxies map[string]bool,
) (cloud.EdgeSpec, *cloud.ResourceSpec, bool) {
	parsed, err := url.Parse(serviceURL)
	if err != nil {
		return cloud.EdgeSpec{}, nil, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return cloud.EdgeSpec{}, nil, false
	}
	sentinelID := apimBackendSentinelID(host)
	edge := cloud.EdgeSpec{
		SourceID:     apimID,
		TargetID:     sentinelID,
		Relationship: kgtypes.EdgeRoutesTo,
		Metadata:     map[string]string{"serviceUrl": serviceURL},
	}
	var proxy *cloud.ResourceSpec
	if seenProxies != nil && !seenProxies[sentinelID] {
		seenProxies[sentinelID] = true
		proxy = &cloud.ResourceSpec{
			ID:           sentinelID,
			Name:         host,
			ResourceType: "azure:apim:backend",
			Metadata: map[string]string{
				"collected":        "false",
				"collected_reason": "APIM backend URL resolved without backend resource ID",
				"discovered_via":   "apim api serviceUrl",
				"subscription_id":  subscriptionID,
				"host":             host,
			},
		}
	}
	return edge, proxy, true
}

func apimResourceSpec(svc *armapimanagement.ServiceResource, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *svc.ID,
		Name:         *svc.Name,
		ResourceType: "Microsoft.ApiManagement/service",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if svc.Location != nil {
		spec.Region = *svc.Location
	}
	if svc.SKU != nil && svc.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*svc.SKU.Name)
	}
	if svc.Properties != nil {
		apimPropertiesMetadata(svc.Properties, spec.Metadata)
	}
	return spec
}

func apimPropertiesMetadata(p *armapimanagement.ServiceProperties, meta map[string]string) {
	if p.PublisherEmail != nil {
		meta["publisherEmail"] = *p.PublisherEmail
	}
	if p.GatewayURL != nil {
		meta["gatewayUrl"] = *p.GatewayURL
	}
}

func apimAPIResourceSpec(
	api *armapimanagement.APIContract,
	svc *armapimanagement.ServiceResource,
	content []byte,
) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *api.ID,
		Name:         *api.Name,
		ResourceType: "Microsoft.ApiManagement/service/apis",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if svc.Location != nil {
		spec.Region = *svc.Location
	}
	if api.Properties != nil && api.Properties.ServiceURL != nil {
		spec.Metadata["serviceUrl"] = *api.Properties.ServiceURL
	}
	return spec
}

// apimEdges extracts USES_SUBNET and ASSUMES_ROLE edges from an APIM instance.
func apimEdges(svc *armapimanagement.ServiceResource) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// VNet integration → USES_SUBNET.
	if svc.Properties != nil && svc.Properties.VirtualNetworkConfiguration != nil {
		if subnetID := svc.Properties.VirtualNetworkConfiguration.SubnetResourceID; subnetID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *svc.ID,
				TargetID:     *subnetID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}

	// Managed identity → ASSUMES_ROLE.
	if svc.Identity != nil && svc.Identity.UserAssignedIdentities != nil {
		for identityID := range svc.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *svc.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	return edges
}

// apimServiceContent is the curated wire shape for
// Microsoft.ApiManagement/service. Curated projection of
// *armapimanagement.ServiceResource (collector-owned, decoupled from SDK
// version). No reader currently consumes Content — fields enumerate the
// metadata-extractor field set for symmetry.
//
// Excluded: VirtualNetworkConfiguration, Identity — pre-Marshal at
// apimanagement.go:166,177.
type apimServiceContent struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	Location   string                        `json:"location,omitempty"`
	SKU        *apimServiceContentSKU        `json:"sku,omitempty"`
	Properties *apimServiceContentProperties `json:"properties,omitempty"`
}

type apimServiceContentSKU struct {
	Name string `json:"name,omitempty"`
}

type apimServiceContentProperties struct {
	PublisherEmail string `json:"publisherEmail,omitempty"`
	GatewayURL     string `json:"gatewayUrl,omitempty"`
}

// buildAPIMServiceContent projects an *armapimanagement.ServiceResource into
// the apimServiceContent wire shape. Nil-safe at every level.
func buildAPIMServiceContent(svc *armapimanagement.ServiceResource) apimServiceContent {
	out := apimServiceContent{}
	if svc == nil {
		return out
	}
	if svc.ID != nil {
		out.ID = *svc.ID
	}
	if svc.Name != nil {
		out.Name = *svc.Name
	}
	if svc.Location != nil {
		out.Location = *svc.Location
	}
	if svc.SKU != nil && svc.SKU.Name != nil {
		out.SKU = &apimServiceContentSKU{Name: string(*svc.SKU.Name)}
	}
	if svc.Properties != nil {
		props := &apimServiceContentProperties{}
		if svc.Properties.PublisherEmail != nil {
			props.PublisherEmail = *svc.Properties.PublisherEmail
		}
		if svc.Properties.GatewayURL != nil {
			props.GatewayURL = *svc.Properties.GatewayURL
		}
		out.Properties = props
	}
	return out
}

// apimAPIContent is the curated wire shape for
// Microsoft.ApiManagement/service/apis. Curated projection of
// *armapimanagement.APIContract (collector-owned, decoupled from SDK
// version). Surfaces ServiceURL into Content (also written to Metadata
// at apimanagement.go:155 as serviceUrl).
//
// Excluded: openAPI spec body, policies — no reader.
type apimAPIContent struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Properties *apimAPIContentProperties `json:"properties,omitempty"`
}

type apimAPIContentProperties struct {
	ServiceURL  string `json:"serviceUrl,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Path        string `json:"path,omitempty"`
}

// buildAPIMAPIContent projects an *armapimanagement.APIContract into the
// apimAPIContent wire shape. Nil-safe at every level.
func buildAPIMAPIContent(api *armapimanagement.APIContract) apimAPIContent {
	out := apimAPIContent{}
	if api == nil {
		return out
	}
	if api.ID != nil {
		out.ID = *api.ID
	}
	if api.Name != nil {
		out.Name = *api.Name
	}
	if api.Properties != nil {
		props := &apimAPIContentProperties{}
		if api.Properties.ServiceURL != nil {
			props.ServiceURL = *api.Properties.ServiceURL
		}
		if api.Properties.DisplayName != nil {
			props.DisplayName = *api.Properties.DisplayName
		}
		if api.Properties.Path != nil {
			props.Path = *api.Properties.Path
		}
		out.Properties = props
	}
	return out
}
