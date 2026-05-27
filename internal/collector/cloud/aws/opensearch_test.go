// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	opensearchtypes "github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBuildOpenSearchNode_KMSEncryption(t *testing.T) {
	c := &opensearchCollector{region: "us-east-1", accountID: "111111111111"}
	kmsARN := "arn:aws:kms:us-east-1:111111111111:key/os-key"
	domainARN := "arn:aws:es:us-east-1:111111111111:domain/my-domain"

	t.Run("emits EdgeEncryptsWith when KmsKeyId set", func(t *testing.T) {
		domain := opensearchtypes.DomainStatus{
			ARN:        awssdk.String(domainARN),
			DomainName: awssdk.String("my-domain"),
			EncryptionAtRestOptions: &opensearchtypes.EncryptionAtRestOptions{
				Enabled:  awssdk.Bool(true),
				KmsKeyId: awssdk.String(kmsARN),
			},
		}
		_, edges, err := c.buildOpenSearchNode(domain)
		require.NoError(t, err)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, domainARN, e.SourceID)
				assert.Equal(t, kmsARN, e.TargetID)
				found = true
			}
		}
		require.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when EncryptionAtRestOptions nil", func(t *testing.T) {
		domain := opensearchtypes.DomainStatus{
			ARN:        awssdk.String(domainARN),
			DomainName: awssdk.String("my-domain"),
		}
		_, edges, err := c.buildOpenSearchNode(domain)
		require.NoError(t, err)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}
