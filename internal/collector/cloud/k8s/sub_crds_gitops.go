// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	registerCRDExtractor("argoproj.io", extractArgoCDEdges)
	registerCRDExtractor("kustomize.toolkit.fluxcd.io", extractFluxKustomizationEdges)
	registerCRDExtractor("helm.toolkit.fluxcd.io", extractFluxHelmReleaseEdges)
	registerCRDExtractor("source.toolkit.fluxcd.io", extractFluxSourceEdges)
	registerCRDExtractor("keda.sh", extractKEDAEdges)
}

// extractArgoCDEdges handles argoproj.io CRDs (ArgoCD Application).
// Emits TARGETS edges to the destination namespace and to resources tracked
// in the Application's status (completeness decision).
func extractArgoCDEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Application → destination namespace.
	destNS, ok, _ := unstructured.NestedString(obj, "spec", "destination", "namespace")
	if ok && destNS != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     nodeID,
			TargetID:     resourceID("", "Namespace", destNS),
			Relationship: kgtypes.EdgeTargets,
		})
	}

	// Application → managed resources from status.resources[].
	edges = append(edges, extractArgoCDStatusResources(nodeID, destNS, obj)...)
	return edges
}

// extractArgoCDStatusResources extracts TARGETS edges from status.resources[],
// which lists every resource the Application manages.
func extractArgoCDStatusResources(nodeID, defaultNS string, obj map[string]any) []cloud.EdgeSpec {
	resources, ok, _ := unstructured.NestedSlice(obj, "status", "resources")
	if !ok {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, res := range resources {
		resMap, ok := res.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := resMap["kind"].(string)
		name, _ := resMap["name"].(string)
		if kind == "" || name == "" {
			continue
		}
		ns, _ := resMap["namespace"].(string)
		if ns == "" {
			ns = defaultNS
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     nodeID,
			TargetID:     resourceID(ns, kind, name),
			Relationship: kgtypes.EdgeTargets,
		})
	}
	return edges
}

// extractFluxKustomizationEdges handles kustomize.toolkit.fluxcd.io CRDs.
// Kustomization → source (GitRepository/OCIRepository) via spec.sourceRef.
func extractFluxKustomizationEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	return fluxSourceRefEdges(nodeID, namespace, obj, "spec", "sourceRef")
}

// extractFluxHelmReleaseEdges handles helm.toolkit.fluxcd.io CRDs.
// HelmRelease → source (HelmRepository/GitRepository) via spec.chart.spec.sourceRef.
func extractFluxHelmReleaseEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	return fluxSourceRefEdges(nodeID, namespace, obj, "spec", "chart", "spec", "sourceRef")
}

// extractFluxSourceEdges handles source.toolkit.fluxcd.io CRDs
// (GitRepository, OCIRepository, HelmRepository, Bucket).
// If a secretRef is present, emit MOUNTS_SECRET edge.
func extractFluxSourceEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	secretName, ok, _ := unstructured.NestedString(obj, "spec", "secretRef", "name")
	if !ok || secretName == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     nodeID,
		TargetID:     resourceID(namespace, "Secret", secretName),
		Relationship: kgtypes.EdgeMountsSecret,
	}}
}

// extractKEDAEdges handles keda.sh CRDs (ScaledObject).
// ScaledObject → target Deployment/StatefulSet via spec.scaleTargetRef.
func extractKEDAEdges(nodeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	name, ok, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "name")
	if !ok || name == "" {
		return nil
	}

	kind, _, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "kind")
	if kind == "" {
		kind = "Deployment" // KEDA default
	}

	return []cloud.EdgeSpec{{
		SourceID:     nodeID,
		TargetID:     resourceID(namespace, kind, name),
		Relationship: kgtypes.EdgeScales,
	}}
}

// fluxSourceRefEdges extracts a TARGETS edge from a Flux sourceRef at the
// given nested path. Shared by Kustomization and HelmRelease extractors.
func fluxSourceRefEdges(nodeID, namespace string, obj map[string]any, fields ...string) []cloud.EdgeSpec {
	ref, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok {
		return nil
	}

	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	if kind == "" || name == "" {
		return nil
	}

	ns, _ := ref["namespace"].(string)
	if ns == "" {
		ns = namespace
	}

	return []cloud.EdgeSpec{{
		SourceID:     nodeID,
		TargetID:     resourceID(ns, kind, name),
		Relationship: kgtypes.EdgeTargets,
	}}
}
