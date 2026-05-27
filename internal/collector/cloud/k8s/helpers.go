// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// resourceID builds a consistent node ID for a k8s resource.
// Namespaced resources: "namespace/Kind/name"
// Cluster-scoped resources: "Kind/name"
func resourceID(namespace, kind, name string) string {
	if namespace == "" {
		return kind + "/" + name
	}
	return namespace + "/" + kind + "/" + name
}

// labelsToMeta converts a k8s label map to metadata, prefixing keys with "label/".
func labelsToMeta(labels map[string]string) map[string]string {
	m := make(map[string]string, len(labels))
	for k, v := range labels {
		m["label/"+k] = v
	}
	return m
}

// marshalJSON marshals a value to JSON bytes, returning nil on error.
func marshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// extractPodTemplateEdges extracts edges from a PodSpec to referenced
// ServiceAccounts, ConfigMaps, Secrets, and PVCs. Used by all workload
// subcollectors (Deployments, StatefulSets, DaemonSets, Jobs, CronJobs).
func extractPodTemplateEdges(sourceID, namespace string, spec corev1.PodSpec) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// ServiceAccount edge.
	if sa := spec.ServiceAccountName; sa != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sourceID,
			TargetID:     resourceID(namespace, "ServiceAccount", sa),
			Relationship: kgtypes.EdgeUsesSA,
		})
	}

	// Volume edges: ConfigMaps, Secrets, PVCs.
	edges = append(edges, extractVolumeEdges(sourceID, namespace, spec.Volumes)...)

	// Env-based ConfigMap and Secret refs from all containers.
	allContainers := append(spec.InitContainers, spec.Containers...)
	edges = append(edges, extractContainerEnvEdges(sourceID, namespace, allContainers)...)

	return deduplicateEdges(edges)
}

// extractVolumeEdges extracts edges from volume mounts to ConfigMaps, Secrets, and PVCs.
func extractVolumeEdges(sourceID, namespace string, volumes []corev1.Volume) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, vol := range volumes {
		if vol.ConfigMap != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     sourceID,
				TargetID:     resourceID(namespace, "ConfigMap", vol.ConfigMap.Name),
				Relationship: kgtypes.EdgeMountsConfigMap,
			})
		}
		if vol.Secret != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     sourceID,
				TargetID:     resourceID(namespace, "Secret", vol.Secret.SecretName),
				Relationship: kgtypes.EdgeMountsSecret,
			})
		}
		if vol.PersistentVolumeClaim != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     sourceID,
				TargetID:     resourceID(namespace, "PersistentVolumeClaim", vol.PersistentVolumeClaim.ClaimName),
				Relationship: kgtypes.EdgeUsesPVC,
			})
		}
		if vol.Projected != nil {
			edges = append(edges, extractProjectedVolumeEdges(sourceID, namespace, vol.Projected.Sources)...)
		}
	}
	return edges
}

// extractProjectedVolumeEdges extracts edges from projected volume sources.
func extractProjectedVolumeEdges(sourceID, namespace string, sources []corev1.VolumeProjection) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, src := range sources {
		if src.ConfigMap != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     sourceID,
				TargetID:     resourceID(namespace, "ConfigMap", src.ConfigMap.Name),
				Relationship: kgtypes.EdgeMountsConfigMap,
			})
		}
		if src.Secret != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     sourceID,
				TargetID:     resourceID(namespace, "Secret", src.Secret.Name),
				Relationship: kgtypes.EdgeMountsSecret,
			})
		}
	}
	return edges
}

// extractContainerEnvEdges extracts edges from container env and envFrom references.
func extractContainerEnvEdges(sourceID, namespace string, containers []corev1.Container) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, c := range containers {
		for _, env := range c.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     sourceID,
					TargetID:     resourceID(namespace, "ConfigMap", env.ValueFrom.ConfigMapKeyRef.Name),
					Relationship: kgtypes.EdgeMountsConfigMap,
				})
			}
			if env.ValueFrom.SecretKeyRef != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     sourceID,
					TargetID:     resourceID(namespace, "Secret", env.ValueFrom.SecretKeyRef.Name),
					Relationship: kgtypes.EdgeMountsSecret,
				})
			}
		}
		for _, envFrom := range c.EnvFrom {
			if envFrom.ConfigMapRef != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     sourceID,
					TargetID:     resourceID(namespace, "ConfigMap", envFrom.ConfigMapRef.Name),
					Relationship: kgtypes.EdgeMountsConfigMap,
				})
			}
			if envFrom.SecretRef != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     sourceID,
					TargetID:     resourceID(namespace, "Secret", envFrom.SecretRef.Name),
					Relationship: kgtypes.EdgeMountsSecret,
				})
			}
		}
	}
	return edges
}

// extractImageTargets parses container images and returns cascade targets for
// cloud container registries (ECR, GCR/Artifact Registry, ACR).
func extractImageTargets(containers []corev1.Container) []cloud.CollectTarget {
	seen := make(map[string]struct{})
	var targets []cloud.CollectTarget

	for _, c := range containers {
		img := c.Image
		if img == "" {
			continue
		}

		// Split image into registry/repo:tag
		parts := strings.SplitN(img, "/", 3)
		if len(parts) < 2 {
			continue // no registry prefix
		}
		registry := parts[0]

		var target *cloud.CollectTarget

		switch {
		// ECR: <account>.dkr.ecr.<region>.amazonaws.com/repo
		case strings.Contains(registry, ".dkr.ecr.") && strings.HasSuffix(registry, ".amazonaws.com"):
			accountID := strings.SplitN(registry, ".", 2)[0]
			target = &cloud.CollectTarget{Collector: "aws", ID: accountID}

		// GCR: gcr.io/<project>/image
		case registry == "gcr.io" || strings.HasSuffix(registry, ".gcr.io"):
			if len(parts) >= 2 {
				project := parts[1]
				// Remove tag if in project part
				project = strings.SplitN(project, ":", 2)[0]
				project = strings.SplitN(project, "/", 2)[0]
				target = &cloud.CollectTarget{Collector: "gcp", ID: project}
			}

		// Artifact Registry: <region>-docker.pkg.dev/<project>/repo/image
		case strings.HasSuffix(registry, "-docker.pkg.dev"):
			if len(parts) >= 2 {
				project := parts[1]
				target = &cloud.CollectTarget{Collector: "gcp", ID: project}
			}

		// ACR: <registry>.azurecr.io/repo
		case strings.HasSuffix(registry, ".azurecr.io"):
			name := strings.TrimSuffix(registry, ".azurecr.io")
			target = &cloud.CollectTarget{Collector: "azure", ID: name}
		}

		if target != nil {
			key := target.Collector + ":" + target.ID
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				targets = append(targets, *target)
			}
		}
	}

	return targets
}

// deduplicateEdges removes duplicate edges by (source, target, relationship) key.
func deduplicateEdges(edges []cloud.EdgeSpec) []cloud.EdgeSpec {
	type edgeKey struct {
		src, tgt string
		rel      kgtypes.EdgeType
	}
	seen := make(map[edgeKey]struct{}, len(edges))
	result := make([]cloud.EdgeSpec, 0, len(edges))
	for _, e := range edges {
		k := edgeKey{e.SourceID, e.TargetID, e.Relationship}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		result = append(result, e)
	}
	return result
}

// formatInt returns a string representation of an int for metadata values.
func formatInt(n int) string {
	return fmt.Sprintf("%d", n)
}

// formatInt32 returns a string representation of an int32 for metadata values.
func formatInt32(n int32) string {
	return fmt.Sprintf("%d", n)
}

// formatInt32Ptr returns a string representation of an *int32, or "0" if nil.
func formatInt32Ptr(n *int32) string {
	if n == nil {
		return "0"
	}
	return fmt.Sprintf("%d", *n)
}
