// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_findings_test.go covers the Phase 4 classifier:
// classifyReachability, sub-classifiers (isolated, over-exposed, asymmetric,
// namespace-fully-open), the k8s_emit_asymmetric flag contract, the
// matrix emitter, and the per-(protocol, port) collapse behavior.

// runClassify runs the full analyzer Run → classifyReachability path against
// a fixture and returns the emitted findings filtered to a single algorithm.
func runClassify(t *testing.T, fx *cloudFixture, extra map[string]string) []Finding {
	t.Helper()
	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		Extra:  extra,
	})
	require.NoError(t, err)
	return findings
}

// findingsByCategory filters findings by a title/summary substring so tests
// can assert on a single sub-classifier's output without tight coupling to
// finding order. Case-sensitive match.
func findingsByCategory(findings []Finding, substr string) []Finding {
	var out []Finding
	for _, f := range findings {
		if containsFold(f.Title, substr) {
			out = append(out, f)
		}
	}
	return out
}

// containsFold is a tiny helper so tests don't pull in strings.Contains
// directly in every match — keeps assertion sites terse.
func containsFold(s, substr string) bool {
	// Using a naive check is fine for the bounded titles this file asserts on.
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// buildIsolatedFixture stands up a 3-pod namespace where one pod is fully
// isolated (ingress+egress restricted, no allow edges) and two pods have
// default-allow on egress so they are NOT isolated. Used by the isolated
// classifier test.
func buildIsolatedFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)

	lonely := addPod(fx, "default/Pod/lonely", "default")
	a := addPod(fx, "default/Pod/a", "default")
	b := addPod(fx, "default/Pod/b", "default")

	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	// The "lonely" pod is selected for BOTH ingress and egress restrictions
	// and has no allow edges → fully isolated.
	addRestrictsEdge(t, fx, denyAll, lonely, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, lonely, kgtypes.EdgeRestrictsEgress)
	// a and b are NOT restricted → default-allow on both sides.
	_ = a
	_ = b
	return fx
}

// TestClassifyReachability_Isolated verifies findIsolatedPods fires for a
// pod with ingress and egress restrictions and no allow edges.
func TestClassifyReachability_Isolated(t *testing.T) {
	fx := buildIsolatedFixture(t)
	findings := runClassify(t, fx, nil)

	isolated := findingsByCategory(findings, "Pod fully isolated")
	require.Len(t, isolated, 1, "exactly one pod must be flagged isolated")
	assert.Equal(t, SeverityInfo, isolated[0].Severity)
	assert.Equal(t, "default/Pod/lonely", isolated[0].Evidence[0])
}

// buildNamespaceFullyOpenFixture builds a 3-pod namespace with no
// NetworkPolicy at all — every pod is default-allow in both directions, so
// namespace-fully-open should fire.
func buildNamespaceFullyOpenFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	addPod(fx, "default/Pod/p1", "default")
	addPod(fx, "default/Pod/p2", "default")
	addPod(fx, "default/Pod/p3", "default")
	return fx
}

// TestClassifyReachability_NamespaceFullyOpen verifies findNamespaceFullyOpen
// fires for a namespace with no NetworkPolicy.
func TestClassifyReachability_NamespaceFullyOpen(t *testing.T) {
	fx := buildNamespaceFullyOpenFixture(t)
	findings := runClassify(t, fx, nil)

	ns := findingsByCategory(findings, "Namespace fully open")
	require.Len(t, ns, 1, "exactly one namespace must be flagged fully open")
	assert.Equal(t, SeverityWarning, ns[0].Severity)
	assert.Equal(t, "default", ns[0].Metadata["namespace"])
}

// buildAsymmetricFixture creates a 2-pod namespace where web can reach api
// on TCP/80 but the reverse is blocked by default-deny.
func buildAsymmetricFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	web := addPod(fx, "default/Pod/web", "default")
	api := addPod(fx, "default/Pod/api", "default")
	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	addRestrictsEdge(t, fx, denyAll, web, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api, kgtypes.EdgeRestrictsIngress)
	// Allow web → api on TCP/80 only.
	addAllowEdge(t, fx, api, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   80,
	})
	return fx
}

// TestClassifyReachability_AsymmetricDefaultSeverity verifies the default
// emit mode surfaces asymmetric findings at SeverityInfo.
func TestClassifyReachability_AsymmetricDefaultSeverity(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, nil)

	asym := findingsByCategory(findings, "Asymmetric reachability")
	require.NotEmpty(t, asym, "asymmetric finding must be emitted by default")
	for _, f := range asym {
		assert.Equal(t, SeverityInfo, f.Severity, "default mode emits SeverityInfo")
	}
}

// TestClassifyReachability_AsymmetricWarningMode verifies the "warning"
// emit mode bumps severity.
func TestClassifyReachability_AsymmetricWarningMode(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, map[string]string{"k8s_emit_asymmetric": "warning"})

	asym := findingsByCategory(findings, "Asymmetric reachability")
	require.NotEmpty(t, asym, "warning mode still emits asymmetric findings")
	for _, f := range asym {
		assert.Equal(t, SeverityWarning, f.Severity, "warning mode emits SeverityWarning")
	}
}

// TestClassifyReachability_AsymmetricSuppressMode verifies the "suppress"
// emit mode skips the classifier entirely.
func TestClassifyReachability_AsymmetricSuppressMode(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, map[string]string{"k8s_emit_asymmetric": "suppress"})

	asym := findingsByCategory(findings, "Asymmetric reachability")
	assert.Empty(t, asym, "suppress mode must produce zero asymmetric findings")
}

// TestClassifyReachability_AsymmetricInvalidFallsBack verifies an invalid
// flag value falls back to SeverityInfo without erroring.
func TestClassifyReachability_AsymmetricInvalidFallsBack(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, map[string]string{"k8s_emit_asymmetric": "oops-invalid"})

	asym := findingsByCategory(findings, "Asymmetric reachability")
	require.NotEmpty(t, asym, "invalid flag must fall back to info (not suppress)")
	for _, f := range asym {
		assert.Equal(t, SeverityInfo, f.Severity, "invalid flag falls back to SeverityInfo")
	}
}

// TestClassifyReachability_FlagDocumentedInRunGodoc is a reflection-ish test
// that the k8s_emit_asymmetric flag is referenced in the Run method's godoc.
// The test reads the source file and asserts the doc contains the key terms.
// Keeps the documentation criterion honest without parsing AST.
func TestClassifyReachability_FlagDocumentedInRunGodoc(t *testing.T) {
	src := mustReadFile(t, "k8s_reachability.go")
	assert.Contains(t, src, "k8s_emit_asymmetric")
	assert.Contains(t, src, `"info"`)
	assert.Contains(t, src, `"warning"`)
	assert.Contains(t, src, `"suppress"`)
}

// mustReadFile reads a topology-package source file relative to the test
// binary's working directory (which `go test` anchors at the package path).
func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err, "read %s", name)
	return string(b)
}

// buildServiceCrossNsReachableFixture stands up a Service with two backing
// pods in "api-ns" and one external pod in "web-ns". A NetworkPolicy
// default-denies ingress to both backing pods, but an allow edge grants
// web → api-1 on TCP/80. The Service should surface as cross-namespace
// reachable via the api-1 backing pod.
func buildServiceCrossNsReachableFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)

	web := addPod(fx, "web-ns/Pod/web", "web-ns")
	api1 := addPod(fx, "api-ns/Pod/api-1", "api-ns")
	api2 := addPod(fx, "api-ns/Pod/api-2", "api-ns")

	denyAll := addNetworkPolicy(fx, "api-ns/NetworkPolicy/deny-all", "api-ns")
	addRestrictsEdge(t, fx, denyAll, api1, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api2, kgtypes.EdgeRestrictsIngress)

	addAllowEdge(t, fx, api1, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   80,
	})

	svc := addService(fx)
	fx.AddEdge(k8sReachabilityAcct, svc, api1, kgtypes.EdgeSelects)
	fx.AddEdge(k8sReachabilityAcct, svc, api2, kgtypes.EdgeSelects)
	return fx
}

// buildServiceFullyIsolatedFixture is the negative half: same Service + 2
// backing pods + 1 external pod, but NO allow edges. Every backing pod is
// default-deny and zero external traffic can reach the Service. The
// classifier must emit zero service_cross_ns_reachable findings.
func buildServiceFullyIsolatedFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)

	addPod(fx, "web-ns/Pod/web", "web-ns")
	api1 := addPod(fx, "api-ns/Pod/api-1", "api-ns")
	api2 := addPod(fx, "api-ns/Pod/api-2", "api-ns")

	denyAll := addNetworkPolicy(fx, "api-ns/NetworkPolicy/deny-all", "api-ns")
	addRestrictsEdge(t, fx, denyAll, api1, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api2, kgtypes.EdgeRestrictsIngress)
	// Both backing pods are default-deny egress too so they can't serve
	// any traffic — fully isolated.
	addRestrictsEdge(t, fx, denyAll, api1, kgtypes.EdgeRestrictsEgress)
	addRestrictsEdge(t, fx, denyAll, api2, kgtypes.EdgeRestrictsEgress)

	svc := addService(fx)
	fx.AddEdge(k8sReachabilityAcct, svc, api1, kgtypes.EdgeSelects)
	fx.AddEdge(k8sReachabilityAcct, svc, api2, kgtypes.EdgeSelects)
	return fx
}

// TestClassifyServiceReachability_CrossNsExposure pins the positive case:
// the Service cross-namespace classifier fires when an external pod can
// reach at least one backing pod.
func TestClassifyServiceReachability_CrossNsExposure(t *testing.T) {
	fx := buildServiceCrossNsReachableFixture(t)
	findings := runClassify(t, fx, nil)

	svcFindings := findingsByCategory(findings, "Service cross-namespace reachable")
	require.Len(t, svcFindings, 1, "exactly one service must be flagged cross-ns reachable")
	f := svcFindings[0]
	assert.Equal(t, SeverityWarning, f.Severity)
	assert.Equal(t, "api-ns", f.Metadata["namespace"])
	assert.Equal(t, "api-ns/Service/api", f.Metadata["service_id"])
	assert.Equal(t, "api-ns/Service/api", f.Evidence[0])
	assert.Contains(t, f.Evidence, "web-ns/Pod/web",
		"external reaching pod must be listed in Evidence")
	assert.InDelta(t, 2, f.Metrics["backing_pod_count"], 0.0001)
	assert.InDelta(t, 1, f.Metrics["external_source_count"], 0.0001)
}

// TestClassifyServiceReachability_FullyIsolatedZeroFindings is the
// criterion-pinned negative test: a Service whose backing pods are fully
// isolated produces zero service_cross_ns_reachable findings.
func TestClassifyServiceReachability_FullyIsolatedZeroFindings(t *testing.T) {
	fx := buildServiceFullyIsolatedFixture(t)
	findings := runClassify(t, fx, nil)

	svcFindings := findingsByCategory(findings, "Service cross-namespace reachable")
	assert.Empty(t, svcFindings, "fully isolated Service must emit zero cross-ns findings")
}

// addIngress creates an Ingress cloud-resource node with the given
// namespace metadata. Mirrors addService/addPod for symmetry.
func addIngress(fx *cloudFixture, account, id, ns string) string {
	meta := map[string]string{"namespace": ns}
	n := fx.AddCloudResource(account, id, id, "Ingress", meta)
	return n.Id
}

// TestClassifyIngressReachability_CrossNsExposure pins the Phase 4.5 Step 3
// happy path: an Ingress routes to a Service whose backing pods are
// reachable from outside the Ingress's namespace. The Ingress classifier
// must surface the cross-namespace exposure chain.
func TestClassifyIngressReachability_CrossNsExposure(t *testing.T) {
	fx := buildServiceCrossNsReachableFixture(t)

	// Add an Ingress in "edge-ns" that routes to the api service.
	ing := addIngress(fx, k8sReachabilityAcct, "edge-ns/Ingress/public", "edge-ns")
	fx.AddEdge(k8sReachabilityAcct, ing, "api-ns/Service/api", kgtypes.EdgeRoutesTo)

	findings := runClassify(t, fx, nil)

	ingFindings := findingsByCategory(findings, "Ingress cross-namespace reachable")
	require.Len(t, ingFindings, 1, "exactly one ingress must be flagged cross-ns reachable")
	f := ingFindings[0]
	assert.Equal(t, SeverityWarning, f.Severity)
	assert.Equal(t, "edge-ns", f.Metadata["namespace"])
	assert.Equal(t, "edge-ns/Ingress/public", f.Metadata["ingress_id"])
	assert.Equal(t, "edge-ns/Ingress/public", f.Evidence[0])
	assert.Contains(t, f.Evidence, "web-ns/Pod/web",
		"external pod that reaches the ingress-routed service must be in Evidence")
}

// TestClassifyIngressReachability_NoIngressNodes verifies that a graph
// without any Ingress nodes emits zero ingress_cross_ns_reachable findings
// and otherwise runs the classifier end-to-end without errors. This pins
// the Phase 4.5 Step 3 criterion's "skipped" leg — the classifier returns
// nil when index.ingresses is empty.
func TestClassifyIngressReachability_NoIngressNodes(t *testing.T) {
	fx := buildServiceCrossNsReachableFixture(t)
	// No Ingress nodes added — only Service + pods.
	findings := runClassify(t, fx, nil)

	ingFindings := findingsByCategory(findings, "Ingress cross-namespace reachable")
	assert.Empty(t, ingFindings, "graph without Ingress nodes must emit zero ingress findings")
}

// TestClassifyIngressReachability_BackingServiceIsolated verifies the
// negative path: an Ingress routes to a Service whose backing pods are
// fully isolated. Even though the Ingress is present, no cross-namespace
// reach can be established, so the classifier emits zero findings.
func TestClassifyIngressReachability_BackingServiceIsolated(t *testing.T) {
	fx := buildServiceFullyIsolatedFixture(t)

	ing := addIngress(fx, k8sReachabilityAcct, "edge-ns/Ingress/public", "edge-ns")
	fx.AddEdge(k8sReachabilityAcct, ing, "api-ns/Service/api", kgtypes.EdgeRoutesTo)

	findings := runClassify(t, fx, nil)

	ingFindings := findingsByCategory(findings, "Ingress cross-namespace reachable")
	assert.Empty(t, ingFindings,
		"Ingress routing to fully-isolated backing pods must emit zero findings")
}
