// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// certificatesCollector enumerates App Service certificates (Microsoft.Web/certificates).
// These are the ARM-level TLS certificate resources that App Gateway, Front Door
// and App Service bindings point at via USES_CERT edges. Key Vault-backed
// certificates emit an ENCRYPTS_WITH edge to the referenced Key Vault secret.
//
// Key Vault data-plane certificates (azcertificates SDK) are intentionally not
// collected — they require per-vault auth and a separate SDK. App Service
// certificates cover the common ARM-level cert use cases.
type certificatesCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newCertificatesCollector(cred azcore.TokenCredential, subID string) *certificatesCollector {
	return &certificatesCollector{cred: cred, subscriptionID: subID}
}

func (c *certificatesCollector) Name() string { return "azure-certificates" }

func (c *certificatesCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armappservice.NewCertificatesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-certificates: client: %w", err)
	}

	var result cloud.SubCollectorResult
	seenCAs := make(map[string]bool)

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-certificates: list: %w", err)
		}

		for _, cert := range page.Value {
			if cert.ID == nil || cert.Name == nil {
				continue
			}

			content, err := json.Marshal(cert)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, certificateResourceSpec(cert, content))
			edges, caNodes := certificateEdges(cert, seenCAs)
			result.Edges = append(result.Edges, edges...)
			result.Resources = append(result.Resources, caNodes...)
		}
	}

	return result, nil
}

func certificateResourceSpec(cert *armappservice.AppCertificate, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *cert.ID,
		Name:         *cert.Name,
		ResourceType: "Microsoft.Web/certificates",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if cert.Location != nil {
		spec.Region = *cert.Location
	}
	certificatePropertiesMetadata(cert.Properties, spec.Metadata)
	return spec
}

func certificatePropertiesMetadata(p *armappservice.AppCertificateProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.Thumbprint != nil {
		meta["thumbprint"] = *p.Thumbprint
	}
	if p.SubjectName != nil {
		meta["subjectName"] = *p.SubjectName
	}
	if p.Issuer != nil {
		meta["issuer"] = *p.Issuer
	}
	if p.ExpirationDate != nil {
		meta["expirationDate"] = p.ExpirationDate.Format("2006-01-02T15:04:05Z")
	}
	if p.Valid != nil {
		meta["valid"] = fmt.Sprintf("%t", *p.Valid)
	}
	if p.KeyVaultSecretStatus != nil {
		meta["keyVaultSecretStatus"] = string(*p.KeyVaultSecretStatus)
	}
}

// certificateEdges emits edges from an App Service certificate:
//   - EdgeEncryptsWith → KV secret (when cert is backed by Key Vault)
//   - EdgeStoredIn → Key Vault resource ID (the vault itself)
//   - EdgeIssuedBy → synthesized azure:ca/{issuer} node
//
// seenCAs tracks which CA proxy nodes have already been emitted so each
// unique issuer produces exactly one ResourceSpec.
func certificateEdges(
	cert *armappservice.AppCertificate,
	seenCAs map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	if cert.Properties == nil {
		return nil, nil
	}
	var edges []cloud.EdgeSpec
	var caNodes []cloud.ResourceSpec

	edges = append(edges, certKVEdges(cert)...)
	issuerEdges, issuerNodes := certIssuerEdges(cert, seenCAs)
	edges = append(edges, issuerEdges...)
	caNodes = append(caNodes, issuerNodes...)

	return edges, caNodes
}

// certKVEdges emits EdgeEncryptsWith → KV secret and EdgeStoredIn → KV vault
// when the certificate is Key Vault-backed.
func certKVEdges(cert *armappservice.AppCertificate) []cloud.EdgeSpec {
	kvID := cert.Properties.KeyVaultID
	if kvID == nil || *kvID == "" {
		return nil
	}
	var edges []cloud.EdgeSpec
	// EdgeStoredIn: cert → vault resource.
	edges = append(edges, cloud.EdgeSpec{
		SourceID:     *cert.ID,
		TargetID:     *kvID,
		Relationship: kgtypes.EdgeStoredIn,
	})
	// EdgeEncryptsWith: cert → KV secret (only when secret name is also set).
	kvSecret := cert.Properties.KeyVaultSecretName
	if kvSecret != nil && *kvSecret != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *cert.ID,
			TargetID:     *kvID + "/secrets/" + *kvSecret,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}
	return edges
}

// certIssuerEdges emits EdgeIssuedBy → synthetic CA node. The CA node
// uses ID format "azure:ca/{issuer-name}" and is only emitted once per
// unique issuer name (tracked via seenCAs).
func certIssuerEdges(
	cert *armappservice.AppCertificate,
	seenCAs map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	issuer := cert.Properties.Issuer
	if issuer == nil || *issuer == "" {
		return nil, nil
	}
	caID := "azure:ca/" + *issuer
	var caNodes []cloud.ResourceSpec
	if !seenCAs[caID] {
		seenCAs[caID] = true
		caNodes = append(caNodes, cloud.ResourceSpec{
			ID:           caID,
			Name:         *issuer,
			ResourceType: "azure:ca",
			Metadata: map[string]string{
				"collected":        "false",
				"collected_reason": "no collector registered",
				"name":             *issuer,
			},
		})
	}
	edges := []cloud.EdgeSpec{{
		SourceID:     *cert.ID,
		TargetID:     caID,
		Relationship: kgtypes.EdgeIssuedBy,
	}}
	return edges, caNodes
}
