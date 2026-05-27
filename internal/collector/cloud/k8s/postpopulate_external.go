// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// externalWorkloadTypes enumerates the resource_type values this
// resolver scans for external-endpoint env vars. Pod and every
// pod-template-bearing controller.
var externalWorkloadTypes = []string{
	"Pod",
	"Deployment",
	"StatefulSet",
	"DaemonSet",
	"ReplicaSet",
	"Job",
	"CronJob",
}

// resolveExternalConnections scans container env var values across
// every workload in the graph and emits CONNECTS_TO edges to
// cross-graph proxies of the external cloud services referenced by
// those env values.
//
// Security/safety:
//   - Only env entries with valueFrom == nil are scanned (literal
//     string values; Secret / ConfigMap refs are captured as separate
//     MOUNTS_SECRET / MOUNTS_CONFIGMAP edges by the subcollector).
//   - Raw env values are NEVER persisted. Only the matched URI
//     substring lands on edge evidence — the surrounding env value
//     may embed credentials even when its base endpoint is public.
//   - The input PodSpec is decoded from the already-collected resource
//     Content, NOT re-fetched from the cluster.
//
// Idempotency: proxy IDs are deterministic (CreateCrossGraphProxy
// upserts by ID) and LinkBatch dedupes edges by (from,to,type) so
// repeat runs don't accumulate duplicates.
func resolveExternalConnections(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	var allWorkloads []*knowledgev1.Node
	for _, rt := range externalWorkloadTypes {
		nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery(rt))
		if err != nil {
			return err
		}
		allWorkloads = append(allWorkloads, nodes...)
	}
	if len(allWorkloads) == 0 {
		return nil
	}

	proxies := newProxyAccumulator()
	var edges []knowledgev1.Edge
	for _, w := range allWorkloads {
		workloadEdges, err := buildExternalEdgesForWorkload(ctx, gc, graphName, w, proxies)
		if err != nil {
			return err
		}
		edges = append(edges, workloadEdges...)
	}
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create CONNECTS_TO edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created CONNECTS_TO edges", "count", len(edges))
	return nil
}

// buildExternalEdgesForWorkload decodes the workload's Content JSON,
// extracts each container's env[] + envFrom[], and emits one
// CONNECTS_TO edge per (env var × pattern match). Multiple matches per
// env value are allowed (a single URL can embed both an S3 bucket name
// in a path and a region hostname — each becomes its own edge).
//
// Two pass order — typed → heuristic:
//  1. Literal string env values (in-line spec.env[*].value) — no
//     resolution needed, just regex-scan.
//  2. Secret / ConfigMap refs (valueFrom, envFrom) — resolves the
//     referenced resource via the cloud graph, reads the decoded data
//     key, regex-scans that. Missing refs log Warn and skip.
//
// This mirrors the "typed evidence before heuristic evidence" ordering
// used by postpopulate.go (WorkloadIdentity → PV disk → external).
func buildExternalEdgesForWorkload(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	containers, ok := decodeContainersFromWorkload(w)
	if !ok || len(containers) == 0 {
		return nil, nil
	}

	// Dedupe by (workload-id → proxy-id) across containers + envvars +
	// refs so N containers (or a literal + a ref) referencing the same
	// URL yield a single edge.
	seen := make(map[string]struct{})
	var edges []knowledgev1.Edge

	// Pass 1: literal env values.
	for _, c := range containers {
		for _, env := range c.Env {
			if env.ValueFrom != nil {
				// valueFrom is handled by pass 2.
				continue
			}
			if env.Value == "" {
				continue
			}
			for _, target := range scanAllPatterns(env.Value) {
				next, err := emitConnectsTo(edges, w, c.Name, env.Name, target, seen, proxies)
				if err != nil {
					return nil, err
				}
				edges = next
			}
		}
	}

	// Pass 2: Secret / ConfigMap refs (valueFrom + envFrom).
	namespace := kgtypes.Value(w, "namespace")
	refEdges, err := buildExternalEdgesFromRefs(ctx, gc, graphName, w, namespace, containers, seen, proxies)
	if err != nil {
		return nil, err
	}
	edges = append(edges, refEdges...)

	return edges, nil
}

// emitConnectsTo creates the cross-graph proxy for target and appends
// the CONNECTS_TO edge with evidence to out, returning the (possibly
// unchanged) slice. Leaves out unchanged when the (workload, target)
// pair has already been emitted this pass (dedup across containers/env
// vars). Used by the literal-env path — the valueFrom / envFrom paths
// build their own evidence prefix and call emitConnectsToRaw directly.
func emitConnectsTo(out []knowledgev1.Edge, w *knowledgev1.Node, containerName, envName string, target externalTarget, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	return emitConnectsToRaw(out, w, "container="+containerName+" env="+envName, target, seen, proxies)
}

// emitConnectsToRaw is the shared edge emitter: upserts the proxy,
// dedupes by (workload,target) via seen, and appends the edge to out
// with an evidence string of the form "<prefix> pattern=<name>
// matched=<URI>". The edge is a fresh knowledgev1.Edge literal at the append
// site so the embedded proto lock is never copied.
//
// Security invariant: target.Matched is the substring the pattern
// regex matched — NOT the original value. Callers MUST NOT pass raw
// Secret/ConfigMap values in prefix; prefix is built from metadata
// (container name, env var name, ref name, key name) only.
func emitConnectsToRaw(out []knowledgev1.Edge, w *knowledgev1.Node, prefix string, target externalTarget, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	proxyID, err := buildExternalProxy(target, proxies)
	if err != nil {
		return out, err
	}
	key := w.Id + "→" + proxyID
	if _, dup := seen[key]; dup {
		return out, nil
	}
	seen[key] = struct{}{}

	evidence := prefix + " pattern=" + target.PatternName + " matched=" + target.Matched
	return append(out, knowledgev1.Edge{
		FromId:   w.Id,
		ToId:     proxyID,
		Type:     string(kgtypes.EdgeConnectsTo),
		Method:   target.PatternName,
		Evidence: evidence,
	}), nil
}

// upsertExternalProxy creates the cross-graph proxy for an
// externalTarget, dispatching to CreateCrossGraphProxy for accounts we
// know and upsertDanglingCloudProxy for accounts we don't (most
// dangling cases: RDS/ElastiCache hostnames, Azure endpoints, S3
// without account inference).
func buildExternalProxy(target externalTarget, proxies *proxyAccumulator) (string, error) {
	source := externalProxySource(target)
	if target.Account == "" {
		return addDanglingExternalProxy(target, source, proxies), nil
	}
	return proxies.proxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphCloud),
		Name:      target.Account,
		NodeId:    target.ID,
	}, source)
}

// externalProxySource builds display fields for the external proxy.
func externalProxySource(target externalTarget) *knowledgev1.Node {
	name := lastSegmentAfterSlash(target.ID)
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Summary:    target.ResourceType + " " + name,
	}
	kgtypes.SetValue(n, "resource_type", target.ResourceType)
	kgtypes.SetValue(n, "provider", target.Provider)
	return n
}

// upsertDanglingExternalProxy is analogous to upsertDanglingCloudProxy
// / upsertDanglingDiskProxy. The "proxy:cloud::<ID>" ID scheme keeps
// dangling proxies from colliding with future enriched versions.
func addDanglingExternalProxy(target externalTarget, source *knowledgev1.Node, proxies *proxyAccumulator) string {
	proxyID := "proxy:cloud::" + target.ID
	proxy := &knowledgev1.Node{
		Id:          proxyID,
		Type:        string(kgtypes.NodeProxy),
		SymbolName:  source.GetSymbolName(),
		Source:      "proxy:cloud:dangling",
		Description: source.GetDescription(),
	}
	kgtypes.SetValue(proxy, "foreign_graph", string(kgtypes.GraphCloud))
	kgtypes.SetValue(proxy, "foreign_id", target.ID)
	kgtypes.SetValue(proxy, "account", "")
	kgtypes.SetValue(proxy, "resource_type", target.ResourceType)
	kgtypes.SetValue(proxy, "provider", target.Provider)
	kgtypes.SetValue(proxy, "dangling", "true")
	proxies.byID[proxyID] = proxy
	return proxyID
}

// --- Content decoding ----------------------------------------------

// containerLite is the minimal shape we need from a container — name
// plus env (name, value, valueFrom-optional) plus envFrom (Secret /
// ConfigMap bulk refs). Using a local type keeps the resolver decoupled
// from k8s api version changes and avoids pulling the full PodSpec into
// this file.
type containerLite struct {
	Name    string        `json:"name"`
	Env     []envLite     `json:"env,omitempty"`
	EnvFrom []envFromLite `json:"envFrom,omitempty"`
}

// envLite mirrors corev1.EnvVar but only fields we read. ValueFrom is
// decoded as a structured pointer so we can (a) detect presence with
// `env.ValueFrom != nil` (preserves the original skip semantics) and
// (b) walk into SecretKeyRef / ConfigMapKeyRef for the URI scan without
// re-unmarshaling. Other EnvVarSource cases (fieldRef, resourceFieldRef)
// are intentionally omitted — they never carry cloud URIs.
type envLite struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *envValueFrom `json:"valueFrom,omitempty"`
}

// envValueFrom captures the two EnvVarSource variants that can
// reference user data (and therefore may hold cloud URIs). Both fields
// are pointers so the zero value means "this variant not set".
type envValueFrom struct {
	SecretKeyRef    *objectKeyRef `json:"secretKeyRef,omitempty"`
	ConfigMapKeyRef *objectKeyRef `json:"configMapKeyRef,omitempty"`
}

// objectKeyRef mirrors SecretKeySelector / ConfigMapKeySelector: both
// wire-compatible shapes consist of {name, key}. Optional flag is
// ignored — a missing resource is a warning regardless.
type objectKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// envFromLite mirrors corev1.EnvFromSource — Secret / ConfigMap bulk
// imports. Prefix is surfaced but not used in edge evidence; it maps
// data keys to env var names inside the pod, but the URI scan operates
// on the decoded VALUE bytes, not the prefixed name.
type envFromLite struct {
	Prefix       string         `json:"prefix,omitempty"`
	SecretRef    *objectNameRef `json:"secretRef,omitempty"`
	ConfigMapRef *objectNameRef `json:"configMapRef,omitempty"`
}

// objectNameRef mirrors SecretEnvSource / ConfigMapEnvSource: just a
// {name}.
type objectNameRef struct {
	Name string `json:"name"`
}

// workloadContent is the superset of shapes we decode across workload
// types. Each workload kind populates one of the paths (e.g. Pod uses
// spec.containers; Deployment/StatefulSet/DaemonSet/ReplicaSet/Job use
// spec.template.spec.containers; CronJob uses spec.jobTemplate.spec.
// template.spec.containers).
//
// The omitempty tags are intentionally OMITTED on nested struct fields
// — Go's encoding/json ignores them for non-pointer struct types, so
// declaring them just confuses linters (modernize) without any effect.
// We only decode this shape; we never marshal it, so empty-field
// suppression isn't useful here either way.
type workloadContent struct {
	Spec struct {
		Containers     []containerLite `json:"containers,omitempty"`
		InitContainers []containerLite `json:"initContainers,omitempty"`
		Template       struct {
			Spec struct {
				Containers     []containerLite `json:"containers,omitempty"`
				InitContainers []containerLite `json:"initContainers,omitempty"`
			} `json:"spec"`
		} `json:"template"`
		JobTemplate struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers     []containerLite `json:"containers,omitempty"`
						InitContainers []containerLite `json:"initContainers,omitempty"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
}

// decodeContainersFromWorkload returns every container (main +
// initContainers) from whichever path is populated in w.Content.
// Returns ok=false when Content is empty or fails to unmarshal —
// callers log and skip.
func decodeContainersFromWorkload(w *knowledgev1.Node) ([]containerLite, bool) {
	if len(w.Content) == 0 {
		return nil, false
	}
	var wc workloadContent
	if err := json.Unmarshal([]byte(w.Content), &wc); err != nil {
		return nil, false
	}

	// Pod.
	containers := append([]containerLite(nil), wc.Spec.Containers...)
	containers = append(containers, wc.Spec.InitContainers...)

	// Deployment / StatefulSet / DaemonSet / ReplicaSet / Job.
	containers = append(containers, wc.Spec.Template.Spec.Containers...)
	containers = append(containers, wc.Spec.Template.Spec.InitContainers...)

	// CronJob.
	containers = append(containers, wc.Spec.JobTemplate.Spec.Template.Spec.Containers...)
	containers = append(containers, wc.Spec.JobTemplate.Spec.Template.Spec.InitContainers...)

	return containers, true
}
