// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	iamv1 "cloud.google.com/go/iam/apiv1"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	run "cloud.google.com/go/run/apiv2"
	runpb "cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// cloudRunSubCollector collects Cloud Run services.
type cloudRunSubCollector struct {
	client    *run.ServicesClient
	iamClient *iamv1.IamPolicyClient
	projectID string
}

func newCloudRunSubCollector(
	client *run.ServicesClient,
	iamClient *iamv1.IamPolicyClient,
	projectID string,
) *cloudRunSubCollector {
	return &cloudRunSubCollector{
		client:    client,
		iamClient: iamClient,
		projectID: projectID,
	}
}

func (c *cloudRunSubCollector) Name() string { return "gcp-cloud-run" }

func (c *cloudRunSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// List Cloud Run services across all regions using location "-".
	it := c.client.ListServices(ctx, &runpb.ListServicesRequest{
		Parent: "projects/" + c.projectID + "/locations/-",
	})

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		svc, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := svc.GetName()
		if name == "" {
			continue
		}

		content, err := json.Marshal(svc)
		if err != nil {
			continue
		}

		spec := cloudRunResourceSpec(name, svc, content)
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, cloudRunEdges(c.projectID, name, svc.GetTemplate())...)

		// Best-effort IAM policy (separate RPC). Captures roles/run.invoker
		// grants — the canonical signal for "is this service publicly invocable".
		if c.iamClient != nil {
			if policy, perr := c.iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: name,
			}); perr == nil && policy != nil {
				result.Edges = append(result.Edges, cloudRunIAMGrantsEdges(name, policy)...)
			} else if perr != nil {
				slog.Debug("gcp-cloud-run: iam policy unavailable",
					"service", name, "error", perr)
			}
		}
	}

	return result, nil
}

// cloudRunResourceSpec builds a ResourceSpec for a Cloud Run service.
func cloudRunResourceSpec(name string, svc *runpb.Service, content []byte) cloud.ResourceSpec {
	return cloud.ResourceSpec{
		ID:           name,
		Name:         extractLast(name),
		ResourceType: "gcp:run:service",
		Region:       extractLocationFromName(name),
		Content:      content,
		Metadata: map[string]string{
			"uri":     svc.GetUri(),
			"ingress": svc.GetIngress().String(),
		},
	}
}

// cloudRunEdges extracts edges from a Cloud Run service template:
// service account, secret env vars, and secret volume mounts.
func cloudRunEdges(projectID, name string, tmpl *runpb.RevisionTemplate) []cloud.EdgeSpec {
	if tmpl == nil {
		return nil
	}

	var edges []cloud.EdgeSpec

	// Service account edge.
	if sa := tmpl.GetServiceAccount(); sa != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     name,
			TargetID:     saResourceName(projectID, sa),
			Relationship: kgtypes.EdgeUsesSA,
		})
	}

	// Secret references from environment variables.
	edges = append(edges, cloudRunContainerSecretEdges(projectID, name, tmpl.GetContainers())...)

	// Secret references from volume mounts.
	for _, vol := range tmpl.GetVolumes() {
		if sv := vol.GetSecret(); sv != nil {
			if secret := sv.GetSecret(); secret != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     name,
					TargetID:     secretResourceName(projectID, secret),
					Relationship: kgtypes.EdgeMountsSecret,
				})
			}
		}
	}

	// VPC connector edge (Serverless VPC Access).
	if vpc := tmpl.GetVpcAccess(); vpc != nil {
		if connector := vpc.GetConnector(); connector != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     connector,
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}
	}

	// ENCRYPTS_WITH edge when CMEK is configured on the revision template.
	if key := tmpl.GetEncryptionKey(); key != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     name,
			TargetID:     key,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	return edges
}

// cloudRunContainerSecretEdges extracts secret edges from container env vars.
func cloudRunContainerSecretEdges(projectID, name string, containers []*runpb.Container) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, container := range containers {
		for _, env := range container.GetEnv() {
			if src := env.GetValueSource(); src != nil {
				if ref := src.GetSecretKeyRef(); ref != nil {
					if secret := ref.GetSecret(); secret != "" {
						edges = append(edges, cloud.EdgeSpec{
							SourceID:     name,
							TargetID:     secretResourceName(projectID, secret),
							Relationship: kgtypes.EdgeMountsSecret,
						})
					}
				}
			}
		}
	}
	return edges
}

// cloudRunIAMGrantsEdges turns an iampb.Policy into EdgeGrants edges from
// the Cloud Run service to each IAM member. allUsers / allAuthenticatedUsers
// surface here as ordinary member sentinels.
func cloudRunIAMGrantsEdges(serviceName string, policy *iampb.Policy) []cloud.EdgeSpec {
	if policy == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, binding := range policy.GetBindings() {
		role := binding.GetRole()
		members := make([]string, len(binding.GetMembers()))
		copy(members, binding.GetMembers())
		sort.Strings(members)
		for _, member := range members {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     serviceName,
				TargetID:     member,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role},
			})
		}
	}
	return edges
}

// extractLocationFromName extracts the location from a resource name like
// "projects/P/locations/L/services/S" -> "L"
func extractLocationFromName(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		if p == "locations" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// secretResourceName builds the full Secret Manager resource name.
func secretResourceName(projectID, secretName string) string {
	// If it's already a full resource name, return as-is.
	if strings.HasPrefix(secretName, "projects/") {
		return secretName
	}
	return "projects/" + projectID + "/secrets/" + secretName
}
