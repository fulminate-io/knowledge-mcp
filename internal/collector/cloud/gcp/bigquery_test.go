// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	bq "google.golang.org/api/bigquery/v2"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBigQuerySubCollector_Name(t *testing.T) {
	c := &bigquerySubCollector{}
	assert.Equal(t, "gcp-bigquery", c.Name())
}

func TestBigQueryDatasetEdges_WithKMSKey(t *testing.T) {
	ds := &bq.Dataset{
		DefaultEncryptionConfiguration: &bq.EncryptionConfiguration{
			KmsKeyName: "projects/p/locations/us/keyRings/kr/cryptoKeys/my-key",
		},
	}
	dsID := "projects/p/datasets/my-dataset"

	edges := bigqueryDatasetEdges(dsID, ds)
	assert.Len(t, edges, 1)
	assert.Equal(t, dsID, edges[0].SourceID)
	assert.Equal(t, "projects/p/locations/us/keyRings/kr/cryptoKeys/my-key", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
}

func TestBigQueryDatasetEdges_NilEncryption(t *testing.T) {
	ds := &bq.Dataset{}
	edges := bigqueryDatasetEdges("projects/p/datasets/ds", ds)
	assert.Nil(t, edges)
}

func TestBigQueryDatasetEdges_EmptyKMSKey(t *testing.T) {
	ds := &bq.Dataset{
		DefaultEncryptionConfiguration: &bq.EncryptionConfiguration{
			KmsKeyName: "",
		},
	}
	edges := bigqueryDatasetEdges("projects/p/datasets/ds", ds)
	assert.Nil(t, edges)
}
