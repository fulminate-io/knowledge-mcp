// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"
	"time"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestKMSSubCollector_Name(t *testing.T) {
	c := &kmsSubCollector{}
	assert.Equal(t, "gcp-kms", c.Name())
}

func TestKMSLocationFromName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects/p/locations/us-central1/keyRings/r", "us-central1"},
		{"projects/p/locations/global/keyRings/r/cryptoKeys/k", "global"},
		{"projects/p/locations/europe-west4", "europe-west4"},
		{"no-location-here", ""},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, kmsLocationFromName(tt.input))
	}
}

func TestCryptoKeyMetadata_EncryptDecryptWithRotation(t *testing.T) {
	key := &kmspb.CryptoKey{
		Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
		VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
			Algorithm:       kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
		},
		RotationSchedule: &kmspb.CryptoKey_RotationPeriod{
			RotationPeriod: durationpb.New(30 * 24 * time.Hour),
		},
	}
	meta := cryptoKeyMetadata(key)
	assert.Equal(t, "ENCRYPT_DECRYPT", meta["purpose"])
	assert.Equal(t, "GOOGLE_SYMMETRIC_ENCRYPTION", meta["algorithm"])
	assert.Equal(t, "SOFTWARE", meta["protectionLevel"])
	assert.Equal(t, (30 * 24 * time.Hour).String(), meta["rotationPeriod"])
}

func TestCryptoKeyMetadata_NoTemplate(t *testing.T) {
	key := &kmspb.CryptoKey{
		Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN,
	}
	meta := cryptoKeyMetadata(key)
	assert.Equal(t, "ASYMMETRIC_SIGN", meta["purpose"])
	_, hasAlg := meta["algorithm"]
	assert.False(t, hasAlg)
	_, hasRot := meta["rotationPeriod"]
	assert.False(t, hasRot)
}

// --- EdgeGrants (IAM per CryptoKey) ---

func TestKMSKeyGrantsEdges(t *testing.T) {
	const projectID = "p"
	keyName := "projects/p/locations/us/keyRings/r/cryptoKeys/k"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/cloudkms.cryptoKeyEncrypterDecrypter",
				Members: []string{"serviceAccount:sa@proj.iam.gserviceaccount.com"},
			},
		},
	}
	edges := kmsKeyGrantsEdges(projectID, keyName, policy)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, keyName, edges[0].SourceID)
	assert.Equal(t,
		"projects/proj/serviceAccounts/sa@proj.iam.gserviceaccount.com",
		edges[0].TargetID,
		"target must be canonical SA path with project parsed from email")
	assert.Equal(t, "roles/cloudkms.cryptoKeyEncrypterDecrypter", edges[0].Metadata["role"])
	assert.Equal(t, "serviceAccount:sa@proj.iam.gserviceaccount.com", edges[0].Metadata["member"])
}

func TestKMSKeyGrantsEdges_DropsUnsupportedMembers(t *testing.T) {
	const projectID = "p"
	keyName := "projects/p/locations/us/keyRings/r/cryptoKeys/k"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/cloudkms.cryptoKeyDecrypter",
				Members: []string{"user:alice@example.com", "domain:example.com"},
			},
		},
	}
	edges := kmsKeyGrantsEdges(projectID, keyName, policy)
	assert.Empty(t, edges, "user:/domain: members are dropped (matches iam.go)")
}

func TestKMSKeyGrantsEdges_GroupPlaceholder(t *testing.T) {
	const projectID = "p"
	keyName := "projects/p/locations/us/keyRings/r/cryptoKeys/k"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/cloudkms.cryptoKeyDecrypter",
				Members: []string{"group:devs@example.com"},
			},
		},
	}
	edges := kmsKeyGrantsEdges(projectID, keyName, policy)
	require.Len(t, edges, 1)
	assert.Equal(t, "group:devs@example.com", edges[0].TargetID,
		"group: stays as placeholder for postpopulate to resolve")
}

func TestKMSKeyGrantsEdges_NilPolicy(t *testing.T) {
	assert.Empty(t, kmsKeyGrantsEdges("p", "k", nil))
}

func TestKMSKeyGrantsEdges_EmptyBindings(t *testing.T) {
	assert.Empty(t, kmsKeyGrantsEdges("p", "k", &iampb.Policy{}))
}
