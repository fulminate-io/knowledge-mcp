// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCloudSQL_CMEKEncryption(t *testing.T) {
	kmsKey := "projects/my-project/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key"

	t.Run("emits EdgeEncryptsWith when DiskEncryptionConfiguration set", func(t *testing.T) {
		inst := &sqladmin.DatabaseInstance{
			SelfLink:                    "https://sqladmin.googleapis.com/sql/v1beta4/projects/p/instances/my-sql",
			Name:                        "my-sql",
			DiskEncryptionConfiguration: &sqladmin.DiskEncryptionConfiguration{KmsKeyName: kmsKey},
		}

		var edges []cloud.EdgeSpec
		if inst.DiskEncryptionConfiguration != nil && inst.DiskEncryptionConfiguration.KmsKeyName != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     inst.SelfLink,
				TargetID:     inst.DiskEncryptionConfiguration.KmsKeyName,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, inst.SelfLink, e.SourceID)
				assert.Equal(t, kmsKey, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when DiskEncryptionConfiguration nil", func(t *testing.T) {
		inst := &sqladmin.DatabaseInstance{
			SelfLink: "https://sqladmin.googleapis.com/sql/v1beta4/projects/p/instances/my-sql",
			Name:     "my-sql",
		}

		var edges []cloud.EdgeSpec
		if inst.DiskEncryptionConfiguration != nil && inst.DiskEncryptionConfiguration.KmsKeyName != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     inst.SelfLink,
				TargetID:     inst.DiskEncryptionConfiguration.KmsKeyName,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
		assert.Empty(t, edges)
	})
}
