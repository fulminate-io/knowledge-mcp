// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type frontDoorCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newFrontDoorCollector(cred azcore.TokenCredential, subID string) *frontDoorCollector {
	return &frontDoorCollector{cred: cred, subscriptionID: subID}
}

func (c *frontDoorCollector) Name() string { return "azure-frontdoor" }

func (c *frontDoorCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	profilesClient, err := armcdn.NewProfilesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: profiles client: %w", err)
	}

	endpointsClient, err := armcdn.NewAFDEndpointsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: endpoints client: %w", err)
	}

	originGroupsClient, err := armcdn.NewAFDOriginGroupsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: origin groups client: %w", err)
	}

	originsClient, err := armcdn.NewAFDOriginsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: origins client: %w", err)
	}

	secPolicyClient, err := armcdn.NewSecurityPoliciesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: security policies client: %w", err)
	}

	customDomainsClient, err := armcdn.NewAFDCustomDomainsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-frontdoor: custom domains client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := profilesClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-frontdoor: list profiles: %w", err)
		}

		for _, profile := range page.Value {
			if profile.ID == nil || profile.Name == nil {
				continue
			}
			if !isFrontDoorProfile(profile) {
				continue
			}
			c.collectProfile(ctx, profile, endpointsClient, originGroupsClient, originsClient, secPolicyClient, customDomainsClient, &result)
		}
	}

	return result, nil
}

func isFrontDoorProfile(profile *armcdn.Profile) bool {
	if profile.SKU == nil || profile.SKU.Name == nil {
		return false
	}
	return *profile.SKU.Name == armcdn.SKUNameStandardAzureFrontDoor ||
		*profile.SKU.Name == armcdn.SKUNamePremiumAzureFrontDoor
}

func (c *frontDoorCollector) collectProfile(
	ctx context.Context,
	profile *armcdn.Profile,
	epClient *armcdn.AFDEndpointsClient,
	ogClient *armcdn.AFDOriginGroupsClient,
	originsClient *armcdn.AFDOriginsClient,
	secPolicyClient *armcdn.SecurityPoliciesClient,
	customDomainsClient *armcdn.AFDCustomDomainsClient,
	result *cloud.SubCollectorResult,
) {
	content, err := json.Marshal(profile)
	if err != nil {
		return
	}

	result.Resources = append(result.Resources, fdProfileResourceSpec(profile, content))

	rg := parseResourceGroup(*profile.ID)
	if rg == "" {
		return
	}

	collectFDEndpoints(ctx, epClient, rg, profile, result)
	collectFDOriginEdges(ctx, ogClient, originsClient, rg, profile, result)
	collectFDSecurityPolicyEdges(ctx, secPolicyClient, rg, profile, result)
	collectFDCustomDomainCertEdges(ctx, customDomainsClient, rg, profile, result)
}

func fdProfileResourceSpec(profile *armcdn.Profile, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *profile.ID,
		Name:         *profile.Name,
		ResourceType: "Microsoft.Cdn/profiles",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if profile.Location != nil {
		spec.Region = *profile.Location
	}
	if profile.SKU != nil && profile.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*profile.SKU.Name)
	}
	return spec
}

// collectFDEndpoints lists AFD endpoints under a profile and emits them as
// child resources with CONTAINS edges from the profile.
func collectFDEndpoints(
	ctx context.Context,
	client *armcdn.AFDEndpointsClient,
	rg string,
	profile *armcdn.Profile,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByProfilePager(rg, *profile.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, ep := range page.Value {
			if ep.ID == nil || ep.Name == nil {
				continue
			}
			content, err := json.Marshal(ep)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, fdEndpointResourceSpec(ep, profile, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *profile.ID,
				TargetID:     *ep.ID,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
}

func fdEndpointResourceSpec(ep *armcdn.AFDEndpoint, _ *armcdn.Profile, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ep.ID,
		Name:         *ep.Name,
		ResourceType: "Microsoft.Cdn/profiles/afdEndpoints",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ep.Location != nil {
		spec.Region = *ep.Location
	}
	if ep.Properties != nil && ep.Properties.HostName != nil {
		spec.Metadata["hostName"] = *ep.Properties.HostName
	}
	return spec
}

// collectFDOriginEdges discovers origin groups and their origins, emitting
// ROUTES_TO edges from the profile to each Azure-backed origin resource.
func collectFDOriginEdges(
	ctx context.Context,
	ogClient *armcdn.AFDOriginGroupsClient,
	originsClient *armcdn.AFDOriginsClient,
	rg string,
	profile *armcdn.Profile,
	result *cloud.SubCollectorResult,
) {
	ogPager := ogClient.NewListByProfilePager(rg, *profile.Name, nil)
	for ogPager.More() {
		ogPage, err := ogPager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, og := range ogPage.Value {
			if og.Name == nil {
				continue
			}
			collectFDOriginsInGroup(ctx, originsClient, rg, profile, *og.Name, result)
		}
	}
}

func collectFDOriginsInGroup(
	ctx context.Context,
	client *armcdn.AFDOriginsClient,
	rg string,
	profile *armcdn.Profile,
	originGroupName string,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByOriginGroupPager(rg, *profile.Name, originGroupName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			break
		}
		for _, origin := range page.Value {
			if origin.Properties == nil || origin.Properties.AzureOrigin == nil {
				continue
			}
			if origin.Properties.AzureOrigin.ID == nil {
				continue
			}
			targetID := *origin.Properties.AzureOrigin.ID
			if !strings.HasPrefix(targetID, "/") {
				continue
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *profile.ID,
				TargetID:     targetID,
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}
	}
}
