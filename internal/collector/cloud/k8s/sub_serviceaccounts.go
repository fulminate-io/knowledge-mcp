// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	// IRSA annotation on EKS ServiceAccounts.
	annotationIRSARoleARN = "eks.amazonaws.com/role-arn"

	// GCP Workload Identity annotation.
	annotationGCPWorkloadIdentity = "iam.gke.io/gcp-service-account"

	// Azure Workload Identity annotation.
	annotationAzureClientID = "azure.workload.identity/client-id"
)

// serviceAccountsSubCollector lists all ServiceAccounts across all namespaces.
type serviceAccountsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *serviceAccountsSubCollector) Name() string { return "serviceaccounts" }

func (s *serviceAccountsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().ServiceAccounts("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list serviceaccounts: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, sa := range list.Items {
		id := resourceID(sa.Namespace, "ServiceAccount", sa.Name)

		meta := labelsToMeta(sa.Labels)
		meta["namespace"] = sa.Namespace

		// Cascade targets from IAM annotations.

		// AWS IRSA: eks.amazonaws.com/role-arn → arn:aws:iam::<account>:role/<name>
		if roleARN, ok := sa.Annotations[annotationIRSARoleARN]; ok {
			meta["irsa_role_arn"] = roleARN
			if accountID := extractAWSAccountFromARN(roleARN); accountID != "" {
				result.Targets = append(result.Targets, cloud.CollectTarget{
					Collector: "aws",
					ID:        accountID,
				})
			}
		}

		// GCP Workload Identity: iam.gke.io/gcp-service-account → <name>@<project>.iam.gserviceaccount.com
		if gcpSA, ok := sa.Annotations[annotationGCPWorkloadIdentity]; ok {
			meta["gcp_service_account"] = gcpSA
			if project := extractGCPProjectFromSA(gcpSA); project != "" {
				result.Targets = append(result.Targets, cloud.CollectTarget{
					Collector: "gcp",
					ID:        project,
				})
			}
		}

		// Azure Workload Identity: azure.workload.identity/client-id → <guid>
		if clientID, ok := sa.Annotations[annotationAzureClientID]; ok {
			meta["azure_client_id"] = clientID
			// Azure subscriptions aren't directly derivable from client-id,
			// so we use the client-id as the cascade ID.
			result.Targets = append(result.Targets, cloud.CollectTarget{
				Collector: "azure",
				ID:        clientID,
			})
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         sa.Name,
			ResourceType: "ServiceAccount",
			Region:       sa.Namespace,
			Content:      marshalJSON(sa),
			Metadata:     meta,
		})

		// Edges to imagePullSecrets.
		for _, ips := range sa.ImagePullSecrets {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID(sa.Namespace, "Secret", ips.Name),
				Relationship: kgtypes.EdgeMountsSecret,
			})
		}
	}

	return result, nil
}

// extractAWSAccountFromARN extracts the account ID from an IAM ARN.
// Example: arn:aws:iam::123456789012:role/my-role → "123456789012"
func extractAWSAccountFromARN(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

// extractGCPProjectFromSA extracts the project ID from a GCP service account email.
// Example: my-sa@my-project.iam.gserviceaccount.com → "my-project"
func extractGCPProjectFromSA(email string) string {
	_, domain, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	project, _, found := strings.Cut(domain, ".")
	if !found {
		return ""
	}
	return project
}
