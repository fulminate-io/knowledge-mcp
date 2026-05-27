// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCertificateEdges(t *testing.T) {
	certID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/certificates/mycert"
	kvID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/myvault"
	kvSecret := "mycert-secret"

	t.Run("emits StoredIn and EncryptsWith when KeyVaultID set", func(t *testing.T) {
		cid := kvID
		name := kvSecret
		cert := &armappservice.AppCertificate{
			ID: &certID,
			Properties: &armappservice.AppCertificateProperties{
				KeyVaultID:         &cid,
				KeyVaultSecretName: &name,
			},
		}
		seenCAs := make(map[string]bool)
		edges, caNodes := certificateEdges(cert, seenCAs)

		var foundStoredIn, foundEncrypts bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeStoredIn {
				assert.Equal(t, certID, e.SourceID)
				assert.Equal(t, kvID, e.TargetID)
				foundStoredIn = true
			}
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, certID, e.SourceID)
				assert.Equal(t, kvID+"/secrets/"+kvSecret, e.TargetID)
				foundEncrypts = true
			}
		}
		assert.True(t, foundStoredIn, "expected EdgeStoredIn")
		assert.True(t, foundEncrypts, "expected EdgeEncryptsWith")
		assert.Empty(t, caNodes)
	})

	t.Run("no KV edges when KeyVaultID empty", func(t *testing.T) {
		empty := ""
		name := kvSecret
		cert := &armappservice.AppCertificate{
			ID: &certID,
			Properties: &armappservice.AppCertificateProperties{
				KeyVaultID:         &empty,
				KeyVaultSecretName: &name,
			},
		}
		seenCAs := make(map[string]bool)
		edges, _ := certificateEdges(cert, seenCAs)
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeStoredIn, e.Relationship)
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})

	t.Run("StoredIn but no EncryptsWith when secret name nil", func(t *testing.T) {
		cid := kvID
		cert := &armappservice.AppCertificate{
			ID: &certID,
			Properties: &armappservice.AppCertificateProperties{
				KeyVaultID: &cid,
			},
		}
		seenCAs := make(map[string]bool)
		edges, _ := certificateEdges(cert, seenCAs)

		var foundStoredIn, foundEncrypts bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeStoredIn {
				foundStoredIn = true
			}
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				foundEncrypts = true
			}
		}
		assert.True(t, foundStoredIn, "expected EdgeStoredIn")
		assert.False(t, foundEncrypts, "should not have EdgeEncryptsWith")
	})

	t.Run("no edges when Properties nil", func(t *testing.T) {
		cert := &armappservice.AppCertificate{ID: &certID}
		seenCAs := make(map[string]bool)
		edges, caNodes := certificateEdges(cert, seenCAs)
		assert.Nil(t, edges)
		assert.Nil(t, caNodes)
	})
}

func TestCertificateIssuerEdges(t *testing.T) {
	certID1 := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/certificates/cert1"
	certID2 := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/certificates/cert2"
	issuer := "DigiCert"

	t.Run("emits IssuedBy with synthetic CA node", func(t *testing.T) {
		cert := &armappservice.AppCertificate{
			ID: &certID1,
			Properties: &armappservice.AppCertificateProperties{
				Issuer: &issuer,
			},
		}
		seenCAs := make(map[string]bool)
		edges, caNodes := certificateEdges(cert, seenCAs)

		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeIssuedBy, edges[0].Relationship)
		assert.Equal(t, certID1, edges[0].SourceID)
		assert.Equal(t, "azure:ca/DigiCert", edges[0].TargetID)

		require.Len(t, caNodes, 1)
		assert.Equal(t, "azure:ca/DigiCert", caNodes[0].ID)
		assert.Equal(t, "azure:ca", caNodes[0].ResourceType)
		assert.Equal(t, "DigiCert", caNodes[0].Name)
		assert.Equal(t, "false", caNodes[0].Metadata["collected"])
	})

	t.Run("multiple certs same issuer — one CA node", func(t *testing.T) {
		seenCAs := make(map[string]bool)
		cert1 := &armappservice.AppCertificate{
			ID:         &certID1,
			Properties: &armappservice.AppCertificateProperties{Issuer: &issuer},
		}
		cert2 := &armappservice.AppCertificate{
			ID:         &certID2,
			Properties: &armappservice.AppCertificateProperties{Issuer: &issuer},
		}

		edges1, ca1 := certificateEdges(cert1, seenCAs)
		edges2, ca2 := certificateEdges(cert2, seenCAs)

		// Both certs emit IssuedBy edges
		require.Len(t, edges1, 1)
		require.Len(t, edges2, 1)
		assert.Equal(t, kgtypes.EdgeIssuedBy, edges1[0].Relationship)
		assert.Equal(t, kgtypes.EdgeIssuedBy, edges2[0].Relationship)

		// Only first cert emits the CA node (dedupe)
		require.Len(t, ca1, 1)
		assert.Empty(t, ca2, "second cert should not re-emit CA node")
	})

	t.Run("no issuer edge when Issuer nil", func(t *testing.T) {
		cert := &armappservice.AppCertificate{
			ID:         &certID1,
			Properties: &armappservice.AppCertificateProperties{},
		}
		seenCAs := make(map[string]bool)
		edges, caNodes := certificateEdges(cert, seenCAs)
		assert.Empty(t, edges)
		assert.Empty(t, caNodes)
	})
}
