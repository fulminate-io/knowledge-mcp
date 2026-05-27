// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// crdEdgeExtractor extracts edges from a CRD instance.
// It receives the instance's computed node ID, namespace, and the raw unstructured object.
type crdEdgeExtractor func(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec

// crdEdgeExtractors maps CRD API groups to their edge extraction functions.
// Populated by registerCRDExtractor from this file's init() and from
// sibling sub_crds_*.go files; the helper panics on duplicate API group so
// a typo in one extractor doesn't silently shadow another.
var crdEdgeExtractors = map[string]crdEdgeExtractor{}

// registerCRDExtractor adds an extractor for a CRD API group. Panics on
// empty group, nil extractor, or duplicate registration.
func registerCRDExtractor(apiGroup string, fn crdEdgeExtractor) {
	if apiGroup == "" {
		panic("k8s: registerCRDExtractor called with empty API group")
	}
	if fn == nil {
		panic("k8s: registerCRDExtractor called with nil extractor")
	}
	if _, exists := crdEdgeExtractors[apiGroup]; exists {
		panic("k8s: duplicate CRD edge extractor for API group " + apiGroup)
	}
	crdEdgeExtractors[apiGroup] = fn
}

func init() {
	registerCRDExtractor("cert-manager.io", extractCertManagerEdges)
	registerCRDExtractor("external-secrets.io", extractExternalSecretsEdges)
	registerCRDExtractor("networking.istio.io", extractIstioNetworkingEdges)
	registerCRDExtractor("traefik.io", extractTraefikEdges)
}

const maxInstancesPerCRD = 1000

// crdsSubCollector discovers CRDs and their instances via the discovery
// and dynamic clients. Instance count per CRD is capped at maxInstancesPerCRD.
type crdsSubCollector struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	crdLister     crdLister
}

// crdLister abstracts CRD listing for testability.
type crdLister interface {
	ListCRDs(ctx context.Context) ([]apiextensionsv1.CustomResourceDefinition, error)
}

func (s *crdsSubCollector) Name() string { return "crds" }

func (s *crdsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	crds, err := s.crdLister.ListCRDs(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list CRDs: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, crd := range crds {
		// CRD node (cluster-scoped).
		crdID := resourceID("", "CustomResourceDefinition", crd.Name)

		meta := labelsToMeta(crd.Labels)
		meta["group"] = crd.Spec.Group
		meta["scope"] = string(crd.Spec.Scope)
		if len(crd.Spec.Versions) > 0 {
			var versions []string
			for _, v := range crd.Spec.Versions {
				if v.Served {
					versions = append(versions, v.Name)
				}
			}
			meta["versions"] = strings.Join(versions, ",")
		}
		if len(crd.Spec.Names.ShortNames) > 0 {
			meta["short_names"] = strings.Join(crd.Spec.Names.ShortNames, ",")
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           crdID,
			Name:         crd.Name,
			ResourceType: "CustomResourceDefinition",
			Content:      marshalJSON(crd),
			Metadata:     meta,
		})

		// List instances of this CRD.
		gvr := preferredGVR(crd)
		if gvr.Resource == "" {
			continue // no served version
		}

		isNamespaced := crd.Spec.Scope == apiextensionsv1.NamespaceScoped
		instances, edges, listErr := s.listInstances(ctx, gvr, isNamespaced)
		if listErr != nil {
			// Best-effort: skip instances if we can't list them.
			continue
		}

		result.Resources = append(result.Resources, instances...)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// preferredGVR returns the GVR for the preferred (first served) version of a CRD.
func preferredGVR(crd apiextensionsv1.CustomResourceDefinition) schema.GroupVersionResource {
	for _, v := range crd.Spec.Versions {
		if v.Served {
			return schema.GroupVersionResource{
				Group:    crd.Spec.Group,
				Version:  v.Name,
				Resource: crd.Spec.Names.Plural,
			}
		}
	}
	return schema.GroupVersionResource{}
}

// listInstances lists instances of a CRD via the dynamic client, capped at maxInstancesPerCRD.
// If a crdEdgeExtractor is registered for the GVR's group, edges are extracted from each instance.
func (s *crdsSubCollector) listInstances(ctx context.Context, gvr schema.GroupVersionResource, namespaced bool) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var resources []cloud.ResourceSpec
	var edges []cloud.EdgeSpec

	var client dynamic.ResourceInterface
	if namespaced {
		client = s.dynamicClient.Resource(gvr).Namespace("")
	} else {
		client = s.dynamicClient.Resource(gvr)
	}

	// Use limit to cap instances.
	list, err := client.List(ctx, metav1.ListOptions{
		Limit: maxInstancesPerCRD,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", gvr.Resource, err)
	}

	kind := gvr.Resource
	// Try to get the Kind from the items themselves.
	if len(list.Items) > 0 {
		kind = list.Items[0].GetKind()
	}

	extractor := crdEdgeExtractors[gvr.Group]

	for _, item := range list.Items {
		namespace := item.GetNamespace()
		name := item.GetName()
		id := resourceID(namespace, kind, name)

		contentJSON, err := json.Marshal(item.Object)
		if err != nil {
			continue // skip instances with unmarshalable content
		}

		meta := labelsToMeta(item.GetLabels())
		if namespace != "" {
			meta["namespace"] = namespace
		}
		meta["api_version"] = item.GetAPIVersion()

		resources = append(resources, cloud.ResourceSpec{
			ID:           id,
			Name:         name,
			ResourceType: kind,
			Region:       namespace,
			Content:      contentJSON,
			Metadata:     meta,
		})

		// Emit OWNED_BY edges for every ownerReference on the CR. Mirrors
		// the sub_pods.go:86-94 pattern — emit for ALL owner refs (not just
		// controller refs); let the extra `controller` metadata key on the
		// edge mark the controller-ref case for downstream filtering.
		// OwnerReferences are always same-namespace per the k8s spec, so
		// the CR's own namespace is used as the target namespace; cluster-
		// scoped CRs produce a cluster-scoped target ID correctly via
		// resourceID's empty-namespace branch.
		for _, ref := range item.GetOwnerReferences() {
			edgeMeta := map[string]string{}
			if ref.Controller != nil && *ref.Controller {
				edgeMeta["controller"] = "true"
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID(namespace, ref.Kind, ref.Name),
				Relationship: kgtypes.EdgeOwnedBy,
				Metadata:     edgeMeta,
			})
		}

		if extractor != nil {
			edges = append(edges, extractor(id, namespace, item.Object)...)
		}
	}

	return resources, edges, nil
}

// --- CRD edge extractors ---

// extractCertManagerEdges handles cert-manager.io CRDs.
// Certificate → Issuer/ClusterIssuer via spec.issuerRef.
func extractCertManagerEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	name, ok, _ := unstructured.NestedString(obj, "spec", "issuerRef", "name")
	if !ok || name == "" {
		return nil
	}

	kind, _, _ := unstructured.NestedString(obj, "spec", "issuerRef", "kind")
	if kind == "" {
		kind = "Issuer"
	}

	var targetID string
	if kind == "ClusterIssuer" {
		targetID = resourceID("", "ClusterIssuer", name)
	} else {
		targetID = resourceID(namespace, kind, name)
	}

	return []cloud.EdgeSpec{{
		SourceID:     nodeID,
		TargetID:     targetID,
		Relationship: kgtypes.EdgeIssuedBy,
	}}
}

// extractExternalSecretsEdges handles external-secrets.io CRDs.
// ExternalSecret → SecretStore/ClusterSecretStore via spec.secretStoreRef.
func extractExternalSecretsEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	name, ok, _ := unstructured.NestedString(obj, "spec", "secretStoreRef", "name")
	if !ok || name == "" {
		return nil
	}

	kind, _, _ := unstructured.NestedString(obj, "spec", "secretStoreRef", "kind")
	if kind == "" {
		kind = "SecretStore"
	}

	var targetID string
	if kind == "ClusterSecretStore" {
		targetID = resourceID("", "ClusterSecretStore", name)
	} else {
		targetID = resourceID(namespace, kind, name)
	}

	return []cloud.EdgeSpec{{
		SourceID:     nodeID,
		TargetID:     targetID,
		Relationship: kgtypes.EdgeReferencesStore,
	}}
}

// extractIstioNetworkingEdges handles networking.istio.io CRDs.
// VirtualService → Service via spec.http[].route[].destination.host and spec.tcp[].route[].destination.host.
// Host formats: "reviews" (short), "reviews.prod" (namespaced), "reviews.prod.svc.cluster.local" (FQDN).
func extractIstioNetworkingEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Extract from both http and tcp route blocks.
	for _, protocol := range []string{"http", "tcp"} {
		routes, ok, _ := unstructured.NestedSlice(obj, "spec", protocol)
		if !ok {
			continue
		}
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			routeEntries, ok := routeMap["route"].([]any)
			if !ok {
				continue
			}
			for _, entry := range routeEntries {
				entryMap, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				destMap, ok := entryMap["destination"].(map[string]any)
				if !ok {
					continue
				}
				host, ok := destMap["host"].(string)
				if !ok || host == "" {
					continue
				}

				svcName, svcNamespace := parseIstioHost(host, namespace)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     nodeID,
					TargetID:     resourceID(svcNamespace, "Service", svcName),
					Relationship: kgtypes.EdgeRoutesTo,
				})
			}
		}
	}

	return edges
}

// parseIstioHost parses an Istio destination host into service name and namespace.
// Handles three formats:
//   - Short name: "reviews" → ("reviews", defaultNamespace)
//   - Namespaced: "reviews.prod" → ("reviews", "prod")
//   - FQDN: "reviews.prod.svc.cluster.local" → ("reviews", "prod")
func parseIstioHost(host, defaultNamespace string) (name, namespace string) {
	parts := strings.Split(host, ".")

	switch {
	case len(parts) == 1:
		// Short name: use the VirtualService's namespace.
		return parts[0], defaultNamespace
	case len(parts) == 2:
		// Namespaced: "service.namespace"
		return parts[0], parts[1]
	default:
		// FQDN: "service.namespace.svc.cluster.local" or similar.
		return parts[0], parts[1]
	}
}

// extractTraefikEdges handles traefik.io CRDs.
// IngressRoute → Service via spec.routes[].services[].name.
// IngressRoute → Middleware via spec.routes[].middlewares[].name.
func extractTraefikEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	routes, ok, _ := unstructured.NestedSlice(obj, "spec", "routes")
	if !ok {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, route := range routes {
		routeMap, ok := route.(map[string]any)
		if !ok {
			continue
		}
		edges = append(edges, extractTraefikRouteRefs(nodeID, namespace, routeMap, "services", "Service", kgtypes.EdgeRoutesTo)...)
		edges = append(edges, extractTraefikRouteRefs(nodeID, namespace, routeMap, "middlewares", "Middleware", kgtypes.EdgeUsesMiddleware)...)
	}
	return edges
}

// extractTraefikRouteRefs extracts edges from a Traefik route's named references (services or middlewares).
func extractTraefikRouteRefs(nodeID, defaultNS string, routeMap map[string]any, key, kind string, rel kgtypes.EdgeType) []cloud.EdgeSpec {
	items, ok := routeMap[key].([]any)
	if !ok {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = defaultNS
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     nodeID,
			TargetID:     resourceID(ns, kind, name),
			Relationship: rel,
		})
	}
	return edges
}
