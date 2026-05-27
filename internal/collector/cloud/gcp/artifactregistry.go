// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	artifactregistrypb "cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	iamv1 "cloud.google.com/go/iam/apiv1"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// artifactRegistrySubCollector collects Artifact Registry repositories.
type artifactRegistrySubCollector struct {
	client    *artifactregistry.Client
	iamClient *iamv1.IamPolicyClient
	projectID string
}

func newArtifactRegistrySubCollector(
	client *artifactregistry.Client,
	iamClient *iamv1.IamPolicyClient,
	projectID string,
) *artifactRegistrySubCollector {
	return &artifactRegistrySubCollector{
		client:    client,
		iamClient: iamClient,
		projectID: projectID,
	}
}

func (c *artifactRegistrySubCollector) Name() string { return "gcp-artifact-registry" }

func (c *artifactRegistrySubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult
	seenProxies := map[string]bool{}

	it := c.client.ListRepositories(ctx, &artifactregistrypb.ListRepositoriesRequest{
		Parent: "projects/" + c.projectID + "/locations/-",
	})

	for {
		repo, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := repo.GetName()
		if name == "" {
			continue
		}

		content, err := json.Marshal(repo)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:artifactregistry:repository",
			Region:       extractLocationFromName(name),
			Content:      content,
			Metadata: map[string]string{
				"format":    repo.GetFormat().String(),
				"mode":      repo.GetMode().String(),
				"sizeBytes": strconv.FormatInt(repo.GetSizeBytes(), 10),
			},
		}
		result.Resources = append(result.Resources, spec)

		// ENCRYPTS_WITH edge when CMEK is configured.
		if key := repo.GetKmsKeyName(); key != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     key,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}

		// Upstream / proxy edges based on mode.
		edges, proxies := arRepoModeEdges(name, repo, seenProxies)
		result.Edges = append(result.Edges, edges...)
		result.Resources = append(result.Resources, proxies...)

		// Best-effort IAM policy (separate RPC).
		if c.iamClient != nil {
			if policy, perr := c.iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: name,
			}); perr == nil && policy != nil {
				result.Edges = append(result.Edges,
					arIAMGrantsEdges(name, policy)...)
			} else if perr != nil {
				slog.Debug("gcp-artifact-registry: iam policy unavailable",
					"repo", name, "error", perr)
			}
		}
	}

	return result, nil
}

// arRepoModeEdges emits EdgeProxiesFrom edges based on the repository mode.
// Virtual repos proxy from each upstream policy repository; remote repos
// proxy from the public registry endpoint.
func arRepoModeEdges(
	name string,
	repo *artifactregistrypb.Repository,
	seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec

	switch repo.GetMode() {
	case artifactregistrypb.Repository_VIRTUAL_REPOSITORY:
		vcfg := repo.GetVirtualRepositoryConfig()
		if vcfg == nil {
			break
		}
		for _, up := range vcfg.GetUpstreamPolicies() {
			target := up.GetRepository()
			if target == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     target,
				Relationship: kgtypes.EdgeProxiesFrom,
				Metadata:     map[string]string{"upstream_id": up.GetId()},
			})
		}

	case artifactregistrypb.Repository_REMOTE_REPOSITORY:
		rcfg := repo.GetRemoteRepositoryConfig()
		if rcfg == nil {
			break
		}
		// Dispatch on the typed RemoteSource oneof; the Description field
		// is user-supplied free-form text (used to alias Docker Hub etc.)
		// and would collide / fragment proxy nodes if used as the ID.
		targetID, displayName, remoteType := remoteRepoUpstream(rcfg)
		if targetID == "" {
			break
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     name,
			TargetID:     targetID,
			Relationship: kgtypes.EdgeProxiesFrom,
			Metadata: map[string]string{
				"remote_type": remoteType,
				"description": rcfg.GetDescription(),
			},
		})
		if !seenProxies[targetID] {
			seenProxies[targetID] = true
			proxies = append(proxies, cloud.ResourceSpec{
				ID:           targetID,
				Name:         displayName,
				ResourceType: "gcp:ar:remote",
				Metadata: map[string]string{
					"collected":        "false",
					"collected_reason": "public registry",
					"remote_type":      remoteType,
				},
			})
		}
	}

	return edges, proxies
}

// arIAMGrantsEdges turns an iampb.Policy into EdgeGrants edges from the
// repository to each IAM member.
func arIAMGrantsEdges(repoName string, policy *iampb.Policy) []cloud.EdgeSpec {
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
				SourceID:     repoName,
				TargetID:     member,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role},
			})
		}
	}
	return edges
}
