// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// siteContent is the curated wire shape for Microsoft.Web/sites and
// Microsoft.Web/sites/functionapp resources. Shared between appservice.go
// and functions.go because *armappservice.Site is identical for both and
// the metadata extractors (appSvcPropertiesMetadata + funcPropertiesMetadata)
// read the same field set. Curated projection of *armappservice.Site
// (collector-owned, decoupled from SDK version).
//
// Convergence target for extractAzureContainerImage at
// postpopulate_images.go:114 — JSON tags use lowerCamelCase to match the
// existing reader and the Azure ARM JSON shape.
//
// Excluded: Properties.VirtualNetworkSubnetID, Identity,
// Properties.SiteConfig.AppSettings — read on the SDK shape pre-Marshal at
// webapps_helpers.go:18,36,61. None flow through Content.
type siteContent struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Location   string                `json:"location,omitempty"`
	Kind       string                `json:"kind,omitempty"`
	Properties siteContentProperties `json:"properties"`
}

type siteContentProperties struct {
	State           string                `json:"state,omitempty"`
	DefaultHostName string                `json:"defaultHostName,omitempty"`
	HTTPSOnly       *bool                 `json:"httpsOnly,omitempty"`
	SiteConfig      siteContentSiteConfig `json:"siteConfig"`
}

type siteContentSiteConfig struct {
	LinuxFxVersion   string `json:"linuxFxVersion,omitempty"`
	WindowsFxVersion string `json:"windowsFxVersion,omitempty"`
}

// buildSiteContent projects an *armappservice.Site into the curated
// siteContent wire shape. Nil-safe at every level — missing pointers
// collapse to zero values that omitempty hides on the wire.
func buildSiteContent(site *armappservice.Site) siteContent {
	out := siteContent{}
	if site == nil {
		return out
	}
	if site.ID != nil {
		out.ID = *site.ID
	}
	if site.Name != nil {
		out.Name = *site.Name
	}
	if site.Location != nil {
		out.Location = *site.Location
	}
	if site.Kind != nil {
		out.Kind = *site.Kind
	}
	if site.Properties != nil {
		out.Properties = projectSiteProperties(site.Properties)
	}
	return out
}

func projectSiteProperties(p *armappservice.SiteProperties) siteContentProperties {
	out := siteContentProperties{}
	if p.State != nil {
		out.State = *p.State
	}
	if p.DefaultHostName != nil {
		out.DefaultHostName = *p.DefaultHostName
	}
	if p.HTTPSOnly != nil {
		b := *p.HTTPSOnly
		out.HTTPSOnly = &b
	}
	if p.SiteConfig != nil {
		if p.SiteConfig.LinuxFxVersion != nil {
			out.SiteConfig.LinuxFxVersion = *p.SiteConfig.LinuxFxVersion
		}
		if p.SiteConfig.WindowsFxVersion != nil {
			out.SiteConfig.WindowsFxVersion = *p.SiteConfig.WindowsFxVersion
		}
	}
	return out
}

// webAppIdentityEdges extracts ASSUMES_ROLE edges from a web app's managed
// identity configuration. User-assigned identities produce one edge each.
func webAppIdentityEdges(sourceID string, identity *armappservice.ManagedServiceIdentity) []cloud.EdgeSpec {
	if identity == nil || identity.UserAssignedIdentities == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for identityID := range identity.UserAssignedIdentities {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sourceID,
			TargetID:     identityID,
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "managed_identity"},
		})
	}
	return edges
}

// webAppSubnetEdge returns a USES_SUBNET edge if the site has VNet integration
// configured via VirtualNetworkSubnetID.
func webAppSubnetEdge(sourceID string, props *armappservice.SiteProperties) *cloud.EdgeSpec {
	if props == nil || props.VirtualNetworkSubnetID == nil || *props.VirtualNetworkSubnetID == "" {
		return nil
	}
	return &cloud.EdgeSpec{
		SourceID:     sourceID,
		TargetID:     *props.VirtualNetworkSubnetID,
		Relationship: kgtypes.EdgeUsesSubnet,
	}
}

// kvRefPattern matches Azure Key Vault reference values in app settings.
// Format: @Microsoft.KeyVault(SecretUri=https://<vault>.vault.azure.net/...)
// or @Microsoft.KeyVault(VaultName=<vault>;SecretName=<secret>)
var kvRefPattern = regexp.MustCompile(`@Microsoft\.KeyVault\(`)

// kvSecretURIPattern extracts the vault host from a SecretUri= form.
var kvSecretURIPattern = regexp.MustCompile(`SecretUri=https://([^.]+)\.vault\.azure\.net`)

// kvVaultNamePattern extracts the vault name from a VaultName= form.
var kvVaultNamePattern = regexp.MustCompile(`VaultName=([^;)]+)`)

// kvVaultSentinelID returns the synthetic graph ID for a Key Vault referenced
// by name only. App Service / Function App settings name vaults without a
// resource group, so the real ARM ID cannot be reconstructed at collection
// time. A postpopulate resolver can later promote the sentinel to the real
// vault node once a Key Vault subcollector lands.
func kvVaultSentinelID(vaultName string) string {
	return "azure:keyvault:" + vaultName
}

// webAppKeyVaultRefEdges scans app settings for Key Vault references and emits
// MOUNTS_SECRET edges plus a proxy ResourceSpec for each referenced vault.
// The target ID is a synthetic sentinel (azure:keyvault:<name>) rather than a
// placeholder ARM path — the real ARM ID requires the resource group, which is
// not present in the reference. seenProxies dedupes proxy emission across
// multiple sites that reference the same vault.
func webAppKeyVaultRefEdges(
	sourceID, subscriptionID string,
	settings []*armappservice.NameValuePair,
	seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	seenVaults := map[string]bool{}
	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec

	for _, s := range settings {
		if s.Value == nil || !kvRefPattern.MatchString(*s.Value) {
			continue
		}

		vaultName := extractKVRefVaultName(*s.Value)
		if vaultName == "" || seenVaults[vaultName] {
			continue
		}
		seenVaults[vaultName] = true

		vaultID := kvVaultSentinelID(vaultName)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sourceID,
			TargetID:     vaultID,
			Relationship: kgtypes.EdgeMountsSecret,
		})

		if seenProxies != nil && !seenProxies[vaultID] {
			seenProxies[vaultID] = true
			proxies = append(proxies, cloud.ResourceSpec{
				ID:           vaultID,
				Name:         vaultName,
				ResourceType: "azure:keyvault:vault",
				Metadata: map[string]string{
					"collected":        "false",
					"collected_reason": "key vault reference resolved without resource group",
					"discovered_via":   "appservice key vault reference",
					"subscription_id":  subscriptionID,
				},
			})
		}
	}
	return edges, proxies
}

// extractKVRefVaultName parses the vault name from a Key Vault reference value.
func extractKVRefVaultName(value string) string {
	if m := kvSecretURIPattern.FindStringSubmatch(value); len(m) > 1 {
		return m[1]
	}
	if m := kvVaultNamePattern.FindStringSubmatch(value); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}
