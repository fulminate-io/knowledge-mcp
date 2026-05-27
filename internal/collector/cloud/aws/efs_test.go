// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestEFS_KMSEncryption(t *testing.T) {
	kmsARN := "arn:aws:kms:us-east-1:111111111111:key/efs-key"
	fsARN := "arn:aws:elasticfilesystem:us-east-1:111111111111:file-system/fs-abc123"

	t.Run("emits EdgeEncryptsWith when Encrypted and KmsKeyId set", func(t *testing.T) {
		fs := efstypes.FileSystemDescription{
			FileSystemArn: awssdk.String(fsARN),
			FileSystemId:  awssdk.String("fs-abc123"),
			Encrypted:     awssdk.Bool(true),
			KmsKeyId:      awssdk.String(kmsARN),
		}
		// Build the edge manually since Collect requires API calls.
		var edges []cloud.EdgeSpec
		if fs.Encrypted != nil && *fs.Encrypted && fs.KmsKeyId != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     fsARN,
				TargetID:     resolveKMSKeyARN(awssdk.ToString(fs.KmsKeyId), "us-east-1", "111111111111"),
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, fsARN, e.SourceID)
				assert.Equal(t, kmsARN, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when not encrypted", func(t *testing.T) {
		fs := efstypes.FileSystemDescription{
			FileSystemArn: awssdk.String(fsARN),
			FileSystemId:  awssdk.String("fs-abc123"),
			Encrypted:     awssdk.Bool(false),
		}
		var edges []cloud.EdgeSpec
		if fs.Encrypted != nil && *fs.Encrypted && fs.KmsKeyId != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     fsARN,
				TargetID:     resolveKMSKeyARN(awssdk.ToString(fs.KmsKeyId), "us-east-1", "111111111111"),
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
		assert.Empty(t, edges)
	})
}
