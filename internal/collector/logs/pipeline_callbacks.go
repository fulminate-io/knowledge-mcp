// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// ResolvedResource pairs a cloud graph account with a node ID in that
// graph. CloudResolver implementations return it instead of a bare ID so
// downstream proxy creation targets the correct cross-graph address.
//
// Account names a specific cloud graph (GCP project id, GKE cluster
// name, AWS account id, ...) — the same string used as the on-disk
// graph file name and as ProxyTarget.Name when creating cross-graph
// proxies. ID is the node ID inside that graph.
type ResolvedResource struct {
	Account string
	ID      string
}

// CloudResolver resolves log-stream labels (service, namespace, etc.) to
// cloud graph resources. Implementations live outside logs/ — the
// caller (tools/, cloud/) wires up the resolver when constructing the
// Pipeline so the logs package stays free of cloud graph imports.
//
// Resolvers return (resolved, true) on a successful match and
// (zero, false) when no cloud resource corresponds to the supplied
// label value. The supplied stream provides label context
// (project_id, cluster_name, region, cloud_provider, ...) that
// implementations use to pick the right target graph.
//
// Implementations must be safe for concurrent use; the pipeline calls
// them from stream-assembly goroutines.
type CloudResolver interface {
	// ResolveService maps a service-label value on the given stream
	// (e.g., the value of a "service", "app", or "deployment" label)
	// to a cloud-graph resource.
	ResolveService(ctx context.Context, stream *wirelogs.LogStream, serviceName string) (ResolvedResource, bool)

	// ResolveNamespace maps a namespace-label value (Kubernetes
	// namespace, AWS account alias, etc.) to a cloud-graph resource.
	ResolveNamespace(ctx context.Context, stream *wirelogs.LogStream, namespace string) (ResolvedResource, bool)
}

// DependencyChecker validates structural relationships in the cloud graph.
// Correlation logic calls this to confirm that two log templates whose
// error bursts align in time also share a real cloud-graph dependency
// (direct edge or short-path reachability) before emitting a
// CORRELATES_WITH edge.
//
// The interface is defined in logs/ so the pipeline can depend on it
// without importing cloud/. Implementations live in tools/ or cloud/.
type DependencyChecker interface {
	// HasDependency reports whether resourceA has a dependency
	// relationship with resourceB. When a.Account == b.Account the
	// check walks within that single graph. When the accounts differ,
	// implementations may follow cross-graph proxy edges (e.g., a GKE
	// workload's RUNS_IN_CLUSTER edge to a cluster proxy resolving
	// into the parent GCP project graph) or fall back to the linkage
	// graph. Implementations that do not yet support cross-graph
	// traversal are permitted to return false for account mismatches.
	HasDependency(ctx context.Context, a, b ResolvedResource) bool
}

// PipelineOption is a functional option for NewPipeline. Callers compose
// options to tune chunking, cardinality detection, and cloud-graph
// callbacks without touching the Pipeline struct directly.
type PipelineOption func(*Pipeline)

// WithCloudResolver installs a CloudResolver. If unset, the pipeline
// skips cloud graph linkage — streams still form but no EMITTED_BY edges
// are emitted.
func WithCloudResolver(r CloudResolver) PipelineOption {
	return func(p *Pipeline) {
		p.cloudResolver = r
	}
}

// WithDependencyChecker installs a DependencyChecker. If unset, correlation
// emits edges based on temporal alignment alone; with it, correlation is
// additionally gated by cloud-graph dependency.
func WithDependencyChecker(c DependencyChecker) PipelineOption {
	return func(p *Pipeline) {
		p.dependencyCheck = c
	}
}
