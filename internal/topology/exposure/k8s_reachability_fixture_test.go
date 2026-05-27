// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_fixture_test.go provides a focused, method-based fixture
// builder for the Phase 5 K8s NetworkPolicy recipe tests. It wraps the lower-
// level cloudFixture with K8s-shaped helpers so each recipe test reads as a
// declarative graph spec instead of a pile of raw AddCloudResource / Link
// calls.
//
// DESIGN NOTES
//
// The fixture intentionally mimics what cloud/k8s/postpopulate.go emits
// AFTER collector run: directional EdgeAllowsIngressFrom / EdgeAllowsEgressTo
// edges with (protocol, port) metadata already attached, EdgeRestrictsIngress
// / EdgeRestrictsEgress default-deny selectors, and EdgeSelects from Services
// to their backing pods. topology/ must not import cloud/k8s/ (layering rule),
// so the fixture emits these edges directly instead of re-running the
// collector's podSelector / namespaceSelector / ipBlock resolution logic.
//
// DEVIATION FROM THE PHASE 5 PLAN SIGNATURE
//
// The plan specifies AddNetworkPolicy(namespace, name, selector, ingressRules,
// egressRules...) which would take rule structs. The current analyzer doesn't
// consult any of those — it walks generic edges — so implementing a rule-struct
// API would force the fixture to reimplement the collector's selector logic
// just to spit out the same directional edges. Instead AddNetworkPolicy creates
// only the policy node; RestrictIngress / RestrictEgress wire the default-deny
// edges, and AllowIngress / AllowEgress wire the directional allow edges with
// port metadata. Each recipe test composes those primitives to express its
// recipe. This matches the abstraction the analyzer actually consumes.
//
// NAMESPACE NODES
//
// The analyzer reads pod namespace via the per-pod `namespace` metadata and
// does NOT walk Namespace nodes or namespace labels (Phase 5 does not exercise
// namespaceSelector resolution at the analyzer layer — that happens in the
// collector). AddNamespace creates a Namespace cloud-resource node with
// labels so future cross-ns classifiers that walk them keep working, but the
// Phase 5 recipes never assert against those labels directly.
//
// CONTAINER PORTS
//
// Named port resolution also happens at collector time (resolving port name
// "web" → container port 8080 by reading the pod spec). For fixture purposes
// AddContainerPort records the name→port mapping in a helper-side lookup table
// that AllowIngress / AllowEgress can consult via the NamedPort field. When a
// named port is set without a matching container port registration, the
// emitted edge carries port_unresolved=true to match the collector contract.

// k8sFixture is the method-based K8s graph builder for the Phase 5 recipe
// tests. Wraps a cloudFixture rooted at a single account and tracks
// per-pod (name → port) container port registrations so named-port recipes
// can resolve to numeric ports at edge-emit time.
type k8sFixture struct {
	t       *testing.T
	cloud   *cloudFixture
	account string
	// containerPorts maps pod ID → (port name → numeric port). AddContainerPort
	// populates this map; AllowIngress/AllowEgress consult it when the caller
	// passes a NamedPort without a numeric PortFrom.
	containerPorts map[string]map[string]int
}

// k8sFixtureAccount is the account name every k8sFixture uses. Tests that
// want explicit cross-account behavior should fall back to cloudFixture
// directly — the K8s fixture is single-account by design because K8s
// reachability is cluster-scoped.
const k8sFixtureAccount = "cluster-test"

// buildK8sFixture returns a fresh k8sFixture backed by a scripted caller.
func buildK8sFixture(t *testing.T) *k8sFixture {
	t.Helper()
	cloud := newCloudFixture(t)
	cloud.account(k8sFixtureAccount)
	return &k8sFixture{
		t:              t,
		cloud:          cloud,
		account:        k8sFixtureAccount,
		containerPorts: map[string]map[string]int{},
	}
}

// reader returns the cloudReader for the fixture's account — what the index
// builder and analyzers read through.
func (f *k8sFixture) reader() *cloudReader { return f.cloud.reader(f.account) }

// req returns the foundation.Request analyzers receive (graph=cloud, the
// fixture's account).
func (f *k8sFixture) req() Request { return f.cloud.cloudReq(f.account, 0) }

// Account returns the single account name backing this fixture. Recipe tests
// use this when constructing the Request they pass to Run.
func (f *k8sFixture) Account() string { return f.account }

// AddPod creates a Pod cloud-resource node in the given namespace with the
// given labels. Returns the pod node ID so callers can wire edges against
// it. Labels are stored as "label.<key>" metadata keys — they are not read
// by the current analyzer but are kept on the node so future classifiers
// can walk them.
func (f *k8sFixture) AddPod(namespace, name string, labels map[string]string) string {
	f.t.Helper()
	id := namespace + "/Pod/" + name
	meta := map[string]string{"namespace": namespace}
	for k, v := range labels {
		meta["label."+k] = v
	}
	node := f.cloud.AddCloudResource(f.account, id, name, "Pod", meta)
	return node.Id
}

// AddNamespace creates a Namespace cloud-resource node with labels. The
// current analyzer doesn't read Namespace nodes, so this is a structural
// placeholder — it just ensures the graph shape matches what the collector
// would emit so downstream classifiers that start walking namespace labels
// have a node to find. The namespace node's ID is "<name>/Namespace/<name>"
// to match the convention used by other cloud collectors.
func (f *k8sFixture) AddNamespace(name string, labels map[string]string) string {
	f.t.Helper()
	id := name + "/Namespace/" + name
	meta := map[string]string{"namespace": name}
	for k, v := range labels {
		meta["label."+k] = v
	}
	node := f.cloud.AddCloudResource(f.account, id, name, "Namespace", meta)
	return node.Id
}

// AddNetworkPolicy creates a NetworkPolicy cloud-resource node in the given
// namespace and returns its ID. Use RestrictIngress / RestrictEgress to wire
// default-deny selector edges and AllowIngress / AllowEgress to wire
// directional allow edges.
func (f *k8sFixture) AddNetworkPolicy(namespace, name string) string {
	f.t.Helper()
	return f.AddNetworkPolicyWithContent(namespace, name, "")
}

// AddNetworkPolicyWithContent creates a NetworkPolicy node with the given
// JSON content. Used by the ipBlock recipe so findWorldExposedPods can
// re-parse the spec.ingress[].from[].ipBlock.cidr path.
func (f *k8sFixture) AddNetworkPolicyWithContent(namespace, name, content string) string {
	f.t.Helper()
	id := namespace + "/NetworkPolicy/" + name
	meta := map[string]string{"namespace": namespace}
	node := f.cloud.AddCloudResource(f.account, id, name, "NetworkPolicy", meta)
	if content != "" {
		f.cloud.setNodeContent(f.account, id, content)
	}
	return node.Id
}

// RestrictIngress wires an EdgeRestrictsIngress edge from policy → pod,
// marking the pod as default-deny ingress (the analyzer sets
// podInfo.IngressRestricted when at least one such edge exists).
func (f *k8sFixture) RestrictIngress(policyID, podID string) {
	f.t.Helper()
	f.cloud.AddEdge(f.account, policyID, podID, kgtypes.EdgeRestrictsIngress)
}

// RestrictEgress wires an EdgeRestrictsEgress edge from policy → pod, marking
// the pod as default-deny egress.
func (f *k8sFixture) RestrictEgress(policyID, podID string) {
	f.t.Helper()
	f.cloud.AddEdge(f.account, policyID, podID, kgtypes.EdgeRestrictsEgress)
}

// AllowIngress wires an EdgeAllowsIngressFrom edge — dstPod → srcPod, "dst
// accepts ingress from src" — with (protocol, port) metadata serialized into
// the edge's Evidence field. When meta.NamedPort is non-empty, the fixture
// looks up the resolved numeric port on dstPod's container port table; a
// miss emits port_unresolved=true to match the collector contract.
func (f *k8sFixture) AllowIngress(dstPodID, srcPodID string, meta edgePortMetadata) {
	f.t.Helper()
	meta = f.resolveNamedPort(dstPodID, meta)
	f.writeAllowEdge(dstPodID, srcPodID, kgtypes.EdgeAllowsIngressFrom, meta)
}

// AllowEgress wires an EdgeAllowsEgressTo edge — srcPod → dstPod — with
// (protocol, port) metadata attached. Named port resolution consults the
// dst pod's container port table, matching K8s semantics.
func (f *k8sFixture) AllowEgress(srcPodID, dstPodID string, meta edgePortMetadata) {
	f.t.Helper()
	meta = f.resolveNamedPort(dstPodID, meta)
	f.writeAllowEdge(srcPodID, dstPodID, kgtypes.EdgeAllowsEgressTo, meta)
}

// resolveNamedPort walks the fixture's per-pod container port table and
// rewrites meta.PortFrom/PortTo when a named port entry exists. Unresolved
// named ports are preserved with port_unresolved=true so the analyzer
// surfaces protocol-only matches.
func (f *k8sFixture) resolveNamedPort(ownerPodID string, meta edgePortMetadata) edgePortMetadata {
	if meta.NamedPort == "" {
		return meta
	}
	ports, ok := f.containerPorts[ownerPodID]
	if !ok {
		meta.PortUnresolved = true
		return meta
	}
	port, ok := ports[meta.NamedPort]
	if !ok {
		meta.PortUnresolved = true
		return meta
	}
	meta.PortFrom = port
	meta.PortTo = port
	return meta
}

// writeAllowEdge marshals the given metadata into the edge Evidence field
// and writes a single allow edge. Zero-valued metadata is stored as an
// empty Evidence string so parseEdgePortRange treats it as the fully-open
// signal the collector emits when no ports[] clause exists.
func (f *k8sFixture) writeAllowEdge(fromID, toID string, edgeType kgtypes.EdgeType, meta edgePortMetadata) {
	var evidence string
	if meta != (edgePortMetadata{}) {
		b, err := json.Marshal(meta)
		require.NoError(f.t, err)
		evidence = string(b)
	}
	f.cloud.AddEdgeWithEvidence(f.account, fromID, toID, edgeType, evidence)
}

// AddService creates a Service cloud-resource node in the given namespace
// and wires EdgeSelects edges from the service to each backing pod ID.
// Returns the service node ID. Matches the collector's "Service selects
// its backing pods via label match" shape.
func (f *k8sFixture) AddService(namespace, name string, backingPods []string) string {
	f.t.Helper()
	id := namespace + "/Service/" + name
	meta := map[string]string{"namespace": namespace}
	node := f.cloud.AddCloudResource(f.account, id, name, "Service", meta)
	for _, podID := range backingPods {
		f.cloud.AddEdge(f.account, id, podID, kgtypes.EdgeSelects)
	}
	return node.Id
}

// AddContainerPort registers a named container port on a pod so AllowIngress
// / AllowEgress can resolve NamedPort references to numeric ports at edge
// emission time. Multiple calls stack; later entries overwrite earlier ones
// with the same name (matching K8s spec behavior where the last matching
// container wins).
func (f *k8sFixture) AddContainerPort(podID, name string, port int) {
	f.t.Helper()
	m, ok := f.containerPorts[podID]
	if !ok {
		m = map[string]int{}
		f.containerPorts[podID] = m
	}
	m[name] = port
}

// TestK8sFixture_Smoke is the Phase 5 Step 1 smoke test. It exercises every
// helper on the k8sFixture and asserts the resulting graph shape is what
// buildReachabilityIndex expects to walk. No recipe assertions run here —
// see k8s_reachability_recipe_test.go for the full recipe set.
func TestK8sFixture_Smoke(t *testing.T) {
	fx := buildK8sFixture(t)

	fx.AddNamespace("default", map[string]string{"env": "test"})
	web := fx.AddPod("default", "web", map[string]string{"app": "web"})
	api := fx.AddPod("default", "api", map[string]string{"app": "api"})

	fx.AddContainerPort(api, "http", 8080)

	policy := fx.AddNetworkPolicy("default", "deny-all")
	fx.RestrictIngress(policy, web)
	fx.RestrictIngress(policy, api)

	// Allow web → api on TCP/http (resolves to 8080 via AddContainerPort).
	fx.AllowIngress(api, web, edgePortMetadata{Protocol: "TCP", NamedPort: "http"})

	svc := fx.AddService("default", "api-svc", []string{api})

	// Build the reachability index and assert the graph shape the analyzer
	// will see. This is the smoke test's whole purpose.
	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.False(t, idx.skipped)
	require.Equal(t, 2, idx.podCount)
	require.NotNil(t, idx.pods[web])
	require.NotNil(t, idx.pods[api])
	require.True(t, idx.pods[web].IngressRestricted)
	require.True(t, idx.pods[api].IngressRestricted)
	require.NotNil(t, idx.services[svc])
	require.Equal(t, []string{api}, idx.services[svc].BackingPods)

	// Named port resolution: the allow edge should carry TCP/8080 on api's
	// ingress allow map for web.
	ranges := idx.pods[api].AllowedIngressFrom[web]
	require.Len(t, ranges, 1)
	require.Equal(t, "TCP", ranges[0].Protocol)
	require.Equal(t, 8080, ranges[0].PortFrom)
	require.Equal(t, 8080, ranges[0].PortTo)

	// canReach must honor the resolved named port.
	require.True(t, idx.canReach(web, api, "TCP", 8080),
		"web → api on resolved named port TCP/8080 must succeed")
	require.False(t, idx.canReach(web, api, "TCP", 8081),
		"web → api on an unmapped port must be blocked by default-deny")
}
