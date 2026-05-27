// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// serviceResourceTypePrefixes lists the resource_type prefixes considered
// "service-like" by ResolveService. A log-stream "service" label typically
// corresponds to one of these: an ECS service, a Kubernetes workload
// (Service, Deployment, StatefulSet, DaemonSet, Job, CronJob, bare Pod),
// a Lambda function, a Cloud Run service, etc.
//
// Order matters: the first match wins, so higher-priority types come first.
// "Service" outranks "Deployment" outranks bare "Pod" because a log label
// like service=api most naturally maps to the K8s Service that exposes
// it, falling back to its backing workload, and finally to an individual
// Pod if nothing else matches.
//
// Matching is case-sensitive HasPrefix against the cloud collector's
// canonical resource_type strings (PascalCase for K8s, colon-separated
// for cloud-provider types).
var serviceResourceTypePrefixes = []string{
	"ecs:service",
	"ecs-service",
	"lambda:function",
	"lambda-function",
	"cloudrun:service",
	"cloud-run:service",
	"k8s:Service",
	"Service",
	"Deployment",
	"StatefulSet",
	"DaemonSet",
	"ReplicaSet",
	"Job",
	"CronJob",
	"Pod",
	"gce:instance-group", // GCP managed instance groups back services
	"appengine:service",
}

// namespaceResourceTypePrefixes lists the resource_type prefixes considered
// "namespace-like" by ResolveNamespace. Kubernetes Namespaces are the
// canonical match; AWS/GCP project-scoping resources are secondary.
var namespaceResourceTypePrefixes = []string{
	"k8s:Namespace",
	"Namespace",
	"gcp:project",
	"aws:account",
	"ec2:vpc",
	"vpc",
}

// controllerResourceTypePrefixes matches ONLY K8s controller workloads —
// the non-ephemeral types that own Pods. Used by ResolveService's
// pod-name fallback when the service label doesn't match the owning
// workload name directly. Pods are intentionally excluded: they are
// ephemeral (routinely evicted, rescheduled, and replaced) so
// correlation that hangs on a specific Pod ID becomes non-deterministic
// as soon as the graph is re-collected. Resolving to the stable
// controller (DaemonSet, Deployment, etc.) keeps correlation robust
// across pod churn, and the controller already carries the equivalent
// RUNS_IN_CLUSTER / IN_NAMESPACE / CONNECTS_TO edges needed for BFS.
var controllerResourceTypePrefixes = []string{
	"Deployment",
	"StatefulSet",
	"DaemonSet",
	"ReplicaSet",
	"Job",
	"CronJob",
}

// podResourceTypePrefixes matches only K8s Pod resources. Used as the
// final fallback in ResolveService when no controller match is found —
// the Pod acts as a leaf identity of last resort.
var podResourceTypePrefixes = []string{
	"Pod",
}

// gkePrefix is the leading token GCP uses when naming GKE cluster
// graphs (e.g., "gke_my-project_us-central1_cluster-1"). Surfacing it
// as a constant keeps gkeGraphMatches readable and prevents ad-hoc
// substring checks elsewhere.
const gkePrefix = "gke_"

// cloudResolver implements logs.CloudResolver by querying every loaded
// cloud graph through an in-memory CloudSubgraph slice (produced by
// IngestService.FetchCloudSubgraph). The resolver is no longer bound to
// a single account at construction time — it inspects each candidate
// stream's labels (project_id, cluster_name, ...) to pick the right
// target graph(s) at resolution time.
//
// Safe for concurrent use: CloudSubgraph is read-only after
// construction and the resolver holds no mutable state of its own.
type cloudResolver struct {
	subgraph *CloudSubgraph
}

// Compile-time check that cloudResolver satisfies the logs.CloudResolver
// contract. Keeping this next to the type guarantees renames or signature
// changes in logs/ break at build time rather than at runtime.
var _ logs.CloudResolver = (*cloudResolver)(nil)

// NewCloudResolver returns a logs.CloudResolver backed by the supplied
// in-memory subgraph. The resolver auto-discovers candidate cloud
// graphs from each stream's labels — no account parameter is required.
// A nil sg yields a resolver that always reports a miss but does not
// panic.
func NewCloudResolver(sg *CloudSubgraph) logs.CloudResolver {
	return &cloudResolver{subgraph: sg}
}

// ResolveService maps a service-name label value (e.g., the value of a
// "service" or "app" label on a log stream) to a cloud-graph resource
// by matching SymbolName exactly (case-insensitive) against any cloud
// resource whose resource_type is service-like. Candidate cloud graphs
// are derived from the stream's labels — see candidateGraphs.
//
// Fallback chain when the service name doesn't match (common for K8s
// workloads whose container_name differs from their owning workload —
// e.g., GKE renames Cilium's container to "cilium-agent" but the
// DaemonSet is "anetd"):
//
//  1. Controller derivation: take the stream's pod_name and strip
//     trailing "-{hash}" segments to recover the controller name
//     ("anetd-s59lq" → "anetd"; "api-5d7b8c-xyz42" → "api-5d7b8c" →
//     "api"). Match each candidate against controller types only
//     (Deployment / StatefulSet / DaemonSet / ReplicaSet / Job /
//     CronJob). Controllers are stable across pod churn, so this is the
//     resolution that holds up across graph re-collections.
//  2. Pod fallback: if no controller matches any candidate, try the
//     full pod_name against Pod as a last resort. Expected to miss
//     often (pods are ephemeral); succeeds only when the exact pod
//     happens to still exist in the graph.
func (r *cloudResolver) ResolveService(
	ctx context.Context, stream *logwire.LogStream, serviceName string,
) (logs.ResolvedResource, bool) {
	if res, ok := r.resolveByTypePrefixes(stream, serviceName, serviceResourceTypePrefixes); ok {
		return res, true
	}
	podName := streamLabel(stream, "pod_name")
	if podName == "" {
		return logs.ResolvedResource{}, false
	}
	// Controller derivation: try the full pod name first (handles the
	// rare case where a pod name is also a controller name), then each
	// progressively stripped variant. Controllers rank better than Pod
	// in pickBestResource's priority order, so the stable identity wins.
	for _, name := range controllerNameCandidates(podName) {
		if res, ok := r.resolveByTypePrefixes(stream, name, controllerResourceTypePrefixes); ok {
			return res, true
		}
	}
	// Final fallback: match the concrete Pod if it still exists in the
	// graph. Only reached when no controller candidate resolved.
	return r.resolveByTypePrefixes(stream, podName, podResourceTypePrefixes)
}

// controllerNameCandidates returns the pod name followed by
// progressively stripped prefixes, representing K8s controller-naming
// conventions:
//
//   - DaemonSet:   "{controller}-{nodehash}"           → strip 1 segment
//   - StatefulSet: "{controller}-{ordinal}"            → strip 1 segment
//   - Job:         "{controller}-{random}"             → strip 1 segment
//   - Deployment:  "{controller}-{rshash}-{podhash}"   → strip 2 segments
//   - CronJob:     "{controller}-{timestamp}-{random}" → strip 2 segments
//
// We stop at 2 strips (covers Deployment/CronJob) and return at most 3
// candidates. The full pod name comes first so a controller that
// happens to share its pod's name still matches at the right rank;
// shorter candidates are tried later so a Deployment named "api" isn't
// accidentally matched when the real workload is "api-canary".
// Names with no hyphen yield only the full name (nothing to strip).
func controllerNameCandidates(podName string) []string {
	out := []string{podName}
	for range 2 {
		idx := strings.LastIndex(out[len(out)-1], "-")
		if idx <= 0 {
			break
		}
		stripped := out[len(out)-1][:idx]
		if stripped == "" {
			break
		}
		out = append(out, stripped)
	}
	return out
}

// ResolveNamespace maps a namespace-label value to a cloud-graph
// resource by matching SymbolName exactly (case-insensitive) against
// any cloud resource whose resource_type is namespace-like. Candidate
// cloud graphs are derived from the stream's labels.
func (r *cloudResolver) ResolveNamespace(
	ctx context.Context, stream *logwire.LogStream, namespace string,
) (logs.ResolvedResource, bool) {
	return r.resolveByTypePrefixes(stream, namespace, namespaceResourceTypePrefixes)
}

// resolveByTypePrefixes is the shared lookup body for ResolveService /
// ResolveNamespace. It walks the candidate cloud graphs in priority
// order (GKE clusters before parent project graphs before
// fall-back-to-all) and returns the first node whose SymbolName matches
// name (case-insensitive) AND whose resource_type begins with any of
// the allowed prefixes.
//
// When multiple candidates match within a graph, pickBestResource
// prefers the one whose resource_type matches the earliest prefix in
// the allowed list. Cross-graph priority is enforced here: the first
// candidate graph that yields any match wins.
func (r *cloudResolver) resolveByTypePrefixes(
	stream *logwire.LogStream,
	name string,
	allowedPrefixes []string,
) (logs.ResolvedResource, bool) {
	if name == "" || r.subgraph == nil {
		return logs.ResolvedResource{}, false
	}
	for _, account := range r.candidateGraphs(stream) {
		// Type-filter to NodeCloudResource only. The in-memory
		// CloudSubgraph.Nodes(account) returns BOTH NodeCloudResource
		// and NodeProxy entries (proxies are stored alongside their
		// origin graph's resources for cross-graph BFS). The original
		// server-side resolver used store.Match(NodeCloudResource).Limit(0)
		// which the in-memory adapter must replicate by hand —
		// otherwise a NodeProxy with matching name+resource_type would
		// shadow the underlying CloudResource. See
		// domains/store/proxy.go:168-171 — production
		// CreateCrossGraphProxy GraphCloud branch copies
		// resource_type/region/provider onto cloud proxies, so a proxy
		// can pass nameMatches + prefixRank for the wrong account.
		all := r.subgraph.Nodes(account)
		nodes := make([]*knowledgev1.Node, 0, len(all))
		for i := range all {
			if kgtypes.NodeType(all[i].Type) == kgtypes.NodeCloudResource {
				nodes = append(nodes, all[i])
			}
		}
		if id, ok := pickBestResource(nodes, name, allowedPrefixes); ok {
			return logs.ResolvedResource{Account: account, ID: id}, true
		}
	}
	return logs.ResolvedResource{}, false
}

// candidateGraphs returns the cloud graph names to search, in priority
// order, for a resolution against the given stream.
//
// The ordering rules (most-specific first) are:
//
//  1. GKE cluster graphs matching prefix "gke_{project_id}_" AND
//     suffix "_{cluster_name}" — the workload-bearing graph for a
//     GKE-hosted stream.
//  2. The bare {project_id} graph — the parent GCP project that owns
//     the cluster.
//  3. Every other loaded cloud graph — fall-back so non-GCP streams
//     (AWS, Azure, on-prem) still resolve when the operator has only
//     one cloud graph loaded.
//
// Each graph appears at most once in the returned slice.
func (r *cloudResolver) candidateGraphs(stream *logwire.LogStream) []string {
	all := r.subgraph.GraphNames()
	if len(all) == 0 {
		return nil
	}

	projectID, clusterName := streamProjectAndCluster(stream)
	if projectID == "" && clusterName == "" {
		return all
	}

	seen := make(map[string]struct{}, len(all))
	out := make([]string, 0, len(all))
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}

	if projectID != "" && clusterName != "" {
		for _, name := range all {
			if gkeGraphMatches(name, projectID, clusterName) {
				add(name)
			}
		}
	}
	if projectID != "" {
		for _, name := range all {
			if name == projectID {
				add(name)
			}
		}
	}
	for _, name := range all {
		add(name)
	}
	return out
}

// streamLabel reads a single label value from a stream, checking both
// LowCardLabels and Labels so the caller doesn't have to worry about
// which tier the label landed in. Returns "" when the key is absent or
// the stream is nil.
func streamLabel(stream *logwire.LogStream, key string) string {
	if stream == nil {
		return ""
	}
	if v := stream.LowCardLabels[key]; v != "" {
		return v
	}
	return stream.Labels[key]
}

// streamProjectAndCluster pulls the GCP project_id and cluster_name
// labels off a stream when present. Either may be empty; both empty is
// the common case for AWS/Azure/on-prem streams. The matched keys are
// the GCP / GKE conventional label names ("project_id",
// "cluster_name") — no provider auto-detection is performed.
func streamProjectAndCluster(stream *logwire.LogStream) (projectID, clusterName string) {
	if stream == nil {
		return "", ""
	}
	for _, src := range []map[string]string{stream.LowCardLabels, stream.Labels} {
		if v := src["project_id"]; v != "" && projectID == "" {
			projectID = v
		}
		if v := src["cluster_name"]; v != "" && clusterName == "" {
			clusterName = v
		}
	}
	return projectID, clusterName
}

// gkeGraphMatches reports whether graphName is a GKE cluster graph that
// matches the given project + cluster pair. The convention is
// "gke_{project_id}_{region}_{cluster_name}" — region is unknown at
// resolution time so the check matches prefix "gke_{project_id}_" AND
// suffix "_{cluster_name}".
func gkeGraphMatches(graphName, projectID, clusterName string) bool {
	if projectID == "" || clusterName == "" {
		return false
	}
	prefix := gkePrefix + projectID + "_"
	suffix := "_" + clusterName
	if !strings.HasPrefix(graphName, prefix) {
		return false
	}
	if !strings.HasSuffix(graphName, suffix) {
		return false
	}
	// Require at least one character (the region) between prefix and
	// suffix so we don't accidentally match "gke_{project}__{cluster}".
	return len(graphName) > len(prefix)+len(suffix)
}

// pickBestResource selects the highest-priority cloud resource node whose
// SymbolName matches name (case-insensitive) and whose resource_type is
// allowed by one of the prefixes. "Priority" is the index in allowedPrefixes
// (lower = better), so the caller controls the precedence order.
func pickBestResource(
	nodes []*knowledgev1.Node,
	name string,
	allowedPrefixes []string,
) (string, bool) {
	lowered := strings.ToLower(name)
	bestID := ""
	bestRank := len(allowedPrefixes) // worse than any legal rank
	for _, n := range nodes {
		if !nameMatches(n.SymbolName, lowered) {
			continue
		}
		rank, ok := prefixRank(kgtypes.Value(n, "resource_type"), allowedPrefixes)
		if !ok {
			continue
		}
		if rank < bestRank {
			bestRank = rank
			bestID = n.Id
		}
	}
	if bestID == "" {
		return "", false
	}
	return bestID, true
}

// nameMatches reports whether a cloud-resource SymbolName corresponds to the
// caller-supplied label value. Comparison is case-insensitive because label
// values are produced by heterogeneous log producers whose casing is
// inconsistent (Helm charts, ECS task definitions, Lambda function names).
func nameMatches(symbolName, loweredName string) bool {
	if symbolName == "" {
		return false
	}
	return strings.EqualFold(symbolName, loweredName)
}

// prefixRank returns the index of the first prefix in allowed that matches
// resourceType along with ok=true. When no prefix matches, ok=false and the
// returned index is meaningless. Matching is case-sensitive because
// resource_type values are produced by the cloud collector and are stable.
//
// Matching requires a word-boundary after the prefix: either the strings
// are equal, or the character following the prefix is one of ":", "-", "/"
// (the separators the cloud collectors use in resource_type values). This
// prevents partial-word collisions — e.g., allowed="Service" must NOT
// match resourceType="ServiceAccount" because those are distinct K8s
// kinds the caller's priority ordering depends on.
func prefixRank(resourceType string, allowed []string) (int, bool) {
	if resourceType == "" {
		return 0, false
	}
	for i, p := range allowed {
		if !strings.HasPrefix(resourceType, p) {
			continue
		}
		if len(resourceType) == len(p) {
			return i, true
		}
		switch resourceType[len(p)] {
		case ':', '-', '/':
			return i, true
		}
	}
	return 0, false
}
