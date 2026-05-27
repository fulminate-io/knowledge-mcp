// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeKmsAPI is a minimal in-memory KMS client for unit tests.
type fakeKmsAPI struct {
	keys      []kmstypes.KeyListEntry
	metadata  map[string]*kmstypes.KeyMetadata // keyed by key ID
	policies  map[string]string                // keyed by key ID
	aliases   map[string][]kmstypes.AliasListEntry
	policyErr map[string]error
}

func (f *fakeKmsAPI) ListKeys(_ context.Context, in *kms.ListKeysInput, _ ...func(*kms.Options)) (*kms.ListKeysOutput, error) {
	return &kms.ListKeysOutput{Keys: f.keys, Truncated: false}, nil
}

func (f *fakeKmsAPI) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	id := awssdk.ToString(in.KeyId)
	if meta, ok := f.metadata[id]; ok {
		return &kms.DescribeKeyOutput{KeyMetadata: meta}, nil
	}
	return &kms.DescribeKeyOutput{KeyMetadata: &kmstypes.KeyMetadata{
		KeyId: in.KeyId,
		Arn:   awssdk.String("arn:aws:kms:us-east-1:111111111111:key/" + id),
	}}, nil
}

func (f *fakeKmsAPI) GetKeyPolicy(_ context.Context, in *kms.GetKeyPolicyInput, _ ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error) {
	id := awssdk.ToString(in.KeyId)
	if err, ok := f.policyErr[id]; ok {
		return nil, err
	}
	if doc, ok := f.policies[id]; ok {
		return &kms.GetKeyPolicyOutput{Policy: awssdk.String(doc)}, nil
	}
	return &kms.GetKeyPolicyOutput{}, nil
}

func (f *fakeKmsAPI) ListAliases(_ context.Context, in *kms.ListAliasesInput, _ ...func(*kms.Options)) (*kms.ListAliasesOutput, error) {
	id := awssdk.ToString(in.KeyId)
	return &kms.ListAliasesOutput{Aliases: f.aliases[id]}, nil
}

const testKMSKeyID = "test-key-id-1"

func testKMSKeyARN() string {
	return "arn:aws:kms:us-east-1:111111111111:key/" + testKMSKeyID
}

func baseFakeKMS() *fakeKmsAPI {
	return &fakeKmsAPI{
		keys: []kmstypes.KeyListEntry{{KeyId: awssdk.String(testKMSKeyID)}},
		metadata: map[string]*kmstypes.KeyMetadata{
			testKMSKeyID: {
				KeyId:      awssdk.String(testKMSKeyID),
				Arn:        awssdk.String(testKMSKeyARN()),
				KeyManager: kmstypes.KeyManagerTypeCustomer,
			},
		},
		policies: map[string]string{},
		aliases:  map[string][]kmstypes.AliasListEntry{},
	}
}

// TestKMSCollector_KeyPolicyGrants verifies that a key policy with
// an Allow statement for a specific IAM role emits EdgeGrants.
func TestKMSCollector_KeyPolicyGrants(t *testing.T) {
	roleARN := "arn:aws:iam::111111111111:root"
	fake := baseFakeKMS()
	fake.policies[testKMSKeyID] = `{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": {"AWS": "` + roleARN + `"},
			"Action": "kms:*",
			"Resource": "*"
		}]
	}`

	c := &kmsCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeGrants {
			assert.Equal(t, testKMSKeyARN(), e.SourceID)
			assert.Equal(t, roleARN, e.TargetID)
			found = true
		}
	}
	assert.True(t, found, "expected EdgeGrants edge from key policy")
}

// TestKMSCollector_KeyPolicyConditionalGrant verifies condition metadata
// round-trips through the edge.
func TestKMSCollector_KeyPolicyConditionalGrant(t *testing.T) {
	fake := baseFakeKMS()
	fake.policies[testKMSKeyID] = `{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": "*",
			"Action": "kms:Decrypt",
			"Resource": "*",
			"Condition": {
				"StringEquals": {"kms:ViaService": "s3.us-east-1.amazonaws.com"}
			}
		}]
	}`

	c := &kmsCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var grant *struct{ meta map[string]string }
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeGrants {
			g := struct{ meta map[string]string }{meta: e.Metadata}
			grant = &g
			break
		}
	}
	require.NotNil(t, grant)
	assert.Contains(t, grant.meta["condition"], "kms:ViaService")
}

// TestKMSCollector_AliasEdges verifies that aliases produce EdgeContains
// from key ARN to alias ARN.
func TestKMSCollector_AliasEdges(t *testing.T) {
	aliasARN := "arn:aws:kms:us-east-1:111111111111:alias/my-key"
	fake := baseFakeKMS()
	fake.aliases[testKMSKeyID] = []kmstypes.AliasListEntry{
		{AliasName: awssdk.String("alias/my-key"), AliasArn: awssdk.String(aliasARN)},
	}

	c := &kmsCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeContains {
			assert.Equal(t, testKMSKeyARN(), e.SourceID)
			assert.Equal(t, aliasARN, e.TargetID)
			found = true
		}
	}
	assert.True(t, found, "expected EdgeContains edge for alias")
}

// TestKMSCollector_NoPolicyNoAliases verifies a key with no policy and
// no aliases produces resources but no edges.
func TestKMSCollector_NoPolicyNoAliases(t *testing.T) {
	fake := baseFakeKMS()
	c := &kmsCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges)
}
