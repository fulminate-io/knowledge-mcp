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

// ingressContentJSON builds the minimal Content JSON the Ingress
// subcollector writes: metadata.creationTimestamp + status.loadBalancer
// .ingress[]. Pass nil ingress to exercise the "no Status yet" Warn path.
func ingressContentJSON(t *testing.T, ingress []map[string]string, createdAgo time.Duration) []byte {
	t.Helper()
	var created string
	if createdAgo > 0 {
		created = time.Now().Add(-createdAgo).UTC().Format(time.RFC3339)
	}
	payload := map[string]any{
		"metadata": map[string]any{
			"creationTimestamp": created,
		},
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

// ingressResource builds an Ingress NodeCloudResource in the "default"
// namespace with the given Content — mirrors serviceResource.
func ingressResource(name string, content []byte) *knowledgev1.Node {
	const namespace = "default"
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "Ingress", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", "Ingress")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// TestResolveIngressCloudLBLinkage_GCP: an Ingress with an IP entry that
// matches a forwardingRule in a sibling GCP cloud graph gets exactly one
// EXPOSED_BY edge to a cross-graph proxy of that rule.
func TestResolveIngressCloudLBLinkage_GCP(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/projects/ing-a/global/forwardingRules/fr-1"
		frIP     = "203.0.113.15"
		gkeGraph = "gke_ing-a_us-central1_main"
		proxyID  = "proxy:cloud:ing-a:" + frID
	)

	fake := newK8sFake()
	fake.seed("ing-a", forwardingRuleNode(frID, frIP, ""))

	ing := ingressResource("web", ingressContentJSON(t,
		[]map[string]string{{"ip": frIP}}, time.Hour))
	fake.seed(gkeGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, gkeGraph))

	edges := collectExposedByEdges(fake, gkeGraph, ing.Id)
	require.Len(t, edges, 1, "expected exactly one EXPOSED_BY edge")
	assert.Equal(t, proxyID, edges[0].ToId, "edge must target the cross-graph proxy")
	assert.Equal(t, string(kgtypes.EdgeExposedBy), edges[0].Type)
	assert.Equal(t, "gcp", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "ip="+frIP)

	proxy, ok := fake.nodeByID(gkeGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "ing-a", kgtypes.Value(proxy, "account"))
	assert.Equal(t, frID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, gcpForwardingRuleResourceType, kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "gcp", kgtypes.Value(proxy, "provider"))
}

// TestResolveIngressCloudLBLinkage_AWS: an Ingress with a Hostname entry
// matching an ELB DNS name gets one EXPOSED_BY edge. Exercises the AWS
// (ALB Ingress Controller) branch.
func TestResolveIngressCloudLBLinkage_AWS(t *testing.T) {
	ctx := newCtx(t)

	const (
		elbARN   = "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/ing/abc"
		elbDNS   = "ing-alb-abc.us-east-1.elb.amazonaws.com"
		k8sGraph = "eks_acme_prod_ingress"
		proxyID  = "proxy:cloud:aws-ing:" + elbARN
	)

	fake := newK8sFake()
	fake.seed("aws-ing", elbResNode(elbARN, "elbv2-loadbalancer", `{"DNSName":"`+elbDNS+`"}`))

	ing := ingressResource("api", ingressContentJSON(t,
		[]map[string]string{{"hostname": elbDNS}}, time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	require.Len(t, edges, 1)
	assert.Equal(t, proxyID, edges[0].ToId)
	assert.Equal(t, "aws", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "hostname="+elbDNS)

	proxy, ok := fake.nodeByID(k8sGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))
}

// TestResolveIngressCloudLBLinkage_NoMatch_NoEdge: an Ingress with a
// populated Status but no matching LB in either cloud index gets zero
// edges and no Warn (Status is present, so nothing is stale).
func TestResolveIngressCloudLBLinkage_NoMatch_NoEdge(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_ing-other_us-central1_main"

	fake := newK8sFake()
	// Seed an unrelated forwardingRule so neither index is empty —
	// exercises the inner loop rather than the top-level early exit.
	fake.seed("ing-other", forwardingRuleNode("https://compute/ing-other/fr-unrelated", "10.0.0.1", ""))

	ing := ingressResource("web", ingressContentJSON(t,
		[]map[string]string{{"ip": "203.0.113.99", "hostname": "nope.elb.amazonaws.com"}},
		time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	assert.Empty(t, edges, "no matching LB must produce no edges")
}

// TestResolveIngressCloudLBLinkage_EmptyStatus_Warn: an Ingress with an
// empty Status.LoadBalancer.Ingress gets zero edges (Warn is side-channel).
func TestResolveIngressCloudLBLinkage_EmptyStatus_Warn(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_ing-w_us-central1_main"

	fake := newK8sFake()
	// Seed a forwardingRule so we exercise the inner decode path.
	fake.seed("ing-w", forwardingRuleNode("https://compute/ing-w/fr-warn", "203.0.113.7", ""))

	ing := ingressResource("stale", ingressContentJSON(t, nil, 3*time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	assert.Empty(t, edges,
		"Ingress with empty Status.LoadBalancer.Ingress[] must produce zero edges")
}

// TestResolveIngressCloudLBLinkage_NoCloudGraphs_NoOp: with no cloud
// graphs loaded (non-GKE kubeconfig flow), the resolver must be a silent
// no-op.
func TestResolveIngressCloudLBLinkage_NoCloudGraphs_NoOp(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "bare-kubeconfig-ingress"

	fake := newK8sFake()
	ing := ingressResource("web", ingressContentJSON(t,
		[]map[string]string{{"ip": "203.0.113.5", "hostname": "lb.example.com"}},
		time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	assert.Empty(t, edges,
		"no cloud graphs loaded → no edges, no error")
}

// TestResolveIngressCloudLBLinkage_DuplicateEntries_Dedup: two Ingress
// entries resolving to the same forwardingRule collapse to a single edge
// via the seen map.
func TestResolveIngressCloudLBLinkage_DuplicateEntries_Dedup(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/ing-d/global/forwardingRules/fr-dup"
		frIP     = "203.0.113.28"
		k8sGraph = "gke_ing-d_us-central1_main"
	)
	fake := newK8sFake()
	fake.seed("ing-d", forwardingRuleNode(frID, frIP, ""))

	ing := ingressResource("dup", ingressContentJSON(t,
		[]map[string]string{{"ip": frIP}, {"ip": frIP}}, time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	assert.Len(t, edges, 1, "duplicate Ingress entries must dedup to one edge")
}

// TestResolveIngressCloudLBLinkage_GKE_TwoHopReachability: the critical
// integration check. Builds a realistic GKE LB chain in the sibling GCP
// graph — forwardingRule → targetHttpsProxy → urlMap, wired via
// EdgeTargets (the same edge the GCP loadbalancer.go subcollector emits).
// Runs postPopulate, walks Ingress → cross-graph proxy → (resolve) →
// forwardingRule → targetHttpsProxy → urlMap, and asserts the chain is
// reachable within N hops.
//
// This proves that emitting ONLY the entry-point edge is sufficient —
// open question #5 in the plan. If OQ#5 were resolved the other way,
// this test would instead assert direct edges to each intra-cloud node.
func TestResolveIngressCloudLBLinkage_GKE_TwoHopReachability(t *testing.T) {
	ctx := newCtx(t)

	const (
		gcpAcct       = "gke-chain"
		frID          = "projects/gke-chain/global/forwardingRules/fr-chain"
		frIP          = "34.120.0.99"
		targetProxyID = "projects/gke-chain/global/targetHttpsProxies/thp-chain"
		urlMapID      = "projects/gke-chain/global/urlMaps/um-chain"
		k8sGraph      = "gke_" + gcpAcct + "_us-central1_main"
	)

	fake := newK8sFake()
	// Seed the sibling GCP graph with a realistic chain.
	fake.seed(gcpAcct,
		forwardingRuleNode(frID, frIP, ""),
		gcpChainNode(targetProxyID, "gcp:compute:targetHttpsProxy"),
		gcpChainNode(urlMapID, "gcp:compute:urlMap"),
	)
	fake.seedEdge(gcpAcct, &knowledgev1.Edge{FromId: frID, ToId: targetProxyID, Type: string(kgtypes.EdgeTargets)})
	fake.seedEdge(gcpAcct, &knowledgev1.Edge{FromId: targetProxyID, ToId: urlMapID, Type: string(kgtypes.EdgeTargets)})

	// K8s graph with Ingress pointing at the forwardingRule's IP.
	ing := ingressResource("chain", ingressContentJSON(t,
		[]map[string]string{{"ip": frIP}}, time.Hour))
	fake.seed(k8sGraph, ing)

	require.NoError(t, resolveIngressCloudLBLinkage(ctx, fake, k8sGraph))

	// Hop 1: Ingress → proxy (in the K8s graph).
	edges := collectExposedByEdges(fake, k8sGraph, ing.Id)
	require.Len(t, edges, 1, "entry-point edge missing")
	proxyNodeID := edges[0].ToId

	// Resolve the proxy → sibling graph + foreign_id (mirrors how a
	// BFS walker crossing graphs would traverse this boundary).
	proxyNode, ok := fake.nodeByID(k8sGraph, proxyNodeID)
	require.True(t, ok)
	account, foreignID, ok := proxyCloudInfo(proxyNode)
	require.True(t, ok, "proxyCloudInfo must resolve foreign graph metadata")
	assert.Equal(t, gcpAcct, account)
	assert.Equal(t, frID, foreignID)

	// Hop 2+3: walk EdgeTargets in the sibling graph: forwardingRule
	// → targetHttpsProxy → urlMap. Confirms the intra-GCP chain is
	// reachable through the proxy boundary.
	reached := bfsReach(fake, gcpAcct, foreignID, []kgtypes.EdgeType{kgtypes.EdgeTargets}, 3)
	assert.Contains(t, reached, targetProxyID,
		"targetHttpsProxy must be reachable one EdgeTargets hop from the forwardingRule")
	assert.Contains(t, reached, urlMapID,
		"urlMap must be reachable two EdgeTargets hops from the forwardingRule; "+
			"emitting only the entry-point edge is sufficient")
}

// gcpChainNode constructs a cloud resource node representing one link in
// the intra-GCP LB chain (targetHttpsProxy, urlMap, backendService …).
// The node only needs to exist for edge walks — no Content is needed.
func gcpChainNode(id, resourceType string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: lastSlashSegment(id),
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	return n
}

// bfsReach runs a bounded BFS from start following the supplied edge
// types within the named account graph. Returns the set of node IDs
// reached (excluding start). Used only by the two-hop reachability
// tests — not exported.
func bfsReach(fake *k8sFake, account, start string, edgeTypes []kgtypes.EdgeType, maxHops int) map[string]struct{} {
	reached := make(map[string]struct{})
	frontier := []string{start}
	visited := map[string]struct{}{start: {}}
	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			for _, et := range edgeTypes {
				hopEdges := fake.outgoingEdges(account, id, et)
				for i := range hopEdges {
					toID := hopEdges[i].ToId
					if _, seen := visited[toID]; seen {
						continue
					}
					visited[toID] = struct{}{}
					reached[toID] = struct{}{}
					next = append(next, toID)
				}
			}
		}
		frontier = next
	}
	return reached
}
