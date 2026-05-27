// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectFDSecurityPolicyEdges lists security policies under a Front Door
// profile and emits PROTECTS edges from each WAF policy to the profile.
func collectFDSecurityPolicyEdges(
	ctx context.Context,
	client *armcdn.SecurityPoliciesClient,
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
		for _, sp := range page.Value {
			edges := fdSecurityPolicyProtectsEdges(sp, profile)
			result.Edges = append(result.Edges, edges...)
		}
	}
}

// fdSecurityPolicyProtectsEdges extracts the WAF policy reference from a
// Front Door security policy and returns a PROTECTS edge.
func fdSecurityPolicyProtectsEdges(sp *armcdn.SecurityPolicy, profile *armcdn.Profile) []cloud.EdgeSpec {
	if sp.Properties == nil || sp.Properties.Parameters == nil {
		return nil
	}

	wafParams, ok := sp.Properties.Parameters.(*armcdn.SecurityPolicyWebApplicationFirewallParameters)
	if !ok || wafParams.WafPolicy == nil || wafParams.WafPolicy.ID == nil {
		return nil
	}

	return []cloud.EdgeSpec{{
		SourceID:     *wafParams.WafPolicy.ID,
		TargetID:     *profile.ID,
		Relationship: kgtypes.EdgeProtects,
	}}
}

// collectFDCustomDomainCertEdges lists custom domains under a Front Door
// profile and emits USES_CERT edges from the profile to the TLS certificate
// (Key Vault secret reference) for each domain with TLS settings.
func collectFDCustomDomainCertEdges(
	ctx context.Context,
	client *armcdn.AFDCustomDomainsClient,
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
		for _, domain := range page.Value {
			if edge := fdCustomDomainCertEdge(domain, profile); edge != nil {
				result.Edges = append(result.Edges, *edge)
			}
		}
	}
}

// fdCustomDomainCertEdge returns a USES_CERT edge if the custom domain has a
// TLS certificate reference (Key Vault secret).
func fdCustomDomainCertEdge(domain *armcdn.AFDDomain, profile *armcdn.Profile) *cloud.EdgeSpec {
	if domain.Properties == nil || domain.Properties.TLSSettings == nil {
		return nil
	}
	secret := domain.Properties.TLSSettings.Secret
	if secret == nil || secret.ID == nil || *secret.ID == "" {
		return nil
	}
	return &cloud.EdgeSpec{
		SourceID:     *profile.ID,
		TargetID:     *secret.ID,
		Relationship: kgtypes.EdgeUsesCert,
	}
}
