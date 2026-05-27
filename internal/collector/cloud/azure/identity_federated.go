// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// federatedCredentialPager abstracts the Azure SDK pager for federated
// identity credential listing. Tests supply a fake.
type federatedCredentialPager interface {
	More() bool
	NextPage(ctx context.Context) (armmsi.FederatedIdentityCredentialsClientListResponse, error)
}

// collectFederatedCredentials iterates federated identity credentials for a
// managed identity and appends EdgeWorkloadIdentity edges (SA -> IAM direction).
func collectFederatedCredentials(
	ctx context.Context,
	identityID string,
	pager federatedCredentialPager,
	result *cloud.SubCollectorResult,
) error {
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azure-identity: federated list: %w", err)
		}
		for _, cred := range page.Value {
			if cred == nil || cred.Properties == nil {
				continue
			}
			emitFederatedEdge(identityID, cred, result)
		}
	}
	return nil
}

// emitFederatedEdge parses a single federated credential and appends the
// appropriate EdgeWorkloadIdentity edge and optional synthetic ResourceSpec.
func emitFederatedEdge(
	identityID string,
	cred *armmsi.FederatedIdentityCredential,
	result *cloud.SubCollectorResult,
) {
	props := cred.Properties
	if props.Issuer == nil || props.Subject == nil {
		return
	}
	issuer := *props.Issuer
	subject := *props.Subject

	md := map[string]string{
		"issuer":  issuer,
		"subject": subject,
	}
	if len(props.Audiences) > 0 {
		md["audiences"] = joinAudiences(props.Audiences)
	}

	sourceID := federatedSourceID(issuer, subject)
	if sourceID == "" {
		return
	}

	// Add synthetic resource for GitHub/generic OIDC sources so the node
	// exists in the graph for traversal.
	if synth := syntheticFederatedResource(issuer, subject, sourceID); synth != nil {
		result.Resources = append(result.Resources, *synth)
	}

	result.Edges = append(result.Edges, cloud.EdgeSpec{
		SourceID:     sourceID,
		TargetID:     identityID,
		Relationship: kgtypes.EdgeWorkloadIdentity,
		Metadata:     md,
	})
}

// federatedSourceID determines the edge source node ID based on the issuer and
// subject. Returns empty string if the credential cannot be classified.
func federatedSourceID(issuer, subject string) string {
	if ns, sa, ok := parseK8sSASubject(subject); ok && isAKSIssuer(issuer) {
		return ns + "/ServiceAccount/" + sa
	}
	if org, repo, ok := parseGitHubSubject(subject); ok && isGitHubIssuer(issuer) {
		return "github:" + org + "/" + repo
	}
	// Generic OIDC fallback.
	return "oidc:" + issuer + "/" + subject
}

// syntheticFederatedResource creates a proxy ResourceSpec for GitHub and
// generic OIDC sources. AKS sources are expected to already exist as K8s
// ServiceAccount nodes, so no synthetic resource is needed.
func syntheticFederatedResource(issuer, subject, sourceID string) *cloud.ResourceSpec {
	if _, _, ok := parseK8sSASubject(subject); ok && isAKSIssuer(issuer) {
		return nil
	}
	resType := "oidc:identity"
	if isGitHubIssuer(issuer) {
		resType = "github:identity"
	}
	return &cloud.ResourceSpec{
		ID:           sourceID,
		Name:         sourceID,
		ResourceType: resType,
		Metadata: map[string]string{
			"issuer":  issuer,
			"subject": subject,
		},
	}
}

// isAKSIssuer returns true if the issuer URL looks like an AKS OIDC endpoint.
func isAKSIssuer(issuer string) bool {
	return strings.Contains(issuer, ".oic.prod-aks.azure.com")
}

// isGitHubIssuer returns true if the issuer is GitHub Actions OIDC.
func isGitHubIssuer(issuer string) bool {
	return strings.Contains(issuer, "token.actions.githubusercontent.com")
}

// parseK8sSASubject parses "system:serviceaccount:{ns}:{name}" and returns
// the namespace and service account name.
func parseK8sSASubject(subject string) (ns, name string, ok bool) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(subject, prefix) {
		return "", "", false
	}
	rest := subject[len(prefix):]
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// parseGitHubSubject parses "repo:{org}/{repo}:..." and returns org, repo.
func parseGitHubSubject(subject string) (org, repo string, ok bool) {
	const prefix = "repo:"
	if !strings.HasPrefix(subject, prefix) {
		return "", "", false
	}
	rest := subject[len(prefix):]
	// Format: {org}/{repo}:ref:... or {org}/{repo}:environment:...
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		colonIdx = len(rest)
	}
	orgRepo := rest[:colonIdx]
	parts := strings.SplitN(orgRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// extractIdentityName returns the last segment of an ARM resource ID, which
// for user-assigned identities is the identity name.
func extractIdentityName(armID string) string {
	parts := strings.Split(armID, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

// joinAudiences concatenates audience strings with commas.
func joinAudiences(audiences []*string) string {
	strs := make([]string, 0, len(audiences))
	for _, a := range audiences {
		if a != nil {
			strs = append(strs, *a)
		}
	}
	return strings.Join(strs, ",")
}
