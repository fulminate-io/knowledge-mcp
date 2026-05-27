// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeS3API is a minimal in-memory S3 client for unit tests. Each map is
// keyed by bucket name.
type fakeS3API struct {
	buckets       []s3types.Bucket
	pab           map[string]*s3types.PublicAccessBlockConfiguration
	pabError      map[string]error // per-bucket error override
	policyStatus  map[string]*s3types.PolicyStatus
	policyError   map[string]error
	acl           map[string][]s3types.Grant
	notifications map[string]*s3.GetBucketNotificationConfigurationOutput
	encryption    map[string]*s3types.ServerSideEncryptionConfiguration
	policyDoc     map[string]string // JSON policy document per bucket
	replication   map[string]*s3types.ReplicationConfiguration
}

func (f *fakeS3API) ListBuckets(_ context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	return &s3.ListBucketsOutput{Buckets: f.buckets}, nil
}

func (f *fakeS3API) GetPublicAccessBlock(_ context.Context, in *s3.GetPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if err, ok := f.pabError[name]; ok {
		return nil, err
	}
	if cfg, ok := f.pab[name]; ok {
		return &s3.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: cfg}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "NoSuchPublicAccessBlockConfiguration", Message: "not found"}
}

func (f *fakeS3API) GetBucketPolicyStatus(_ context.Context, in *s3.GetBucketPolicyStatusInput, _ ...func(*s3.Options)) (*s3.GetBucketPolicyStatusOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if err, ok := f.policyError[name]; ok {
		return nil, err
	}
	if status, ok := f.policyStatus[name]; ok {
		return &s3.GetBucketPolicyStatusOutput{PolicyStatus: status}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "NoSuchBucketPolicy", Message: "no policy"}
}

func (f *fakeS3API) GetBucketAcl(_ context.Context, in *s3.GetBucketAclInput, _ ...func(*s3.Options)) (*s3.GetBucketAclOutput, error) {
	name := awssdk.ToString(in.Bucket)
	return &s3.GetBucketAclOutput{Grants: f.acl[name]}, nil
}

func (f *fakeS3API) GetBucketNotificationConfiguration(_ context.Context, in *s3.GetBucketNotificationConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketNotificationConfigurationOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if out, ok := f.notifications[name]; ok {
		return out, nil
	}
	return &s3.GetBucketNotificationConfigurationOutput{}, nil
}

func (f *fakeS3API) GetBucketEncryption(_ context.Context, in *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if cfg, ok := f.encryption[name]; ok {
		return &s3.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: cfg}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "ServerSideEncryptionConfigurationNotFoundError", Message: "no SSE"}
}

func (f *fakeS3API) GetBucketPolicy(_ context.Context, in *s3.GetBucketPolicyInput, _ ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if doc, ok := f.policyDoc[name]; ok {
		return &s3.GetBucketPolicyOutput{Policy: awssdk.String(doc)}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "NoSuchBucketPolicy", Message: "no policy"}
}

func (f *fakeS3API) GetBucketReplication(_ context.Context, in *s3.GetBucketReplicationInput, _ ...func(*s3.Options)) (*s3.GetBucketReplicationOutput, error) {
	name := awssdk.ToString(in.Bucket)
	if cfg, ok := f.replication[name]; ok {
		return &s3.GetBucketReplicationOutput{ReplicationConfiguration: cfg}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "ReplicationConfigurationNotFoundError", Message: "no replication"}
}

func runS3Collector(t *testing.T, fake *fakeS3API) s3BucketContent {
	t.Helper()
	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var env s3BucketContent
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &env))
	return env
}

func TestS3Collector_AllBlocked(t *testing.T) {
	name := "locked-down"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		pab: map[string]*s3types.PublicAccessBlockConfiguration{
			name: {
				BlockPublicAcls:       awssdk.Bool(true),
				BlockPublicPolicy:     awssdk.Bool(true),
				IgnorePublicAcls:      awssdk.Bool(true),
				RestrictPublicBuckets: awssdk.Bool(true),
			},
		},
		policyStatus: map[string]*s3types.PolicyStatus{
			name: {IsPublic: awssdk.Bool(false)},
		},
		acl: map[string][]s3types.Grant{
			name: {{
				Grantee:    &s3types.Grantee{Type: s3types.TypeCanonicalUser, ID: awssdk.String("owner-id")},
				Permission: s3types.PermissionFullControl,
			}},
		},
	}
	env := runS3Collector(t, fake)

	require.NotNil(t, env.PublicAccessBlock)
	assert.True(t, env.PublicAccessBlock.BlockPublicAcls)
	assert.True(t, env.PublicAccessBlock.BlockPublicPolicy)
	assert.True(t, env.PublicAccessBlock.IgnorePublicAcls)
	assert.True(t, env.PublicAccessBlock.RestrictPublicBuckets)
	assert.False(t, env.PublicAccessBlockMissing)
	require.NotNil(t, env.BucketPolicyStatus)
	assert.False(t, env.BucketPolicyStatus.IsPublic)
	assert.Empty(t, env.ACLPublicGrants)
}

func TestS3Collector_NoBlockPublicPolicy(t *testing.T) {
	name := "policy-public"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		// No PAB -> NoSuchPublicAccessBlockConfiguration path.
		policyStatus: map[string]*s3types.PolicyStatus{
			name: {IsPublic: awssdk.Bool(true)},
		},
	}
	env := runS3Collector(t, fake)

	assert.Nil(t, env.PublicAccessBlock)
	assert.True(t, env.PublicAccessBlockMissing, "PAB missing marker should be set")
	require.NotNil(t, env.BucketPolicyStatus)
	assert.True(t, env.BucketPolicyStatus.IsPublic)
	assert.Empty(t, env.ACLPublicGrants)
}

func TestS3Collector_NoBlockPublicACL(t *testing.T) {
	name := "acl-public"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		acl: map[string][]s3types.Grant{
			name: {{
				Grantee: &s3types.Grantee{
					Type: s3types.TypeGroup,
					URI:  awssdk.String(s3AllUsersGroupURI),
				},
				Permission: s3types.PermissionRead,
			}},
		},
	}
	env := runS3Collector(t, fake)

	assert.True(t, env.PublicAccessBlockMissing)
	assert.Nil(t, env.BucketPolicyStatus) // NoSuchBucketPolicy path
	require.Len(t, env.ACLPublicGrants, 1)
	assert.Equal(t, s3AllUsersGroupURI, env.ACLPublicGrants[0].GroupURI)
	assert.Equal(t, "READ", env.ACLPublicGrants[0].Permission)
}

func TestS3Collector_AllPublic(t *testing.T) {
	name := "fully-public"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		pab: map[string]*s3types.PublicAccessBlockConfiguration{
			name: {
				BlockPublicAcls:       awssdk.Bool(false),
				BlockPublicPolicy:     awssdk.Bool(false),
				IgnorePublicAcls:      awssdk.Bool(false),
				RestrictPublicBuckets: awssdk.Bool(false),
			},
		},
		policyStatus: map[string]*s3types.PolicyStatus{
			name: {IsPublic: awssdk.Bool(true)},
		},
		acl: map[string][]s3types.Grant{
			name: {
				{
					Grantee: &s3types.Grantee{
						Type: s3types.TypeGroup,
						URI:  awssdk.String(s3AllUsersGroupURI),
					},
					Permission: s3types.PermissionRead,
				},
				{
					Grantee: &s3types.Grantee{
						Type: s3types.TypeGroup,
						URI:  awssdk.String(s3AuthenticatedUsersGroupURI),
					},
					Permission: s3types.PermissionWrite,
				},
			},
		},
	}
	env := runS3Collector(t, fake)

	require.NotNil(t, env.PublicAccessBlock)
	assert.False(t, env.PublicAccessBlock.BlockPublicAcls)
	assert.False(t, env.PublicAccessBlock.BlockPublicPolicy)
	assert.False(t, env.PublicAccessBlockMissing)
	require.NotNil(t, env.BucketPolicyStatus)
	assert.True(t, env.BucketPolicyStatus.IsPublic)
	require.Len(t, env.ACLPublicGrants, 2)
}

func TestS3Collector_NotificationEdges(t *testing.T) {
	name := "evented"
	lambdaARN := "arn:aws:lambda:us-east-1:111111111111:function:processor"
	sqsARN := "arn:aws:sqs:us-east-1:111111111111:my-queue"
	snsARN := "arn:aws:sns:us-east-1:111111111111:my-topic"

	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		notifications: map[string]*s3.GetBucketNotificationConfigurationOutput{
			name: {
				LambdaFunctionConfigurations: []s3types.LambdaFunctionConfiguration{
					{LambdaFunctionArn: awssdk.String(lambdaARN)},
				},
				QueueConfigurations: []s3types.QueueConfiguration{
					{QueueArn: awssdk.String(sqsARN)},
				},
				TopicConfigurations: []s3types.TopicConfiguration{
					{TopicArn: awssdk.String(snsARN)},
				},
			},
		},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	require.Len(t, result.Edges, 3)

	bucketARN := "arn:aws:s3:::" + name
	assert.Equal(t, bucketARN, result.Edges[0].SourceID)
	assert.Equal(t, lambdaARN, result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTriggers, result.Edges[0].Relationship)

	assert.Equal(t, sqsARN, result.Edges[1].TargetID)
	assert.Equal(t, snsARN, result.Edges[2].TargetID)
}

// TestS3Collector_EncryptionKMSEdge exercises GetBucketEncryption and
// verifies an EdgeEncryptsWith edge is emitted from the bucket ARN to the
// configured KMS master key ARN. The fake returns a single SSE-KMS default
// rule; SSE-S3 and unset cases are covered by sibling tests below.
func TestS3Collector_EncryptionKMSEdge(t *testing.T) {
	name := "kms-encrypted"
	keyARN := "arn:aws:kms:us-east-1:111111111111:key/1234-5678"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		encryption: map[string]*s3types.ServerSideEncryptionConfiguration{
			name: {
				Rules: []s3types.ServerSideEncryptionRule{{
					ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
						SSEAlgorithm:   s3types.ServerSideEncryptionAwsKms,
						KMSMasterKeyID: awssdk.String(keyARN),
					},
				}},
			},
		},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	bucketARN := "arn:aws:s3:::" + name
	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeEncryptsWith {
			assert.Equal(t, bucketARN, e.SourceID)
			assert.Equal(t, keyARN, e.TargetID)
			found = true
		}
	}
	assert.True(t, found, "expected EdgeEncryptsWith edge for SSE-KMS bucket")
}

// TestS3Collector_EncryptionSSES3_NoEdge verifies an SSE-S3 (AES256) default
// rule does NOT emit an EdgeEncryptsWith edge — only SSE-KMS creates a
// traversable dependency on a KMS key.
func TestS3Collector_EncryptionSSES3_NoEdge(t *testing.T) {
	name := "sse-s3"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		encryption: map[string]*s3types.ServerSideEncryptionConfiguration{
			name: {
				Rules: []s3types.ServerSideEncryptionRule{{
					ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
						SSEAlgorithm: s3types.ServerSideEncryptionAes256,
					},
				}},
			},
		},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	for _, e := range result.Edges {
		assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
	}
}

// TestS3Collector_PolicyUnconditionalGrant verifies a bucket policy with
// a single Allow statement targeting a specific IAM role emits exactly one
// EdgeGrants edge with no condition metadata.
func TestS3Collector_PolicyUnconditionalGrant(t *testing.T) {
	name := "unconditional"
	roleARN := "arn:aws:iam::111111111111:role/reader"
	policy := `{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": {"AWS": "` + roleARN + `"},
			"Action": "s3:GetObject",
			"Resource": "arn:aws:s3:::unconditional/*"
		}]
	}`
	fake := &fakeS3API{
		buckets:   []s3types.Bucket{{Name: awssdk.String(name)}},
		policyDoc: map[string]string{name: policy},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	bucketARN := "arn:aws:s3:::" + name
	var grants []cloud.EdgeSpec
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeGrants {
			grants = append(grants, e)
		}
	}
	require.Len(t, grants, 1)
	assert.Equal(t, bucketARN, grants[0].SourceID)
	assert.Equal(t, roleARN, grants[0].TargetID)
	_, hasCond := grants[0].Metadata["condition"]
	assert.False(t, hasCond, "unconditional grant must not carry condition metadata")
}

// TestS3Collector_PolicyConditionalGrant verifies a bucket policy with a
// Condition block on an Allow statement emits an EdgeGrants edge whose
// Metadata["condition"] round-trips the Condition JSON intact.
func TestS3Collector_PolicyConditionalGrant(t *testing.T) {
	name := "conditional"
	policy := `{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": "*",
			"Action": "s3:GetObject",
			"Resource": "arn:aws:s3:::conditional/*",
			"Condition": {
				"StringEquals": {"aws:SourceArn": "arn:aws:cloudfront::111111111111:distribution/EDFDVBD6"}
			}
		}]
	}`
	fake := &fakeS3API{
		buckets:   []s3types.Bucket{{Name: awssdk.String(name)}},
		policyDoc: map[string]string{name: policy},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var grant *cloud.EdgeSpec
	for i := range result.Edges {
		if result.Edges[i].Relationship == kgtypes.EdgeGrants {
			grant = &result.Edges[i]
			break
		}
	}
	require.NotNil(t, grant, "expected EdgeGrants edge on conditional policy")
	assert.Equal(t, "*", grant.TargetID)
	condRaw, ok := grant.Metadata["condition"]
	require.True(t, ok, "conditional grant must carry Metadata[\"condition\"]")
	assert.Contains(t, condRaw, "StringEquals")
	assert.Contains(t, condRaw, "cloudfront")
}

// TestS3Collector_PolicyAWSPrincipalList verifies a single Allow statement
// whose Principal.AWS is a JSON array produces one EdgeGrants edge per
// array element.
func TestS3Collector_PolicyAWSPrincipalList(t *testing.T) {
	name := "multi-principal"
	policy := `{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": {"AWS": [
				"arn:aws:iam::111111111111:role/a",
				"arn:aws:iam::111111111111:role/b"
			]},
			"Action": "s3:GetObject",
			"Resource": "*"
		}]
	}`
	fake := &fakeS3API{
		buckets:   []s3types.Bucket{{Name: awssdk.String(name)}},
		policyDoc: map[string]string{name: policy},
	}

	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var targets []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeGrants {
			targets = append(targets, e.TargetID)
		}
	}
	assert.ElementsMatch(t, []string{
		"arn:aws:iam::111111111111:role/a",
		"arn:aws:iam::111111111111:role/b",
	}, targets)
}
