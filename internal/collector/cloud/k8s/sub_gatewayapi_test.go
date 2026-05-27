// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestGatewayAPISubCollector_Name(t *testing.T) {
	s := &gatewayAPISubCollector{}
	assert.Equal(t, "gateway-api", s.Name())
}

func TestExtractRouteBackendEdges_ServiceBackends(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{
			"rules": []any{
				map[string]any{
					"backendRefs": []any{
						map[string]any{
							"name": "web-svc",
							"kind": "Service",
						},
						map[string]any{
							"name":      "api-svc",
							"namespace": "backend",
							"kind":      "Service",
						},
					},
				},
			},
		},
	}

	edges := extractRouteBackendEdges(routeID, "default", obj)
	require.Len(t, edges, 2)

	assert.Equal(t, routeID, edges[0].SourceID)
	assert.Equal(t, "default/Service/web-svc", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)

	assert.Equal(t, routeID, edges[1].SourceID)
	assert.Equal(t, "backend/Service/api-svc", edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[1].Relationship)
}

func TestExtractRouteBackendEdges_DefaultKindIsService(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{
			"rules": []any{
				map[string]any{
					"backendRefs": []any{
						map[string]any{
							"name": "web-svc",
							// kind omitted — should default to Service
						},
					},
				},
			},
		},
	}

	edges := extractRouteBackendEdges(routeID, "default", obj)
	require.Len(t, edges, 1)
	assert.Equal(t, "default/Service/web-svc", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
}

func TestExtractRouteBackendEdges_NonServiceKindSkipped(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{
			"rules": []any{
				map[string]any{
					"backendRefs": []any{
						map[string]any{
							"name": "my-bucket",
							"kind": "Bucket", // not a Service
						},
					},
				},
			},
		},
	}

	edges := extractRouteBackendEdges(routeID, "default", obj)
	assert.Empty(t, edges)
}

func TestExtractRouteBackendEdges_NoRules(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractRouteBackendEdges(routeID, "default", obj)
	assert.Empty(t, edges)
}

func TestExtractRouteParentEdges_GatewayParents(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]any{
					"name": "my-gateway",
					"kind": "Gateway",
				},
				map[string]any{
					"name":      "shared-gw",
					"namespace": "infra",
					// kind omitted — should default to Gateway
				},
			},
		},
	}

	edges := extractRouteParentEdges(routeID, "default", obj)
	require.Len(t, edges, 2)

	assert.Equal(t, routeID, edges[0].SourceID)
	assert.Equal(t, "default/Gateway/my-gateway", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)

	assert.Equal(t, routeID, edges[1].SourceID)
	assert.Equal(t, "infra/Gateway/shared-gw", edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[1].Relationship)
}

func TestExtractRouteParentEdges_NonGatewaySkipped(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]any{
					"name": "my-mesh",
					"kind": "Mesh",
				},
			},
		},
	}

	edges := extractRouteParentEdges(routeID, "default", obj)
	assert.Empty(t, edges)
}

func TestExtractRouteParentEdges_NoParentRefs(t *testing.T) {
	routeID := "default/HTTPRoute/my-route"
	obj := map[string]any{
		"spec": map[string]any{},
	}

	edges := extractRouteParentEdges(routeID, "default", obj)
	assert.Empty(t, edges)
}

func TestBackendRefEdge_EmptyName(t *testing.T) {
	edges := backendRefEdge("route-id", "default", map[string]any{
		"kind": "Service",
		"name": "",
	})
	assert.Empty(t, edges)
}

func TestExtractRouteBackendEdges_MultipleRules(t *testing.T) {
	routeID := "default/HTTPRoute/multi-rule"
	obj := map[string]any{
		"spec": map[string]any{
			"rules": []any{
				map[string]any{
					"backendRefs": []any{
						map[string]any{"name": "svc-a"},
					},
				},
				map[string]any{
					"backendRefs": []any{
						map[string]any{"name": "svc-b"},
					},
				},
			},
		},
	}

	edges := extractRouteBackendEdges(routeID, "default", obj)
	require.Len(t, edges, 2)
	assert.Equal(t, "default/Service/svc-a", edges[0].TargetID)
	assert.Equal(t, "default/Service/svc-b", edges[1].TargetID)
}
