// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	artifactregistrypb "cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestArtifactRegistrySubCollector_Name(t *testing.T) {
	c := &artifactRegistrySubCollector{}
	assert.Equal(t, "gcp-artifact-registry", c.Name())
}

// --- EdgeProxiesFrom (virtual) ---

func TestARRepoModeEdges_Virtual(t *testing.T) {
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/virt-repo",
		Mode: artifactregistrypb.Repository_VIRTUAL_REPOSITORY,
		ModeConfig: &artifactregistrypb.Repository_VirtualRepositoryConfig{
			VirtualRepositoryConfig: &artifactregistrypb.VirtualRepositoryConfig{
				UpstreamPolicies: []*artifactregistrypb.UpstreamPolicy{
					{Id: "up-1", Repository: "projects/p/locations/us/repositories/upstream-1"},
					{Id: "up-2", Repository: "projects/p/locations/us/repositories/upstream-2"},
				},
			},
		},
	}
	seen := map[string]bool{}
	edges, proxies := arRepoModeEdges(repo.GetName(), repo, seen)
	require.Len(t, edges, 2)
	assert.Empty(t, proxies, "virtual repos reference internal repos, no proxy needed")

	assert.Equal(t, kgtypes.EdgeProxiesFrom, edges[0].Relationship)
	assert.Equal(t, "projects/p/locations/us/repositories/virt-repo", edges[0].SourceID)
	assert.Equal(t, "projects/p/locations/us/repositories/upstream-1", edges[0].TargetID)

	assert.Equal(t, "projects/p/locations/us/repositories/upstream-2", edges[1].TargetID)
}

// --- EdgeProxiesFrom (remote) ---

func TestARRepoModeEdges_Remote_DockerHub(t *testing.T) {
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/docker-hub-mirror",
		Mode: artifactregistrypb.Repository_REMOTE_REPOSITORY,
		ModeConfig: &artifactregistrypb.Repository_RemoteRepositoryConfig{
			RemoteRepositoryConfig: &artifactregistrypb.RemoteRepositoryConfig{
				Description: "Docker Hub", // user description — must NOT be the ID
				RemoteSource: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository_{
					DockerRepository: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository{
						Upstream: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository_PublicRepository_{
							PublicRepository: artifactregistrypb.RemoteRepositoryConfig_DockerRepository_DOCKER_HUB,
						},
					},
				},
			},
		},
	}
	seen := map[string]bool{}
	edges, proxies := arRepoModeEdges(repo.GetName(), repo, seen)
	require.Len(t, edges, 1)
	require.Len(t, proxies, 1)

	assert.Equal(t, kgtypes.EdgeProxiesFrom, edges[0].Relationship)
	assert.Equal(t, "remote://docker.io", edges[0].TargetID,
		"target must be derived from the typed PublicRepository enum, not Description")
	assert.Equal(t, "public", edges[0].Metadata["remote_type"])
	assert.Equal(t, "Docker Hub", edges[0].Metadata["description"])

	assert.Equal(t, "gcp:ar:remote", proxies[0].ResourceType)
	assert.Equal(t, "Docker Hub", proxies[0].Name)
}

func TestARRepoModeEdges_Remote_DedupeProxy(t *testing.T) {
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/dh-mirror-2",
		Mode: artifactregistrypb.Repository_REMOTE_REPOSITORY,
		ModeConfig: &artifactregistrypb.Repository_RemoteRepositoryConfig{
			RemoteRepositoryConfig: &artifactregistrypb.RemoteRepositoryConfig{
				RemoteSource: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository_{
					DockerRepository: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository{
						Upstream: &artifactregistrypb.RemoteRepositoryConfig_DockerRepository_PublicRepository_{
							PublicRepository: artifactregistrypb.RemoteRepositoryConfig_DockerRepository_DOCKER_HUB,
						},
					},
				},
			},
		},
	}
	seen := map[string]bool{"remote://docker.io": true}
	_, proxies := arRepoModeEdges(repo.GetName(), repo, seen)
	assert.Empty(t, proxies)
}

func TestARRepoModeEdges_Remote_MavenCentral(t *testing.T) {
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/mvn-mirror",
		Mode: artifactregistrypb.Repository_REMOTE_REPOSITORY,
		ModeConfig: &artifactregistrypb.Repository_RemoteRepositoryConfig{
			RemoteRepositoryConfig: &artifactregistrypb.RemoteRepositoryConfig{
				RemoteSource: &artifactregistrypb.RemoteRepositoryConfig_MavenRepository_{
					MavenRepository: &artifactregistrypb.RemoteRepositoryConfig_MavenRepository{
						Upstream: &artifactregistrypb.RemoteRepositoryConfig_MavenRepository_PublicRepository_{
							PublicRepository: artifactregistrypb.RemoteRepositoryConfig_MavenRepository_MAVEN_CENTRAL,
						},
					},
				},
			},
		},
	}
	edges, _ := arRepoModeEdges(repo.GetName(), repo, map[string]bool{})
	require.Len(t, edges, 1)
	assert.Equal(t, "remote://repo.maven.apache.org", edges[0].TargetID)
}

func TestARRepoModeEdges_Remote_NoUpstreamSource(t *testing.T) {
	// User-supplied description but no typed RemoteSource → no edge.
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/dh-mirror",
		Mode: artifactregistrypb.Repository_REMOTE_REPOSITORY,
		ModeConfig: &artifactregistrypb.Repository_RemoteRepositoryConfig{
			RemoteRepositoryConfig: &artifactregistrypb.RemoteRepositoryConfig{
				Description: "some user note",
			},
		},
	}
	edges, proxies := arRepoModeEdges(repo.GetName(), repo, map[string]bool{})
	assert.Empty(t, edges, "no typed RemoteSource → no edge (description alone is insufficient)")
	assert.Empty(t, proxies)
}

// --- Standard mode: no proxy edges ---

func TestARRepoModeEdges_Standard(t *testing.T) {
	repo := &artifactregistrypb.Repository{
		Name: "projects/p/locations/us/repositories/standard",
		Mode: artifactregistrypb.Repository_STANDARD_REPOSITORY,
	}
	edges, proxies := arRepoModeEdges(repo.GetName(), repo, nil)
	assert.Empty(t, edges)
	assert.Empty(t, proxies)
}

// --- EdgeGrants (IAM) ---

func TestARIAMGrantsEdges(t *testing.T) {
	repoName := "projects/p/locations/us/repositories/my-repo"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/artifactregistry.reader",
				Members: []string{"serviceAccount:sa@proj.iam.gserviceaccount.com"},
			},
		},
	}
	edges := arIAMGrantsEdges(repoName, policy)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, repoName, edges[0].SourceID)
	assert.Equal(t, "roles/artifactregistry.reader", edges[0].Metadata["role"])
}

func TestARIAMGrantsEdges_NilPolicy(t *testing.T) {
	assert.Empty(t, arIAMGrantsEdges("repo", nil))
}
