// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestInstanceEdges_KMSEncryption(t *testing.T) {
	c := &rdsCollector{region: "us-east-1", accountID: "111111111111"}
	kmsARN := "arn:aws:kms:us-east-1:111111111111:key/test-key-id"
	dbARN := "arn:aws:rds:us-east-1:111111111111:db:my-db"

	t.Run("emits EdgeEncryptsWith when KmsKeyId set", func(t *testing.T) {
		instance := rdstypes.DBInstance{KmsKeyId: awssdk.String(kmsARN)}
		edges := c.instanceEdges(dbARN, instance)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, dbARN, e.SourceID)
				assert.Equal(t, kmsARN, e.TargetID)
				found = true
			}
		}
		require.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when KmsKeyId nil", func(t *testing.T) {
		instance := rdstypes.DBInstance{}
		edges := c.instanceEdges(dbARN, instance)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}

func TestInstanceEdges_ReadReplicaChain(t *testing.T) {
	c := &rdsCollector{region: "us-east-1", accountID: "111111111111"}

	t.Run("replica points back to primary by bare identifier", func(t *testing.T) {
		dbARN := "arn:aws:rds:us-east-1:111111111111:db:my-replica"
		instance := rdstypes.DBInstance{
			ReadReplicaSourceDBInstanceIdentifier: awssdk.String("primary-db"),
		}
		edges := c.instanceEdges(dbARN, instance)

		var found bool
		for _, e := range edges {
			if e.Relationship != kgtypes.EdgeReplicatesTo {
				continue
			}
			assert.Equal(t, dbARN, e.SourceID)
			assert.Equal(t, "arn:aws:rds:us-east-1:111111111111:db:primary-db", e.TargetID)
			assert.Equal(t, "replica", e.Metadata["role"])
			found = true
		}
		require.True(t, found, "expected EdgeReplicatesTo from replica → primary")
	})

	t.Run("primary lists replicas with full ARN preserved", func(t *testing.T) {
		dbARN := "arn:aws:rds:us-east-1:111111111111:db:primary-db"
		crossRegion := "arn:aws:rds:eu-west-1:111111111111:db:primary-db-replica-eu"
		instance := rdstypes.DBInstance{
			ReadReplicaDBInstanceIdentifiers: []string{
				"primary-db-replica-1",
				crossRegion,
			},
		}
		edges := c.instanceEdges(dbARN, instance)

		seen := map[string]string{}
		for _, e := range edges {
			if e.Relationship != kgtypes.EdgeReplicatesTo {
				continue
			}
			seen[e.TargetID] = e.Metadata["role"]
		}
		assert.Equal(t, "primary", seen["arn:aws:rds:us-east-1:111111111111:db:primary-db-replica-1"])
		assert.Equal(t, "primary", seen[crossRegion])
	})

	t.Run("no edges when primary has no replicas", func(t *testing.T) {
		dbARN := "arn:aws:rds:us-east-1:111111111111:db:standalone"
		instance := rdstypes.DBInstance{}
		edges := c.instanceEdges(dbARN, instance)
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeReplicatesTo, e.Relationship)
		}
	})
}
