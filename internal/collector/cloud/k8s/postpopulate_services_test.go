// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// serviceContentJSON builds the minimal Content JSON the subcollector
// writes for a Service: metadata.creationTimestamp + spec.type +
// status.loadBalancer.ingress[]. Callers pass nil ingress to exercise the
// "no Status yet" Warn path.
func serviceContentJSON(t *testing.T, svcType string, ingress []map[string]string, createdAgo time.Duration) []byte {
	t.Helper()
	var created string
	if createdAgo > 0 {
		created = time.Now().Add(-createdAgo).UTC().Format(time.RFC3339)
	}
	payload := map[string]any{
		"metadata": map[string]any{
			"creationTimestamp": created,
		},
		"spec": map[string]any{"type": svcType},
		"status": map[string]any{
			"loadBalancer": map[string]any{
				"ingress": ingress,
			},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// serviceResource builds a Service NodeCloudResource in the "default"
// namespace with the given Content — mirrors workloadNode but keeps the
// resource_type fixed to "Service".
func serviceResource(name string, content []byte) *knowledgev1.Node {
	const namespace = "default"
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "Service", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", "Service")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// collectExposedByEdges returns every outgoing EdgeExposedBy edge from the
// given node in the named account graph. Mirrors collectConnectsToEdges in
// postpopulate_external_test.go.
func collectExposedByEdges(fake *k8sFake, account, from string) []knowledgev1.Edge {
	return fake.outgoingEdges(account, from, kgtypes.EdgeExposedBy)
}

// TestResolveServiceCloudLBLinkage_GCP: a LoadBalancer Service with an
// IP Ingress that matches a forwardingRule in a sibling cloud graph gets
// exactly one EXPOSED_BY edge to a cross-graph proxy of that rule.
func TestResolveServiceCloudLBLinkage_GCP(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/projects/proj-a/global/forwardingRules/fr-1"
		frIP     = "203.0.113.5"
		gkeGraph = "gke_proj-a_us-central1_main"
		proxyID  = "proxy:cloud:proj-a:" + frID
	)

	fake := newK8sFake()
	// Seed the sibling GCP cloud graph with the matching forwardingRule.
	fake.seed("proj-a", forwardingRuleNode(frID, frIP, ""))

	// The GKE graph the resolver runs against.
	svc := serviceResource("web", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"ip": frIP}}, time.Hour))
	fake.seed(gkeGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, gkeGraph))

	edges := collectExposedByEdges(fake, gkeGraph, svc.Id)
	require.Len(t, edges, 1, "expected exactly one EXPOSED_BY edge")
	assert.Equal(t, proxyID, edges[0].ToId, "edge must target the cross-graph proxy")
	assert.Equal(t, string(kgtypes.EdgeExposedBy), edges[0].Type)
	assert.Equal(t, "gcp", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "ip="+frIP)

	// The proxy must exist with deterministic metadata.
	proxy, ok := fake.nodeByID(gkeGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "proj-a", kgtypes.Value(proxy, "account"))
	assert.Equal(t, frID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, gcpForwardingRuleResourceType, kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "gcp", kgtypes.Value(proxy, "provider"))
}

// TestResolveServiceCloudLBLinkage_AWS: a LoadBalancer Service with a
// Hostname Ingress that matches an ELB DNS name gets one EXPOSED_BY
// edge. Exercises the AWS branch of the resolver.
func TestResolveServiceCloudLBLinkage_AWS(t *testing.T) {
	ctx := newCtx(t)

	const (
		elbARN   = "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/web/abc"
		elbDNS   = "my-lb-abc.us-east-1.elb.amazonaws.com"
		k8sGraph = "eks_acme_prod"
		proxyID  = "proxy:cloud:aws-prod:" + elbARN
	)

	fake := newK8sFake()
	// Seed AWS cloud graph with the ELB.
	fake.seed("aws-prod", elbResNode(elbARN, "elbv2-loadbalancer", `{"DNSName":"`+elbDNS+`"}`))

	// The K8s graph the resolver runs against. Use a non-GKE name to confirm
	// the resolver doesn't rely on GKE-shaped graph names.
	svc := serviceResource("api", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"hostname": elbDNS}}, time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	require.Len(t, edges, 1)
	assert.Equal(t, proxyID, edges[0].ToId)
	assert.Equal(t, "aws", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "hostname="+elbDNS)

	proxy, ok := fake.nodeByID(k8sGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))
}

// TestResolveServiceCloudLBLinkage_AWS_MixedCaseHostname: the resolver
// must lowercase the Ingress Hostname before looking it up in the index.
// (The index itself already lowercases its keys — this guards the caller
// side symmetrically.)
func TestResolveServiceCloudLBLinkage_AWS_MixedCaseHostname(t *testing.T) {
	ctx := newCtx(t)

	const (
		elbARN   = "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/web/xyz"
		elbDNS   = "mylb-xyz.us-east-1.elb.amazonaws.com"
		k8sGraph = "k8s-case"
	)

	fake := newK8sFake()
	fake.seed("aws-case", elbResNode(elbARN, "elbv2-loadbalancer", `{"DNSName":"`+elbDNS+`"}`))

	// Seed with an ALLCAPS hostname — the lookup must case-fold.
	svc := serviceResource("web", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"hostname": "MYLB-XYZ.US-EAST-1.elb.amazonaws.com"}}, time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	require.Len(t, edges, 1, "hostname lookup must be case-insensitive")
}

// TestResolveServiceCloudLBLinkage_IPAndHostname_GCPWins: when the same
// Ingress entry carries BOTH an IP and a Hostname, the GCP IP index is
// consulted first per the plan's decision. Only the GCP edge emits.
func TestResolveServiceCloudLBLinkage_IPAndHostname_GCPWins(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/projects/proj-b/global/forwardingRules/fr-x"
		frIP     = "198.51.100.7"
		elbARN   = "arn:aws:elasticloadbalancing:us-east-1:222:loadbalancer/app/other/def"
		elbDNS   = "other.us-east-1.elb.amazonaws.com"
		k8sGraph = "gke_proj-b_us-central1_main"
	)

	fake := newK8sFake()
	fake.seed("proj-b", forwardingRuleNode(frID, frIP, ""))
	fake.seed("aws-shared", elbResNode(elbARN, "elbv2-loadbalancer", `{"DNSName":"`+elbDNS+`"}`))

	svc := serviceResource("dual", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"ip": frIP, "hostname": elbDNS}}, time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	require.Len(t, edges, 1, "IP match wins over Hostname on the same Ingress")
	assert.Equal(t, "gcp", edges[0].Method)
	assert.Equal(t, "proxy:cloud:proj-b:"+frID, edges[0].ToId)
}

// TestResolveServiceCloudLBLinkage_NoMatch_NoEdge: a LoadBalancer Service
// whose Ingress[] IP and Hostname do not appear in any cloud graph gets
// zero edges. Also exercises the "seed cloud graphs exist but nothing
// matches this service" path (distinct from the no-graphs-loaded no-op).
func TestResolveServiceCloudLBLinkage_NoMatch_NoEdge(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_proj-other_us-central1_main"

	fake := newK8sFake()
	// Seed an unrelated forwardingRule so the early "both indexes empty"
	// short-circuit does NOT fire — we're testing the inner loop.
	fake.seed("proj-other", forwardingRuleNode("https://compute/other/fr-unrelated", "10.0.0.1", ""))

	svc := serviceResource("web", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"ip": "203.0.113.99", "hostname": "nope.elb.amazonaws.com"}},
		time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	assert.Empty(t, edges, "no matching LB must produce no edges")
}

// TestResolveServiceCloudLBLinkage_ClusterIP_Skipped: ClusterIP Services
// are skipped even when they carry a Status.LoadBalancer.Ingress (which
// shouldn't happen in practice but defends against stale/weird content).
func TestResolveServiceCloudLBLinkage_ClusterIP_Skipped(t *testing.T) {
	ctx := newCtx(t)

	const (
		frIP     = "203.0.113.42"
		k8sGraph = "gke_proj-c_us-central1_main"
	)

	fake := newK8sFake()
	fake.seed("proj-c", forwardingRuleNode("https://compute/proj-c/fr-ci", frIP, ""))

	// A ClusterIP Service with a fabricated matching Ingress — the
	// resolver must skip it because Spec.Type != LoadBalancer.
	svc := serviceResource("internal", serviceContentJSON(t, "ClusterIP",
		[]map[string]string{{"ip": frIP}}, time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	assert.Empty(t, edges, "ClusterIP Service must be skipped regardless of Ingress contents")
}

// TestResolveServiceCloudLBLinkage_EmptyIngress_Warn: a LoadBalancer-typed
// Service with no Status.LoadBalancer.Ingress gets zero edges (the Warn
// log itself is side-channel — we don't assert the log line, just the
// edge count, per past guidance).
func TestResolveServiceCloudLBLinkage_EmptyIngress_Warn(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_proj-w_us-central1_main"

	fake := newK8sFake()
	// Seed a forwardingRule so both indexes are non-empty and we exercise
	// the inner decode path rather than the top-level early-exit.
	fake.seed("proj-w", forwardingRuleNode("https://compute/proj-w/fr-warn", "203.0.113.7", ""))

	svc := serviceResource("stale", serviceContentJSON(t, "LoadBalancer", nil, 2*time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	assert.Empty(t, edges,
		"LoadBalancer Service with empty Ingress[] must produce zero edges")
}

// TestResolveServiceCloudLBLinkage_DuplicateIngress_Dedup: two Ingress
// entries resolving to the same forwardingRule collapse to a single edge
// via the seen map — matches external.go's emitConnectsToRaw pattern.
func TestResolveServiceCloudLBLinkage_DuplicateIngress_Dedup(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/proj-d/global/forwardingRules/fr-dup"
		frIP     = "203.0.113.8"
		k8sGraph = "gke_proj-d_us-central1_main"
	)
	fake := newK8sFake()
	fake.seed("proj-d", forwardingRuleNode(frID, frIP, ""))

	// Two ingress entries with the same IP → should dedup to one edge.
	svc := serviceResource("dup", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"ip": frIP}, {"ip": frIP}}, time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	assert.Len(t, edges, 1, "duplicate Ingress entries must dedup to one edge")
}

// TestResolveServiceCloudLBLinkage_NoCloudGraphs_NoOp: with no cloud
// graphs loaded (non-GKE kubeconfig flow), the resolver must be a silent
// no-op — no error, no edges, no proxies.
func TestResolveServiceCloudLBLinkage_NoCloudGraphs_NoOp(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "bare-kubeconfig"

	// Only the K8s graph exists. No forwardingRule or ELB anywhere.
	fake := newK8sFake()
	svc := serviceResource("web", serviceContentJSON(t, "LoadBalancer",
		[]map[string]string{{"ip": "203.0.113.5", "hostname": "lb.example.com"}},
		time.Hour))
	fake.seed(k8sGraph, svc)

	require.NoError(t, resolveServiceCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, svc.Id)
	assert.Empty(t, edges,
		"no cloud graphs loaded → no edges, no error")
}

// TestResourceAge_FormatMatchesPlanContract: the age helper is a pure
// function shared across Service / Ingress / Gateway warn helpers.
func TestResourceAge_FormatMatchesPlanContract(t *testing.T) {
	cases := []struct {
		name, ts, wantKind string
	}{
		{"empty", "", "unknown"},
		{"malformed", "not-a-date", "unknown"},
		{"valid rfc3339", time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339), "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourceAge(tc.ts)
			switch tc.wantKind {
			case "unknown":
				assert.Equal(t, "unknown", got)
			case "duration":
				assert.NotEqual(t, "unknown", got)
				// duration string always has a "h" or "m" or "s" suffix somewhere.
				_, err := time.ParseDuration(got)
				assert.NoError(t, err, "valid timestamp must yield a ParseDuration-compatible string")
			}
		})
	}
}
