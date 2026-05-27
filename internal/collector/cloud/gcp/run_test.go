// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	runpb "cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCloudRunSubCollector_Name(t *testing.T) {
	c := &cloudRunSubCollector{}
	assert.Equal(t, "gcp-cloud-run", c.Name())
}

func TestCloudRunEdges_EncryptsWith(t *testing.T) {
	kmsKey := "projects/p/locations/us/keyRings/ring/cryptoKeys/key"
	name := "projects/p/locations/us-central1/services/my-svc"

	t.Run("emits ENCRYPTS_WITH when EncryptionKey set", func(t *testing.T) {
		tmpl := &runpb.RevisionTemplate{
			EncryptionKey: kmsKey,
		}
		edges := cloudRunEdges("p", name, tmpl)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, name, e.SourceID)
				assert.Equal(t, kmsKey, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no ENCRYPTS_WITH when EncryptionKey empty", func(t *testing.T) {
		tmpl := &runpb.RevisionTemplate{}
		edges := cloudRunEdges("p", name, tmpl)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})

	t.Run("nil template returns no edges", func(t *testing.T) {
		edges := cloudRunEdges("p", name, nil)
		assert.Empty(t, edges)
	})
}

// --- EdgeGrants (IAM) ---

func TestCloudRunIAMGrantsEdges_PublicAccess(t *testing.T) {
	// roles/run.invoker → allUsers is the canonical "public Cloud Run service"
	// signal — without IAM-binding edges, public-exposure analysis cannot see it.
	svcName := "projects/p/locations/us-central1/services/public-svc"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/run.invoker",
				Members: []string{"allUsers"},
			},
		},
	}
	edges := cloudRunIAMGrantsEdges(svcName, policy)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, svcName, edges[0].SourceID)
	assert.Equal(t, "allUsers", edges[0].TargetID)
	assert.Equal(t, "roles/run.invoker", edges[0].Metadata["role"])
}

func TestCloudRunIAMGrantsEdges_MultipleMembers(t *testing.T) {
	svcName := "projects/p/locations/us-central1/services/svc"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role: "roles/run.invoker",
				Members: []string{
					"serviceAccount:sa@proj.iam.gserviceaccount.com",
					"allAuthenticatedUsers",
				},
			},
		},
	}
	edges := cloudRunIAMGrantsEdges(svcName, policy)
	require.Len(t, edges, 2)
	// Members are sorted for determinism.
	assert.Equal(t, "allAuthenticatedUsers", edges[0].TargetID)
	assert.Equal(t, "serviceAccount:sa@proj.iam.gserviceaccount.com", edges[1].TargetID)
}

func TestCloudRunIAMGrantsEdges_NilPolicy(t *testing.T) {
	assert.Empty(t, cloudRunIAMGrantsEdges("svc", nil))
}
