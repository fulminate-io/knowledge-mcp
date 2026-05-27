// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_test.go covers KubernetesReachabilityAnalyzer registration,
// Run-time graph type validation, and buildReachabilityIndex + canReach
// behavior including the hard pod cap and the (protocol, port) range
// intersection contract Phase 2.5 introduces.

const k8sReachabilityAcct = "cluster-prod"

// TestKubernetesReachabilityAnalyzer_Name pins the analyzer name. Findings
// carry this in their Algorithm field, so renaming it would silently break
// dedup.
func TestKubernetesReachabilityAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "k8s_reachability", KubernetesReachabilityAnalyzer{}.Name())
}

// TestKubernetesReachabilityAnalyzer_RegisteredAtInit confirms the analyzer
// self-registered with the topology registry at init() time so the dream
// topology phase (and every other caller that looks analyzers up by name)
// can find it without importing k8s_reachability.go directly.
func TestKubernetesReachabilityAnalyzer_RegisteredAtInit(t *testing.T) {
	a, ok := Get("k8s_reachability")
	require.True(t, ok, "k8s_reachability analyzer must be registered at init time")
	require.NotNil(t, a)
	assert.Equal(t, "k8s_reachability", a.Name())
}

// TestKubernetesReachabilityAnalyzer_WrongGraphType verifies Run rejects any
// request whose Graph is not kgtypes.GraphCloud. Reachability analysis is
// cloud-specific and the analyzer must fail loudly rather than produce
// nonsense findings when pointed at the wrong graph.
func TestKubernetesReachabilityAnalyzer_WrongGraphType(t *testing.T) {
	fx := newCloudFixture(t)
	_, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphKnowledge,
		Name:   k8sReachabilityAcct,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GraphCloud")
}

// TestKubernetesReachabilityAnalyzer_NilCaller returns an error rather than
// panicking when req.Caller is nil. Matches the convention in orphan.go (the
// wire-fetch analyzers reject a missing GraphCaller the way the store-backed
// analyzers rejected a missing req.DB).
func TestKubernetesReachabilityAnalyzer_NilCaller(t *testing.T) {
	_, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Graph: kgtypes.GraphCloud,
		Name:  k8sReachabilityAcct,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Caller")
}

// addPod creates a Pod cloud-resource node in the k8sReachabilityAcct account
// with the given namespace metadata and returns its ID. Kept small so tests
// read as graph shape declarations. The account is hardcoded because every
// caller uses the same cluster fixture.
func addPod(fx *cloudFixture, id, ns string) string {
	meta := map[string]string{"namespace": ns}
	n := fx.AddCloudResource(k8sReachabilityAcct, id, id, "Pod", meta)
	return n.Id
}

// addNetworkPolicy creates a NetworkPolicy cloud-resource node in the
// k8sReachabilityAcct account with the given namespace metadata and returns
// its ID.
func addNetworkPolicy(fx *cloudFixture, id, ns string) string {
	meta := map[string]string{"namespace": ns}
	n := fx.AddCloudResource(k8sReachabilityAcct, id, id, "NetworkPolicy", meta)
	return n.Id
}

// addAllowEdge creates an EdgeAllowsIngressFrom reachability edge with the
// given port metadata attached to Edge.Evidence (matching the schema Phase 2
// emits). LinkBatch is used directly so the Evidence field survives the
// write — store.Link only takes (from, to, type). The edge type is hardcoded
// because every caller uses EdgeAllowsIngressFrom; if an egress variant
// lands, reintroduce the parameter.
func addAllowEdge(t *testing.T, fx *cloudFixture, from, to string, meta edgePortMetadata) {
	t.Helper()
	var evidence string
	if meta.Protocol != "" || meta.PortFrom != 0 || meta.PortTo != 0 || meta.NamedPort != "" || meta.PortUnresolved {
		b, err := json.Marshal(meta)
		require.NoError(t, err)
		evidence = string(b)
	}
	fx.AddEdgeWithEvidence(k8sReachabilityAcct, from, to, kgtypes.EdgeAllowsIngressFrom, evidence)
}

// addRestrictsEdge wires a NetworkPolicy → pod restricts edge. These edges
// carry no port metadata (they exist only to flag default-deny) so the
// helper is a thin wrapper over Link.
func addRestrictsEdge(t *testing.T, fx *cloudFixture, policyID, podID string, edgeType kgtypes.EdgeType) {
	t.Helper()
	fx.AddEdge(k8sReachabilityAcct, policyID, podID, edgeType)
}

// TestBuildReachabilityIndex_DenyAllAllowSpecific builds the canonical
// "deny-all ingress + allow-specific" recipe and asserts canReach returns
// true for the permitted edge and false for everything else. This is the
// fixture Phase 3 Step 2 asserts against per the criterion description.
func TestBuildReachabilityIndex_DenyAllAllowSpecific(t *testing.T) {
	fx := newCloudFixture(t)

	web := addPod(fx, "default/Pod/web", "default")
	api := addPod(fx, "default/Pod/api", "default")
	db := addPod(fx, "default/Pod/db", "default")

	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	allowWebAPI := addNetworkPolicy(fx, "default/NetworkPolicy/allow-web-api", "default")

	// deny-all selects every pod for ingress restrictions (default-deny).
	addRestrictsEdge(t, fx, denyAll, web, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, db, kgtypes.EdgeRestrictsIngress)

	// allow-web-api grants the api pod ingress from the web pod on TCP:8080.
	_ = allowWebAPI
	addAllowEdge(t, fx, api, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 8080,
		PortTo:   8080,
	})

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.False(t, idx.skipped)
	require.Equal(t, 3, idx.podCount)

	// Pods that were restricted must report IngressRestricted=true.
	require.True(t, idx.pods[api].IngressRestricted)
	require.True(t, idx.pods[web].IngressRestricted)
	require.True(t, idx.pods[db].IngressRestricted)

	// web→api on TCP 8080 is allowed — ingress side permits it, egress
	// side is default-allow because no EdgeRestrictsEgress edge exists.
	assert.True(t, idx.canReach(web, api, "TCP", 8080),
		"web → api on TCP 8080 must be allowed by the allow-web-api policy")

	// web→api on TCP 8081 is NOT allowed — the allow edge covers only 8080.
	assert.False(t, idx.canReach(web, api, "TCP", 8081),
		"web → api on TCP 8081 must be blocked by default-deny")

	// web→db on any port is NOT allowed — no allow edge exists.
	assert.False(t, idx.canReach(web, db, "TCP", 8080),
		"web → db must be blocked — no allow-db policy exists")

	// api→web on TCP 8080 is NOT allowed — the allow edge covers the
	// opposite direction only.
	assert.False(t, idx.canReach(api, web, "TCP", 8080),
		"api → web must be blocked — only web → api is whitelisted")
}

// TestBuildReachabilityIndex_UnderCap_Normal exercises the normal-path
// builder with 999 pods (well below the hard cap). The builder must return
// a fully-populated index — no sentinel, all pods present.
func TestBuildReachabilityIndex_UnderCap_Normal(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 999 {
		addPod(fx, fmt.Sprintf("default/Pod/p-%04d", i), "default")
	}

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	assert.False(t, idx.skipped, "999 pods must NOT trip the cap")
	assert.Equal(t, 999, idx.podCount)
	assert.Len(t, idx.pods, 999)
}

// Streaming-path tests (above-legacy-cap and sentinel) live in
// k8s_reachability_streaming_test.go to keep this file under the hard
// 500-line file size cap. The shared withPodCap helper is also defined
// there so the streaming and sentinel tests can reuse it.

// TestKubernetesReachabilityAnalyzer_EmptyGraph exercises the normal-path
// classifier with no pods at all. Phase 4's full classifier short-circuits
// the matrix emitter when the index has zero pods and emits no findings from
// any sub-classifier (all guard on len(pods) >= 2), so the analyzer returns
// an empty slice — same behavior as the Phase 3 stub, just with a different
// internal path.
func TestKubernetesReachabilityAnalyzer_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	_ = fx.account(k8sReachabilityAcct)
	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
	})
	require.NoError(t, err)
	assert.Empty(t, findings, "phase 4 classifier emits no findings for an empty cloud graph")
}

// TestCanReach_PortRangeIntersection is the canonical Phase 2.5 Step 3
// test: a stored edge with (TCP, 80-100) must match query ports 80, 90,
// and 100 but NOT 79 or 101. This pins the range-intersection contract
// of rangeCovers.
func TestCanReach_PortRangeIntersection(t *testing.T) {
	fx := newCloudFixture(t)

	client := addPod(fx, "default/Pod/client", "default")
	server := addPod(fx, "default/Pod/server", "default")
	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	addRestrictsEdge(t, fx, denyAll, server, kgtypes.EdgeRestrictsIngress)

	// Allow client → server on TCP 80-100 (port range).
	addAllowEdge(t, fx, server, client, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   100,
	})

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.False(t, idx.skipped)

	// Ports 80, 100, and 90 must all match the stored range.
	assert.True(t, idx.canReach(client, server, "TCP", 80),
		"TCP 80 must match stored range (TCP, 80-100)")
	assert.True(t, idx.canReach(client, server, "TCP", 100),
		"TCP 100 must match stored range (TCP, 80-100)")
	assert.True(t, idx.canReach(client, server, "TCP", 90),
		"TCP 90 must match stored range (TCP, 80-100)")

	// Ports 79 and 101 must NOT match — just outside the range.
	assert.False(t, idx.canReach(client, server, "TCP", 79),
		"TCP 79 must NOT match stored range (TCP, 80-100)")
	assert.False(t, idx.canReach(client, server, "TCP", 101),
		"TCP 101 must NOT match stored range (TCP, 80-100)")

	// Protocol mismatch must not match even when the port is in range.
	assert.False(t, idx.canReach(client, server, "UDP", 80),
		"UDP 80 must NOT match a TCP-only stored range")
}

// TestCanReach_FullyOpenEdge pins the fully-open edge behavior: an edge
// with empty Evidence (zero-value portMetadata) must match any query,
// including unusual ports and protocols. This is the canonical
// "rule with no ports[] clause" signal Phase 2 emits.
func TestCanReach_FullyOpenEdge(t *testing.T) {
	fx := newCloudFixture(t)

	a := addPod(fx, "default/Pod/a", "default")
	b := addPod(fx, "default/Pod/b", "default")
	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	addRestrictsEdge(t, fx, denyAll, b, kgtypes.EdgeRestrictsIngress)

	// Fully-open allow edge: empty Evidence = all ports, all protocols.
	addAllowEdge(t, fx, b, a, edgePortMetadata{})

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)

	assert.True(t, idx.canReach(a, b, "TCP", 80))
	assert.True(t, idx.canReach(a, b, "UDP", 53))
	assert.True(t, idx.canReach(a, b, "SCTP", 443))
	assert.True(t, idx.canReach(a, b, "", 0))
}

// Canonical Service identity every reachability test uses. Hardcoded so
// addService stays unparam-clean; if a future test needs a second Service,
// reintroduce id/ns parameters.
const (
	k8sReachabilityTestServiceID = "api-ns/Service/api"
	k8sReachabilityTestServiceNS = "api-ns"
)

// addService creates a Service cloud-resource node in the k8sReachabilityAcct
// account under the canonical api-ns namespace and returns its ID. Keeps
// reachability tests terse.
func addService(fx *cloudFixture) string {
	meta := map[string]string{"namespace": k8sReachabilityTestServiceNS}
	n := fx.AddCloudResource(k8sReachabilityAcct, k8sReachabilityTestServiceID, k8sReachabilityTestServiceID, "Service", meta)
	return n.Id
}

// TestCanReachService_ORsOverBackingPods exercises the Phase 4.5 Step 1
// canReachService helper. Recipe: 2 backing pods (api-1, api-2), 1 external
// pod (web) in a different namespace. A Service selects both backing pods
// via SELECTS edges. web has default-allow egress; both backing pods are
// default-deny ingress, but only api-1 has an allow-edge from web on TCP/80.
// canReachService must return true (web reaches the service via api-1) for
// TCP/80 and false for TCP/443 (no backing pod accepts it).
func TestCanReachService_ORsOverBackingPods(t *testing.T) {
	fx := newCloudFixture(t)

	web := addPod(fx, "web-ns/Pod/web", "web-ns")
	api1 := addPod(fx, "api-ns/Pod/api-1", "api-ns")
	api2 := addPod(fx, "api-ns/Pod/api-2", "api-ns")

	denyAll := addNetworkPolicy(fx, "api-ns/NetworkPolicy/deny-all", "api-ns")
	addRestrictsEdge(t, fx, denyAll, api1, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api2, kgtypes.EdgeRestrictsIngress)

	// Allow web → api1 on TCP/80 only. api2 remains unreachable.
	addAllowEdge(t, fx, api1, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   80,
	})

	svc := addService(fx)
	fx.AddEdge(k8sReachabilityAcct, svc, api1, kgtypes.EdgeSelects)
	fx.AddEdge(k8sReachabilityAcct, svc, api2, kgtypes.EdgeSelects)

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.False(t, idx.skipped)

	// Index must have recorded both backing pods for the service.
	require.NotNil(t, idx.services[svc])
	assert.ElementsMatch(t, []string{api1, api2}, idx.services[svc].BackingPods)

	// canReachService: web → service on TCP/80 is true (via api1).
	assert.True(t, idx.canReachService(web, svc, "TCP", 80),
		"web → Service on TCP 80 must succeed via the api-1 backing pod")

	// canReachService: web → service on TCP/443 is false (no backing pod
	// accepts that port).
	assert.False(t, idx.canReachService(web, svc, "TCP", 443),
		"web → Service on TCP 443 must fail — no backing pod allows it")

	// canReachService: unknown service → false.
	assert.False(t, idx.canReachService(web, "api-ns/Service/ghost", "TCP", 80))

	// canReachService: unknown src pod → false.
	assert.False(t, idx.canReachService("default/Pod/ghost", svc, "TCP", 80))
}

// TestCanReachService_AllBackingPodsIsolated pins the "zero finding" path:
// a Service whose backing pods are all isolated by default-deny with no
// allow edges must yield canReachService=false for any external source.
// This is the negative half of the Phase 4.5 contract.
func TestCanReachService_AllBackingPodsIsolated(t *testing.T) {
	fx := newCloudFixture(t)

	web := addPod(fx, "web-ns/Pod/web", "web-ns")
	api1 := addPod(fx, "api-ns/Pod/api-1", "api-ns")
	api2 := addPod(fx, "api-ns/Pod/api-2", "api-ns")

	denyAll := addNetworkPolicy(fx, "api-ns/NetworkPolicy/deny-all", "api-ns")
	addRestrictsEdge(t, fx, denyAll, api1, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api2, kgtypes.EdgeRestrictsIngress)
	// No allow edges at all — the Service's backing pods are fully isolated.

	svc := addService(fx)
	fx.AddEdge(k8sReachabilityAcct, svc, api1, kgtypes.EdgeSelects)
	fx.AddEdge(k8sReachabilityAcct, svc, api2, kgtypes.EdgeSelects)

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.False(t, idx.skipped)

	assert.False(t, idx.canReachService(web, svc, "TCP", 80),
		"service with isolated backing pods must be unreachable")
	assert.False(t, idx.canReachService(web, svc, "", 0),
		"service with isolated backing pods must be unreachable on any probe")
}
