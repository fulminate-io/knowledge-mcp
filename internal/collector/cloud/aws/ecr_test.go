// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type fakeEcrAPI struct {
	repos    []ecrtypes.Repository
	policies map[string]string // keyed by repo name
}

func (f *fakeEcrAPI) DescribeRepositories(_ context.Context, _ *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	return &ecr.DescribeRepositoriesOutput{Repositories: f.repos}, nil
}

func (f *fakeEcrAPI) GetRepositoryPolicy(_ context.Context, in *ecr.GetRepositoryPolicyInput, _ ...func(*ecr.Options)) (*ecr.GetRepositoryPolicyOutput, error) {
	name := awssdk.ToString(in.RepositoryName)
	if doc, ok := f.policies[name]; ok {
		return &ecr.GetRepositoryPolicyOutput{PolicyText: awssdk.String(doc)}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "RepositoryPolicyNotFoundException", Message: "no policy"}
}

// TestECRCollector_EncryptionKMSEdge verifies EdgeEncryptsWith is emitted
// when the repo has KMS encryption configured.
func TestECRCollector_EncryptionKMSEdge(t *testing.T) {
	repoARN := "arn:aws:ecr:us-east-1:111111111111:repository/my-app"
	kmsKey := "arn:aws:kms:us-east-1:111111111111:key/ecr-key-id"
	fake := &fakeEcrAPI{
		repos: []ecrtypes.Repository{{
			RepositoryArn:  awssdk.String(repoARN),
			RepositoryName: awssdk.String("my-app"),
			EncryptionConfiguration: &ecrtypes.EncryptionConfiguration{
				EncryptionType: ecrtypes.EncryptionTypeKms,
				KmsKey:         awssdk.String(kmsKey),
			},
		}},
		policies: map[string]string{},
	}

	c := &ecrCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeEncryptsWith {
			assert.Equal(t, repoARN, e.SourceID)
			assert.Equal(t, kmsKey, e.TargetID)
			found = true
		}
	}
	assert.True(t, found, "expected EdgeEncryptsWith for KMS-encrypted ECR repo")
}

// TestECRCollector_EncryptionKMS_BareKeyID canonicalizes a bare key ID
// to the full KMS ARN via resolveKMSKeyARN.
func TestECRCollector_EncryptionKMS_BareKeyID(t *testing.T) {
	repoARN := "arn:aws:ecr:us-east-1:111111111111:repository/my-app"
	fake := &fakeEcrAPI{
		repos: []ecrtypes.Repository{{
			RepositoryArn:  awssdk.String(repoARN),
			RepositoryName: awssdk.String("my-app"),
			EncryptionConfiguration: &ecrtypes.EncryptionConfiguration{
				EncryptionType: ecrtypes.EncryptionTypeKms,
				KmsKey:         awssdk.String("1234abcd-12ab-34cd-56ef-1234567890ab"),
			},
		}},
		policies: map[string]string{},
	}
	c := &ecrCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var target string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeEncryptsWith {
			target = e.TargetID
		}
	}
	assert.Equal(t,
		"arn:aws:kms:us-east-1:111111111111:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		target,
		"bare key ID must be canonicalized to full KMS ARN")
}

// TestECRCollector_AES256_NoEncryptionEdge verifies that AES256 (default)
// encryption does not produce an EdgeEncryptsWith edge.
func TestECRCollector_AES256_NoEncryptionEdge(t *testing.T) {
	fake := &fakeEcrAPI{
		repos: []ecrtypes.Repository{{
			RepositoryArn:  awssdk.String("arn:aws:ecr:us-east-1:111:repository/plain"),
			RepositoryName: awssdk.String("plain"),
			EncryptionConfiguration: &ecrtypes.EncryptionConfiguration{
				EncryptionType: ecrtypes.EncryptionTypeAes256,
			},
		}},
	}

	c := &ecrCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	for _, e := range result.Edges {
		assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
	}
}

// TestECRCollector_RepoPolicyGrant verifies EdgeGrants is emitted from
// the repo ARN to the principal in the repository policy.
func TestECRCollector_RepoPolicyGrant(t *testing.T) {
	repoARN := "arn:aws:ecr:us-east-1:111111111111:repository/shared"
	roleARN := "arn:aws:iam::222222222222:root"
	fake := &fakeEcrAPI{
		repos: []ecrtypes.Repository{{
			RepositoryArn:  awssdk.String(repoARN),
			RepositoryName: awssdk.String("shared"),
		}},
		policies: map[string]string{
			"shared": `{
				"Version": "2012-10-17",
				"Statement": [{
					"Effect": "Allow",
					"Principal": {"AWS": "` + roleARN + `"},
					"Action": ["ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"],
					"Resource": "*"
				}]
			}`,
		},
	}

	c := &ecrCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var grants int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeGrants {
			assert.Equal(t, repoARN, e.SourceID)
			assert.Equal(t, roleARN, e.TargetID)
			grants++
		}
	}
	assert.Equal(t, 1, grants)
}

// TestECRCollector_NoPolicyNoEncryption verifies clean path when repo
// has neither encryption nor a policy.
func TestECRCollector_NoPolicyNoEncryption(t *testing.T) {
	fake := &fakeEcrAPI{
		repos: []ecrtypes.Repository{{
			RepositoryArn:  awssdk.String("arn:aws:ecr:us-east-1:111:repository/bare"),
			RepositoryName: awssdk.String("bare"),
		}},
	}

	c := &ecrCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges)
}
