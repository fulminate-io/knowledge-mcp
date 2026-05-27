// SPDX-License-Identifier: Apache-2.0

package cloud

import "github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

// ResourceSpec describes a cloud infrastructure resource discovered by a SubCollector.
// Fields map directly to *knowledgev1.Node fields via BuildNode. SDK-specific types never
// appear here -- this is the boundary between provider SDKs and the knowledge graph.
type ResourceSpec struct {
	// ID is the real cloud provider ID (ARN, self-link, Azure resource ID,
	// k8s namespace/kind/name). Used directly as the node ID, unmodified.
	ID string

	// Name is the human-readable display name.
	Name string

	// ResourceType classifies the resource. Convention is per-provider:
	//   - AWS: hyphen form (e.g. "ec2-instance", "s3-bucket")
	//   - GCP: colon-namespaced (e.g. "gcp:compute:instance")
	//   - Azure: ARM resource type string (e.g. "Microsoft.Compute/virtualMachines")
	//   - K8s: bare CamelCase Kind (e.g. "Pod", "Deployment")
	// The full canonical key list is the Phase 0 inventory finding linked
	// from the deterministic-summary plan.
	ResourceType string

	// Region is the cloud region or zone where the resource lives.
	Region string

	// Content is the raw API JSON from the SDK response, preserved as-is for full fidelity.
	Content []byte

	// Metadata holds additional key-value pairs (account, project, labels, etc.).
	Metadata map[string]string
}

// EdgeSpec describes a directed relationship from one cloud resource to another.
// The runner converts these to store.BatchEdge (the gap edge carrier) via BuildEdge.
type EdgeSpec struct {
	// SourceID is the real cloud ID of the resource that declares this edge.
	// The runner uses this as the fromID when building store.BatchEdge.
	SourceID string

	// TargetID is the real cloud ID of the target resource.
	TargetID string

	// Relationship is the edge type from pkg/kgtypes/edge_types.go
	// (e.g. kgtypes.EdgeMountsSecret, kgtypes.EdgeUsesSA, kgtypes.EdgeSelects).
	Relationship kgtypes.EdgeType

	// Metadata holds optional key-value pairs describing how this edge was
	// established (e.g. role_source, policy_name, port). BuildEdge serializes
	// non-empty Metadata as JSON into BatchEdge.Evidence and sets Method to
	// "cloud-collect". Nil or empty Metadata leaves Evidence and Method empty
	// for backward compatibility.
	Metadata map[string]string
}

// CollectTarget identifies a follow-up collection target discovered during collection.
// For example, an EKS cluster discovery yields a CollectTarget for the k8s collector
// to enumerate that cluster's workloads.
type CollectTarget struct {
	// Collector is the collector type name (e.g. "k8s", "aws").
	Collector string

	// ID is the target identifier (e.g. k8s cluster context, AWS account+region).
	ID string

	// ResolutionID is the provider-canonical ID of the target when ID is a
	// lossy form. AKS sets this to the full ARM resource path while ID
	// remains the kubeconfig-context (cluster name) so client-side cluster
	// proxy emission can recover the canonical cluster identity. Empty for
	// targets whose ID is already the canonical form (e.g. EKS, where ID
	// is the full cluster ARN).
	ResolutionID string
}

// SubCollectorResult holds the output of a single SubCollector.Collect call.
type SubCollectorResult struct {
	// Resources are the discovered cloud resources.
	Resources []ResourceSpec

	// Edges are the relationships between resources.
	Edges []EdgeSpec

	// Targets are follow-up collection targets for cascade discovery.
	Targets []CollectTarget
}
