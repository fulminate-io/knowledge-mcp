// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// k8s_reachability_recipe_test.go is the Phase 5 Step 2 recipe suite. Each
// test builds a small k8sFixture, runs the KubernetesReachabilityAnalyzer
// end-to-end, and asserts the expected finding categories fire. The recipes
// cover the canonical NetworkPolicy patterns: deny-all, intra-ns allow,
// cross-ns allow, fully-open, asymmetric, over-exposed, ipBlock world
// exposure, Service composition, empty graph, and named-port resolution.
//
// Tests assert on finding-category substrings (via findingsByCategory) rather
// than exact counts because sub-classifiers frequently overlap — an
// asymmetric pair also appears as a partial-reachability finding, a fully-open
// namespace also yields over-exposed findings for each pod, etc. Substring
// matching is the most honest contract for "this category fired at least
// once". Tight-count assertions are reserved for the isolated/namespace-fully-
// open tests where the classifier's shape is unambiguous.

// runRecipe is a tiny wrapper that runs the analyzer against a k8sFixture
// and returns the emitted findings. Kept out of the fixture file so the
// fixture helper stays focused on graph construction. Recipe tests that
// need to set req.Extra (e.g. k8s_emit_asymmetric mode) call the analyzer
// directly instead of going through this helper.
func runRecipe(t *testing.T, fx *k8sFixture) []Finding {
	t.Helper()
	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), fx.req())
	require.NoError(t, err)
	return findings
}

// tcpPort is shorthand for building edgePortMetadata with a single TCP port.
func tcpPort(p int) edgePortMetadata {
	return edgePortMetadata{Protocol: "TCP", PortFrom: p, PortTo: p}
}

// TestReachability_DenyAllIngress builds the canonical deny-all recipe:
// a single NetworkPolicy selects every pod for BOTH ingress and egress
// restrictions with no allow edges. The classifier must mark every pod as
// fully isolated.
func TestReachability_DenyAllIngress(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	a := fx.AddPod("default", "a", map[string]string{"app": "a"})
	b := fx.AddPod("default", "b", map[string]string{"app": "b"})
	c := fx.AddPod("default", "c", map[string]string{"app": "c"})

	denyAll := fx.AddNetworkPolicy("default", "deny-all")
	for _, pod := range []string{a, b, c} {
		fx.RestrictIngress(denyAll, pod)
		fx.RestrictEgress(denyAll, pod)
	}

	findings := runRecipe(t, fx)

	isolated := findingsByCategory(findings, "Pod fully isolated")
	require.Len(t, isolated, 3, "every pod must be flagged isolated under deny-all")
	for _, f := range isolated {
		assert.Equal(t, SeverityInfo, f.Severity)
	}
}

// TestReachability_AllowFromSameNamespace builds the canonical intra-ns
// allow recipe: frontend and backend in the same namespace. Backend is
// default-deny ingress with an allow rule granting frontend → backend on
// TCP/80 only. Other frontend→backend ports are blocked, and a second
// unrelated pod (other) is also denied. The assertions pin the canReach
// contract: frontend can hit backend on TCP/80 but nothing else can.
func TestReachability_AllowFromSameNamespace(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	frontend := fx.AddPod("default", "frontend", map[string]string{"tier": "frontend"})
	backend := fx.AddPod("default", "backend", map[string]string{"tier": "backend"})
	other := fx.AddPod("default", "other", map[string]string{"tier": "other"})

	policy := fx.AddNetworkPolicy("default", "backend-ingress")
	fx.RestrictIngress(policy, backend)
	fx.AllowIngress(backend, frontend, tcpPort(80))

	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.False(t, idx.skipped)

	// Backend is default-deny ingress. Frontend reaches it on TCP/80 only.
	assert.True(t, idx.canReach(frontend, backend, "TCP", 80),
		"frontend → backend on TCP/80 must succeed")
	assert.False(t, idx.canReach(frontend, backend, "TCP", 443),
		"frontend → backend on TCP/443 must be blocked")
	assert.False(t, idx.canReach(other, backend, "TCP", 80),
		"other → backend on TCP/80 must be blocked — no allow rule for other")

	// The classifier should surface a partial-reachability finding because
	// the frontend→backend pair has mixed reachability across the probe
	// set (TCP/80 is the only probe, but other→backend pairs are all
	// blocked, creating asymmetry at the classifier level).
	findings := runRecipe(t, fx)
	require.NotEmpty(t, findings,
		"at least one reachability finding should fire under a selective allow policy")
}

// TestReachability_AllowFromNamespaceSelector exercises the cross-namespace
// recipe. frontend lives in ns-a, backend in ns-b; the backend policy allows
// ingress from frontend's namespace selector — expressed here as a direct
// allow edge because the collector pre-resolves namespaceSelector at index
// time. The recipe confirms cross-ns reachability is honored.
func TestReachability_AllowFromNamespaceSelector(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("ns-a", map[string]string{"team": "web"})
	fx.AddNamespace("ns-b", map[string]string{"team": "api"})
	frontend := fx.AddPod("ns-a", "frontend", map[string]string{"app": "web"})
	backend := fx.AddPod("ns-b", "backend", map[string]string{"app": "api"})

	policy := fx.AddNetworkPolicy("ns-b", "allow-from-web")
	fx.RestrictIngress(policy, backend)
	fx.AllowIngress(backend, frontend, tcpPort(443))

	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.False(t, idx.skipped)
	assert.True(t, idx.canReach(frontend, backend, "TCP", 443),
		"cross-ns frontend → backend on TCP/443 must succeed")
	assert.False(t, idx.canReach(frontend, backend, "TCP", 80),
		"cross-ns frontend → backend on TCP/80 must be blocked — only 443 is whitelisted")
	// frontend is NOT ingress-restricted in this recipe (no policy selects
	// it), so backend → frontend is default-allow. The reachability contract
	// here is strictly "the cross-ns allow rule pointed at backend works" —
	// not that the reverse is blocked. A tighter recipe would restrict
	// frontend too, but the plan's intent is to exercise the cross-ns allow
	// path end-to-end, not the reverse-direction contract.
	assert.Equal(t, "ns-a", idx.pods[frontend].Namespace)
	assert.Equal(t, "ns-b", idx.pods[backend].Namespace)
}

// TestReachability_NamespaceFullyOpen builds a namespace with multiple pods
// and no NetworkPolicy. Every pod is default-allow in both directions, so
// the namespace-fully-open classifier must surface exactly one warning
// finding for the namespace.
func TestReachability_NamespaceFullyOpen(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	fx.AddPod("default", "p1", nil)
	fx.AddPod("default", "p2", nil)
	fx.AddPod("default", "p3", nil)

	findings := runRecipe(t, fx)

	ns := findingsByCategory(findings, "Namespace fully open")
	require.Len(t, ns, 1, "exactly one namespace fully-open finding expected")
	assert.Equal(t, SeverityWarning, ns[0].Severity)
	assert.Equal(t, "default", ns[0].Metadata["namespace"])
}

// TestReachability_Asymmetric pins the asymmetric-reachability recipe: a
// single allow edge on one direction creates an asymmetric pair. Verifies
// the classifier surfaces the finding at default severity (info).
func TestReachability_Asymmetric(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	a := fx.AddPod("default", "a", nil)
	b := fx.AddPod("default", "b", nil)

	policy := fx.AddNetworkPolicy("default", "deny-all-ingress")
	fx.RestrictIngress(policy, a)
	fx.RestrictIngress(policy, b)
	// Only A → B allowed; B → A remains blocked by default-deny.
	fx.AllowIngress(b, a, tcpPort(80))

	findings := runRecipe(t, fx)
	asym := findingsByCategory(findings, "Asymmetric reachability")
	require.NotEmpty(t, asym, "asymmetric classifier must fire for one-way allow")
	for _, f := range asym {
		assert.Equal(t, SeverityInfo, f.Severity, "default severity is info")
		assert.Contains(t, f.Evidence, a)
		assert.Contains(t, f.Evidence, b)
	}
}

// TestReachability_OverExposed builds a namespace with three pods — one
// target, two peers — where the target's ingress is restricted but every
// peer has an allow edge to it. The classifier's allPeersReach walk must
// fire the over-exposed finding for the target pod.
func TestReachability_OverExposed(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	target := fx.AddPod("default", "target", map[string]string{"role": "target"})
	peer1 := fx.AddPod("default", "peer-1", map[string]string{"role": "peer"})
	peer2 := fx.AddPod("default", "peer-2", map[string]string{"role": "peer"})

	policy := fx.AddNetworkPolicy("default", "target-ingress")
	fx.RestrictIngress(policy, target)
	// Every peer is whitelisted to the target on TCP/80.
	fx.AllowIngress(target, peer1, tcpPort(80))
	fx.AllowIngress(target, peer2, tcpPort(80))

	findings := runRecipe(t, fx)
	over := findingsByCategory(findings, "Pod over-exposed")
	require.NotEmpty(t, over, "over-exposed classifier must fire for the target pod")
	var targetFinding *Finding
	for i := range over {
		if over[i].Evidence[0] == target {
			targetFinding = &over[i]
			break
		}
	}
	require.NotNil(t, targetFinding, "over-exposed finding must cite the target pod")
	assert.Equal(t, SeverityNotice, targetFinding.Severity)
}

// TestReachability_IpBlockWorldExposure sets a NetworkPolicy Content to a
// JSON document containing spec.ingress[].from[].ipBlock.cidr == 0.0.0.0/0,
// which the findWorldExposedPods classifier re-parses and surfaces as a
// critical finding.
func TestReachability_IpBlockWorldExposure(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	fx.AddPod("default", "public-target", nil)

	content := `{
  "spec": {
    "ingress": [
      {
        "from": [
          { "ipBlock": { "cidr": "0.0.0.0/0" } }
        ]
      }
    ]
  }
}`
	fx.AddNetworkPolicyWithContent("default", "public-ingress", content)

	findings := runRecipe(t, fx)
	var world []Finding
	for _, f := range findings {
		if indexOf(f.Title, "admits ingress from 0.0.0.0/0") >= 0 {
			world = append(world, f)
		}
	}
	require.Len(t, world, 1, "exactly one world-exposure finding expected")
	assert.Equal(t, SeverityCritical, world[0].Severity)
	assert.Equal(t, "0.0.0.0/0", world[0].Metadata["cidr"])
}

// TestReachability_ServiceComposition exercises the Phase 4.5 Service
// composition path end-to-end: two backing pods in api-ns, one external pod
// in web-ns, a NetworkPolicy default-denies ingress to the backing pods, and
// a single allow edge grants web → api-1 on TCP/80. The Service selects
// both backing pods. canReachService(web, svc, TCP, 80) must return true and
// the classifier must surface the cross-ns exposure finding.
func TestReachability_ServiceComposition(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("web-ns", nil)
	fx.AddNamespace("api-ns", nil)
	web := fx.AddPod("web-ns", "web", nil)
	api1 := fx.AddPod("api-ns", "api-1", nil)
	api2 := fx.AddPod("api-ns", "api-2", nil)

	policy := fx.AddNetworkPolicy("api-ns", "deny-all")
	fx.RestrictIngress(policy, api1)
	fx.RestrictIngress(policy, api2)
	fx.AllowIngress(api1, web, tcpPort(80))

	svc := fx.AddService("api-ns", "api", []string{api1, api2})

	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.False(t, idx.skipped)
	require.NotNil(t, idx.services[svc])
	assert.ElementsMatch(t, []string{api1, api2}, idx.services[svc].BackingPods)
	assert.True(t, idx.canReachService(web, svc, "TCP", 80),
		"Service reach must OR over backing pods and pick up api-1's allow edge")
	assert.False(t, idx.canReachService(web, svc, "TCP", 443),
		"no backing pod accepts TCP/443 → Service reach must be false")

	findings := runRecipe(t, fx)
	svcFindings := findingsByCategory(findings, "Service cross-namespace reachable")
	require.Len(t, svcFindings, 1, "exactly one Service cross-ns finding expected")
	assert.Equal(t, SeverityWarning, svcFindings[0].Severity)
	assert.Equal(t, "api-ns/Service/api", svcFindings[0].Metadata["service_id"])
}

// TestReachability_EmptyGraph verifies an empty account (no pods, no
// policies) produces no findings and no error. The fixture creates the
// cloud graph via fx.DB() then immediately hands it to the analyzer.
func TestReachability_EmptyGraph(t *testing.T) {
	fx := buildK8sFixture(t)
	// Touch the account so the cloud graph exists, then run with nothing
	// in it. Matches TestKubernetesReachabilityAnalyzer_EmptyGraph's setup.
	_ = fx.cloud.account(fx.Account())

	findings := runRecipe(t, fx)
	assert.Empty(t, findings, "empty cluster must produce zero findings")
}

// TestReachability_NamedPortResolution builds a pod with a containerPort
// named "web" bound to numeric port 8080 and a policy that allows ingress
// to "web" (named port). The fixture's resolveNamedPort rewrites the allow
// edge's port metadata to TCP/8080 at emit time, so the resulting
// reachability index treats the allow as a TCP/8080 rule. canReach(client,
// server, TCP, 8080) must succeed; TCP/8081 must fail.
func TestReachability_NamedPortResolution(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	client := fx.AddPod("default", "client", nil)
	server := fx.AddPod("default", "server", nil)
	fx.AddContainerPort(server, "web", 8080)

	policy := fx.AddNetworkPolicy("default", "server-ingress")
	fx.RestrictIngress(policy, server)
	fx.AllowIngress(server, client, edgePortMetadata{
		Protocol:  "TCP",
		NamedPort: "web",
	})

	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.False(t, idx.skipped)

	ranges := idx.pods[server].AllowedIngressFrom[client]
	require.Len(t, ranges, 1, "one allow edge expected client → server")
	assert.Equal(t, "TCP", ranges[0].Protocol)
	assert.Equal(t, 8080, ranges[0].PortFrom,
		"named port 'web' must resolve to the registered container port 8080")
	assert.Equal(t, 8080, ranges[0].PortTo)

	assert.True(t, idx.canReach(client, server, "TCP", 8080),
		"reach must succeed on resolved named port TCP/8080")
	assert.False(t, idx.canReach(client, server, "TCP", 8081),
		"reach on an unmapped port must fail")
}
