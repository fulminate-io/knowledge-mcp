// SPDX-License-Identifier: Apache-2.0

package cicd

import "github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

// ResourceSpec describes a CI/CD resource discovered by a SubCollector.
// Fields map directly to *knowledgev1.Node fields via BuildNode. SDK-specific
// types never appear here -- this is the boundary between provider APIs and the
// knowledge graph.
type ResourceSpec struct {
	// ID is the provider-specific resource identifier
	// (e.g. "github:anthropics:workflow:deploy.yml").
	ID string

	// Name is the human-readable display name.
	Name string

	// ResourceType classifies the resource
	// (e.g. "workflow", "pipeline", "runner", "environment", "secret").
	ResourceType string

	// Provider is the CI/CD platform (e.g. "github", "gitlab", "bitbucket").
	Provider string

	// Content is the raw API JSON from the provider response, preserved
	// as-is for full fidelity.
	Content []byte

	// Metadata holds additional key-value pairs (org, repo, labels, etc.).
	Metadata map[string]string
}

// EdgeSpec describes a directed relationship from one CI/CD resource to another.
// The runner converts these to kgwire.BatchEdge via BuildEdge.
type EdgeSpec struct {
	// SourceID is the provider-specific ID of the resource that declares
	// this edge.
	SourceID string

	// TargetID is the provider-specific ID of the target resource.
	TargetID string

	// Relationship is the edge type from kgtypes/edge_types.go
	// (e.g. kgtypes.EdgeDeploysTo, kgtypes.EdgeRunsIn, kgtypes.EdgeUsesSecret).
	Relationship kgtypes.EdgeType

	// Metadata holds optional key-value pairs describing how this edge was
	// established (e.g. trigger_type, approval_count). BuildEdge serializes
	// non-empty Metadata as JSON into BatchEdge.Evidence and sets Method to
	// "cicd-collect". Nil or empty Metadata leaves Evidence and Method empty
	// for backward compatibility.
	Metadata map[string]string
}

// SubCollectorResult holds the output of a single SubCollector.Collect call.
type SubCollectorResult struct {
	// Resources are the discovered CI/CD resources.
	Resources []ResourceSpec

	// Edges are the relationships between resources.
	Edges []EdgeSpec
}
