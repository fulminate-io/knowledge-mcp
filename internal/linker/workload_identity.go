// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// LinkWorkloadIdentity scans cloud graphs for Kubernetes ServiceAccounts with
// workload identity annotations (IRSA, GCP Workload Identity, Azure Workload
// Identity) and emits mutate(link, link_graph:"linkage") with the
// WORKLOAD_IDENTITY edge to the corresponding cloud IAM identity.
//
// Client-side port of pkg/linker.LinkWorkloadIdentity; awsAccountFromRoleARN,
// gcpProjectFromSAEmail are pure helpers ported verbatim.
func LinkWorkloadIdentity(ctx context.Context, gc GraphCaller, opts LinkOptions) (int, error) {
	if gc == nil {
		return 0, nil
	}
	cloudGraphs, err := listCloudGraphs(ctx, gc)
	if err != nil {
		return 0, fmt.Errorf("list cloud graphs: %w", err)
	}
	linkCount := 0
	for _, name := range cloudGraphs {
		n, lerr := linkWIInCloudGraph(ctx, gc, opts, name, cloudGraphs)
		if lerr != nil {
			continue
		}
		linkCount += n
	}
	return linkCount, nil
}

// linkWIInCloudGraph scans a single cloud graph for K8s ServiceAccounts with
// workload identity metadata and emits links.
func linkWIInCloudGraph(ctx context.Context, gc GraphCaller, opts LinkOptions, cloudGraphName string, allClouds []string) (int, error) {
	nodes, err := queryCloudResources(ctx, gc, cloudGraphName)
	if err != nil {
		return 0, err
	}
	linkCount := 0
	for _, node := range nodes {
		if kgtypes.Value(node, "resource_type") != "ServiceAccount" {
			continue
		}
		linkCount += linkIRSA(ctx, gc, opts, node)
		linkCount += linkGCPWorkloadIdentity(ctx, gc, opts, node)
		linkCount += linkAzureWorkloadIdentity(ctx, gc, opts, allClouds, node)
	}
	return linkCount, nil
}

// linkIRSA emits a WORKLOAD_IDENTITY edge for an IRSA-annotated SA. The IAM
// role ARN is used directly as the target node ID; the server's
// handleLink + ResolveOrProxy looks it up across cloud graphs.
func linkIRSA(ctx context.Context, gc GraphCaller, opts LinkOptions, sa *knowledgev1.Node) int {
	roleARN := kgtypes.Value(sa, "irsa_role_arn")
	if roleARN == "" {
		return 0
	}
	evidence := fmt.Sprintf("K8s SA %s → %s via irsa", sa.Id, roleARN)
	if err := emitLink(ctx, gc, opts, sa.Id, roleARN, "WORKLOAD_IDENTITY", "tier1-irsa", evidence, 1.0); err != nil {
		return 0
	}
	return 1
}

// awsAccountFromRoleARN extracts the AWS account ID from a role ARN.
// Ported verbatim from pkg/linker/workload_identity.go.
func awsAccountFromRoleARN(arn string) string {
	const prefix = "arn:aws:iam::"
	if !strings.HasPrefix(arn, prefix) {
		return ""
	}
	rest := arn[len(prefix):]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return ""
	}
	return rest[:colon]
}

// linkGCPWorkloadIdentity emits a WORKLOAD_IDENTITY edge for a GKE-annotated
// SA. Target node ID format matches the GCP IAM collector's convention.
func linkGCPWorkloadIdentity(ctx context.Context, gc GraphCaller, opts LinkOptions, sa *knowledgev1.Node) int {
	email := kgtypes.Value(sa, "gcp_service_account")
	if email == "" {
		return 0
	}
	project := gcpProjectFromSAEmail(email)
	if project == "" {
		return 0
	}
	targetID := "projects/" + project + "/serviceAccounts/" + email
	evidence := fmt.Sprintf("K8s SA %s → %s via gcp-wi", sa.Id, targetID)
	if err := emitLink(ctx, gc, opts, sa.Id, targetID, "WORKLOAD_IDENTITY", "tier1-gcp-wi", evidence, 1.0); err != nil {
		return 0
	}
	return 1
}

// linkAzureWorkloadIdentity scans every cloud graph for an Azure managed
// identity whose clientId matches the SA annotation; on hit, emits the
// edge.
func linkAzureWorkloadIdentity(ctx context.Context, gc GraphCaller, opts LinkOptions, allClouds []string, sa *knowledgev1.Node) int {
	clientID := kgtypes.Value(sa, "azure_client_id")
	if clientID == "" {
		return 0
	}
	targetID := findAzureIdentityByClientID(ctx, gc, allClouds, clientID)
	if targetID == "" {
		return 0
	}
	evidence := fmt.Sprintf("K8s SA %s → %s via azure-wi", sa.Id, targetID)
	if err := emitLink(ctx, gc, opts, sa.Id, targetID, "WORKLOAD_IDENTITY", "tier1-azure-wi", evidence, 0.9); err != nil {
		return 0
	}
	return 1
}

// findAzureIdentityByClientID scans every cloud graph for a managed-identity
// node carrying clientId == clientID. Returns the target node ID or "" on
// miss. Skips graphs that fail to load.
func findAzureIdentityByClientID(ctx context.Context, gc GraphCaller, allClouds []string, clientID string) string {
	for _, name := range allClouds {
		nodes, err := queryCloudResources(ctx, gc, name)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if kgtypes.Value(n, "clientId") == clientID {
				return n.Id
			}
		}
	}
	return ""
}

// gcpProjectFromSAEmail extracts the GCP project ID from a service account
// email. Ported verbatim from pkg/linker/workload_identity.go.
func gcpProjectFromSAEmail(email string) string {
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
