// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ecrAPI is the subset of the ECR client surface used by ecrCollector.
type ecrAPI interface {
	DescribeRepositories(ctx context.Context, params *ecr.DescribeRepositoriesInput, optFns ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	GetRepositoryPolicy(ctx context.Context, params *ecr.GetRepositoryPolicyInput, optFns ...func(*ecr.Options)) (*ecr.GetRepositoryPolicyOutput, error)
}

type ecrCollector struct {
	client    ecrAPI
	region    string
	accountID string
}

func newECRCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &ecrCollector{
		client:    ecr.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *ecrCollector) Name() string { return "ecr" }

func (c *ecrCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	var nextToken *string
	for {
		page, err := c.client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("ecr: describe repositories: %w", err)
		}

		for _, repo := range page.Repositories {
			content, merr := json.Marshal(repo)
			if merr != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("ecr: marshal: %w", merr)
			}

			repoARN := awssdk.ToString(repo.RepositoryArn)
			repoName := awssdk.ToString(repo.RepositoryName)

			resources = append(resources, cloud.ResourceSpec{
				ID:           repoARN,
				Name:         repoName,
				ResourceType: "ecr-repository",
				Region:       c.region,
				Content:      content,
				Metadata:     ecrRepositoryMetadata(repo),
			})

			edges = append(edges, encryptionEdge(repoARN, c.region, c.accountID, repo)...)
			edges = append(edges, c.repoPolicyEdges(ctx, repoARN, repoName)...)
		}

		nextToken = page.NextToken
		if nextToken == nil {
			break
		}
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// encryptionEdge emits EdgeEncryptsWith from the repo ARN to the KMS key
// ARN when the repository is configured with KMS encryption. The KmsKey
// field on EncryptionConfiguration may be a key ID, alias, or full ARN per
// the ECR API; route through resolveKMSKeyARN so the edge target matches
// the canonical KMS node ID emitted by the KMS subcollector.
func encryptionEdge(repoARN, region, accountID string, repo ecrtypes.Repository) []cloud.EdgeSpec {
	enc := repo.EncryptionConfiguration
	if enc == nil {
		return nil
	}
	if enc.EncryptionType != ecrtypes.EncryptionTypeKms {
		return nil
	}
	kmsKey := awssdk.ToString(enc.KmsKey)
	if kmsKey == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     repoARN,
		TargetID:     resolveKMSKeyARN(kmsKey, region, accountID),
		Relationship: kgtypes.EdgeEncryptsWith,
	}}
}

// repoPolicyEdges calls GetRepositoryPolicy and parses the IAM policy JSON
// to emit EdgeGrants per principal. Missing policies are silently skipped.
func (c *ecrCollector) repoPolicyEdges(ctx context.Context, repoARN, repoName string) []cloud.EdgeSpec {
	out, err := c.client.GetRepositoryPolicy(ctx, &ecr.GetRepositoryPolicyInput{
		RepositoryName: awssdk.String(repoName),
	})
	if err != nil {
		// RepositoryPolicyNotFoundException is the standard error for repos
		// without a policy. Fail-open on all errors.
		slog.Debug("ecr: get repository policy", "repo", repoName, "error", err)
		return nil
	}
	if out == nil || out.PolicyText == nil {
		return nil
	}

	// Reuse the same bucket policy parsing types — ECR policies use the
	// identical IAM policy JSON format.
	edges, perr := parseECRRepoPolicy(repoARN, awssdk.ToString(out.PolicyText))
	if perr != nil {
		slog.Warn("ecr: parse repository policy", "repo", repoName, "error", perr)
		return nil
	}
	return edges
}

// parseECRRepoPolicy parses an ECR repository policy and returns
// EdgeGrants edges. Reuses the IAM policy types from s3_policy.go.
func parseECRRepoPolicy(repoARN, policyJSON string) ([]cloud.EdgeSpec, error) {
	if policyJSON == "" {
		return nil, nil
	}
	var doc bucketPolicyDoc
	if err := json.Unmarshal([]byte(policyJSON), &doc); err != nil {
		return nil, err
	}

	var edges []cloud.EdgeSpec
	for _, stmt := range doc.Statement {
		if stmt.Effect != "Allow" {
			continue
		}
		principals := extractPrincipals(stmt.Principal)
		if len(principals) == 0 {
			continue
		}

		var metadata map[string]string
		if len(stmt.Condition) > 0 {
			raw, err := json.Marshal(stmt.Condition)
			if err == nil {
				metadata = map[string]string{"condition": string(raw)}
			}
		}
		for _, p := range principals {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     repoARN,
				TargetID:     p,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     metadata,
			})
		}
	}
	return edges, nil
}

// ecrRepositoryMetadata extracts discriminating fields from an ECR repository.
func ecrRepositoryMetadata(r ecrtypes.Repository) map[string]string {
	m := make(map[string]string, 2)
	if im := string(r.ImageTagMutability); im != "" {
		m["image_tag_mutability"] = im
	}
	if r.EncryptionConfiguration != nil {
		if t := string(r.EncryptionConfiguration.EncryptionType); t != "" {
			m["encryption_type"] = t
		}
	}
	return m
}
