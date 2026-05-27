// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- ArgoCD ---

func TestExtractArgoCDEdges_DestinationNamespace(t *testing.T) {
	nodeID := "argocd/Application/my-app"
	obj := map[string]any{
		"spec": map[string]any{
			"destination": map[string]any{
				"namespace": "production",
			},
		},
	}

	edges := extractArgoCDEdges(nodeID, "argocd", obj)
	require.NotEmpty(t, edges)

	assert.Equal(t, nodeID, edges[0].SourceID)
	assert.Equal(t, "Namespace/production", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestExtractArgoCDEdges_StatusResources(t *testing.T) {
	nodeID := "argocd/Application/my-app"
	obj := map[string]any{
		"spec": map[string]any{
			"destination": map[string]any{
				"namespace": "production",
			},
		},
		"status": map[string]any{
			"resources": []any{
				map[string]any{
					"kind":      "Deployment",
					"name":      "web",
					"namespace": "production",
				},
				map[string]any{
					"kind": "Service",
					"name": "web-svc",
					// namespace omitted — falls back to destination namespace
				},
			},
		},
	}

	edges := extractArgoCDEdges(nodeID, "argocd", obj)
	// 1 destination namespace + 2 status resources = 3 edges
	require.Len(t, edges, 3)

	// Destination namespace edge.
	assert.Equal(t, "Namespace/production", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)

	// Status resource: Deployment with explicit namespace.
	assert.Equal(t, "production/Deployment/web", edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[1].Relationship)

	// Status resource: Service with fallback namespace.
	assert.Equal(t, "production/Service/web-svc", edges[2].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[2].Relationship)
}

func TestExtractArgoCDEdges_NoDestination(t *testing.T) {
	nodeID := "argocd/Application/empty-app"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractArgoCDEdges(nodeID, "argocd", obj)
	assert.Empty(t, edges)
}

func TestExtractArgoCDEdges_StatusResourceMissingFields(t *testing.T) {
	nodeID := "argocd/Application/partial"
	obj := map[string]any{
		"spec": map[string]any{
			"destination": map[string]any{
				"namespace": "default",
			},
		},
		"status": map[string]any{
			"resources": []any{
				map[string]any{
					"kind": "Deployment",
					// name missing — should be skipped
				},
				map[string]any{
					"name": "web",
					// kind missing — should be skipped
				},
			},
		},
	}

	edges := extractArgoCDEdges(nodeID, "argocd", obj)
	// Only the destination namespace edge.
	require.Len(t, edges, 1)
	assert.Equal(t, "Namespace/default", edges[0].TargetID)
}

// --- Flux ---

func TestExtractFluxKustomizationEdges_SourceRef(t *testing.T) {
	nodeID := "flux-system/Kustomization/my-app"
	obj := map[string]any{
		"spec": map[string]any{
			"sourceRef": map[string]any{
				"kind": "GitRepository",
				"name": "my-repo",
			},
		},
	}

	edges := extractFluxKustomizationEdges(nodeID, "flux-system", obj)
	require.Len(t, edges, 1)

	assert.Equal(t, nodeID, edges[0].SourceID)
	assert.Equal(t, "flux-system/GitRepository/my-repo", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestExtractFluxKustomizationEdges_CrossNamespaceSourceRef(t *testing.T) {
	nodeID := "apps/Kustomization/frontend"
	obj := map[string]any{
		"spec": map[string]any{
			"sourceRef": map[string]any{
				"kind":      "GitRepository",
				"name":      "shared-repo",
				"namespace": "flux-system",
			},
		},
	}

	edges := extractFluxKustomizationEdges(nodeID, "apps", obj)
	require.Len(t, edges, 1)
	assert.Equal(t, "flux-system/GitRepository/shared-repo", edges[0].TargetID)
}

func TestExtractFluxKustomizationEdges_NoSourceRef(t *testing.T) {
	nodeID := "flux-system/Kustomization/empty"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractFluxKustomizationEdges(nodeID, "flux-system", obj)
	assert.Empty(t, edges)
}

func TestExtractFluxHelmReleaseEdges_ChartSourceRef(t *testing.T) {
	nodeID := "flux-system/HelmRelease/redis"
	obj := map[string]any{
		"spec": map[string]any{
			"chart": map[string]any{
				"spec": map[string]any{
					"sourceRef": map[string]any{
						"kind": "HelmRepository",
						"name": "bitnami",
					},
				},
			},
		},
	}

	edges := extractFluxHelmReleaseEdges(nodeID, "flux-system", obj)
	require.Len(t, edges, 1)

	assert.Equal(t, nodeID, edges[0].SourceID)
	assert.Equal(t, "flux-system/HelmRepository/bitnami", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestExtractFluxHelmReleaseEdges_NoChart(t *testing.T) {
	nodeID := "flux-system/HelmRelease/empty"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractFluxHelmReleaseEdges(nodeID, "flux-system", obj)
	assert.Empty(t, edges)
}

func TestExtractFluxSourceEdges_SecretRef(t *testing.T) {
	nodeID := "flux-system/GitRepository/private-repo"
	obj := map[string]any{
		"spec": map[string]any{
			"secretRef": map[string]any{
				"name": "git-credentials",
			},
		},
	}

	edges := extractFluxSourceEdges(nodeID, "flux-system", obj)
	require.Len(t, edges, 1)

	assert.Equal(t, nodeID, edges[0].SourceID)
	assert.Equal(t, "flux-system/Secret/git-credentials", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeMountsSecret, edges[0].Relationship)
}

func TestExtractFluxSourceEdges_NoSecretRef(t *testing.T) {
	nodeID := "flux-system/GitRepository/public-repo"
	obj := map[string]any{
		"spec": map[string]any{
			"url": "https://github.com/example/repo",
		},
	}

	edges := extractFluxSourceEdges(nodeID, "flux-system", obj)
	assert.Empty(t, edges)
}

// --- KEDA ---

func TestExtractKEDAEdges_ScaleTargetRef(t *testing.T) {
	nodeID := "default/ScaledObject/web-scaler"
	obj := map[string]any{
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"name": "web-deploy",
			},
		},
	}

	edges := extractKEDAEdges(nodeID, "default", obj)
	require.Len(t, edges, 1)

	assert.Equal(t, nodeID, edges[0].SourceID)
	// Default kind is Deployment.
	assert.Equal(t, "default/Deployment/web-deploy", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeScales, edges[0].Relationship)
}

func TestExtractKEDAEdges_ExplicitKind(t *testing.T) {
	nodeID := "default/ScaledObject/stateful-scaler"
	obj := map[string]any{
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"name": "db-sts",
				"kind": "StatefulSet",
			},
		},
	}

	edges := extractKEDAEdges(nodeID, "default", obj)
	require.Len(t, edges, 1)
	assert.Equal(t, "default/StatefulSet/db-sts", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeScales, edges[0].Relationship)
}

func TestExtractKEDAEdges_NoScaleTargetRef(t *testing.T) {
	nodeID := "default/ScaledObject/empty"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractKEDAEdges(nodeID, "default", obj)
	assert.Empty(t, edges)
}

func TestExtractKEDAEdges_MissingName(t *testing.T) {
	nodeID := "default/ScaledObject/no-name"
	obj := map[string]any{
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"kind": "Deployment",
			},
		},
	}

	edges := extractKEDAEdges(nodeID, "default", obj)
	assert.Empty(t, edges)
}

// --- Extractor Registration ---

func TestGitOpsExtractorsRegistered(t *testing.T) {
	groups := []string{
		"argoproj.io",
		"kustomize.toolkit.fluxcd.io",
		"helm.toolkit.fluxcd.io",
		"source.toolkit.fluxcd.io",
		"keda.sh",
	}
	for _, g := range groups {
		_, ok := crdEdgeExtractors[g]
		assert.True(t, ok, "extractor not registered for group %s", g)
	}
}
