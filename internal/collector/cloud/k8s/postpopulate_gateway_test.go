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

// proxyCloudInfo is the wire-only reader for the cloud-proxy fields a
// resolver-emitted proxy carries — the GraphCloud branch of
// kgwire.ProxyInfo, replicated locally to return the (account, foreign_id)
// pair the Gateway tests assert on directly. Returns ok=false unless n is a
// NodeProxy with foreign_graph="cloud". The Gateway tests only exercise
// cloud proxies, so the other ProxyInfo branches are intentionally absent.
func proxyCloudInfo(n *knowledgev1.Node) (account, foreignID string, ok bool) {
	if kgtypes.NodeType(n.Type) != kgtypes.NodeProxy {
		return "", "", false
	}
	if kgtypes.Value(n, "foreign_graph") != string(kgtypes.GraphCloud) {
		return "", "", false
	}
	return kgtypes.Value(n, "account"), kgtypes.Value(n, "foreign_id"), true
}

// gatewayContentJSON builds the minimal Content JSON the Gateway
// subcollector writes: metadata.creationTimestamp + status.addresses[].
// Pass nil addresses to exercise the "no Status yet" Warn path.
//
// Each entry in addresses must carry "type" and "value" keys mirroring the
// Gateway API v1 GatewayStatusAddress shape.
func gatewayContentJSON(t *testing.T, addresses []map[string]string, createdAgo time.Duration) []byte {
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
			"addresses": addresses,
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// gatewayResource builds a Gateway NodeCloudResource in the "default"
// namespace with the given Content — mirrors serviceResource / ingressResource.
func gatewayResource(name string, content []byte) *knowledgev1.Node {
	const namespace = "default"
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "Gateway", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", "Gateway")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// TestResolveGatewayCloudLBLinkage_GCP: a Gateway with a Type=IPAddress
// entry that matches a forwardingRule in a sibling GCP cloud graph gets
// exactly one EXPOSED_BY edge to a cross-graph proxy of that rule.
func TestResolveGatewayCloudLBLinkage_GCP(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/projects/gw-a/global/forwardingRules/fr-1"
		frIP     = "34.120.0.2"
		gkeGraph = "gke_gw-a_us-central1_main"
		proxyID  = "proxy:cloud:gw-a:" + frID
	)

	fake := newK8sFake()
	fake.seed("gw-a", forwardingRuleNode(frID, frIP, ""))

	gw := gatewayResource("web", gatewayContentJSON(t,
		[]map[string]string{{"type": "IPAddress", "value": frIP}}, time.Hour))
	fake.seed(gkeGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, gkeGraph))

	edges := collectExposedByEdges(fake, gkeGraph, gw.Id)
	require.Len(t, edges, 1, "expected exactly one EXPOSED_BY edge")
	assert.Equal(t, proxyID, edges[0].ToId, "edge must target the cross-graph proxy")
	assert.Equal(t, string(kgtypes.EdgeExposedBy), edges[0].Type)
	assert.Equal(t, "gcp", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "ip="+frIP)

	proxy, ok := fake.nodeByID(gkeGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "gw-a", kgtypes.Value(proxy, "account"))
	assert.Equal(t, frID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, gcpForwardingRuleResourceType, kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "gcp", kgtypes.Value(proxy, "provider"))
}

// TestResolveGatewayCloudLBLinkage_AWS: a Gateway with a Type=Hostname
// entry matching an ELB DNS name gets one EXPOSED_BY edge. Exercises the
// AWS (e.g. AWS Gateway API controller) branch.
func TestResolveGatewayCloudLBLinkage_AWS(t *testing.T) {
	ctx := newCtx(t)

	const (
		elbARN   = "arn:aws:elasticloadbalancing:us-east-1:222:loadbalancer/app/gw/xyz"
		elbDNS   = "gw-alb-xyz.us-east-1.elb.amazonaws.com"
		k8sGraph = "eks_acme_prod_gateway"
		proxyID  = "proxy:cloud:aws-gw:" + elbARN
	)

	fake := newK8sFake()
	fake.seed("aws-gw", elbResNode(elbARN, "elbv2-loadbalancer", `{"DNSName":"`+elbDNS+`"}`))

	gw := gatewayResource("api", gatewayContentJSON(t,
		[]map[string]string{{"type": "Hostname", "value": elbDNS}}, time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	require.Len(t, edges, 1)
	assert.Equal(t, proxyID, edges[0].ToId)
	assert.Equal(t, "aws", edges[0].Method)
	assert.Contains(t, edges[0].Evidence, "hostname="+elbDNS)

	proxy, ok := fake.nodeByID(k8sGraph, proxyID)
	require.True(t, ok)
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))
}

// TestResolveGatewayCloudLBLinkage_ImplSpecificType_Skip: a Gateway with a
// Type value outside the two standard ones ("IPAddress" | "Hostname") —
// e.g. "NamedAddress" used by some controllers — must be silently skipped.
// Zero edges AND no Warn (Status is non-empty, so nothing is stale; the
// Type is simply not portable).
func TestResolveGatewayCloudLBLinkage_ImplSpecificType_Skip(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_gw-impl_us-central1_main"

	fake := newK8sFake()
	// Seed a forwardingRule so neither index is empty — exercises the
	// inner loop rather than the top-level early exit.
	fake.seed("gw-impl", forwardingRuleNode("https://compute/gw-impl/fr-unrelated", "10.0.0.2", ""))

	gw := gatewayResource("custom", gatewayContentJSON(t,
		[]map[string]string{{"type": "NamedAddress", "value": "shared-ip-1"}},
		time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	assert.Empty(t, edges,
		"implementation-specific Type must be silently skipped (zero edges)")
}

// TestResolveGatewayCloudLBLinkage_NoMatch_NoEdge: a Gateway with populated
// Status.Addresses entries that don't match any LB in either cloud index
// gets zero edges and no Warn (Status is present, so nothing is stale).
func TestResolveGatewayCloudLBLinkage_NoMatch_NoEdge(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_gw-other_us-central1_main"

	fake := newK8sFake()
	// Seed an unrelated forwardingRule so neither index is empty —
	// exercises the inner loop rather than the top-level early exit.
	fake.seed("gw-other", forwardingRuleNode("https://compute/gw-other/fr-unrelated", "10.0.0.3", ""))

	gw := gatewayResource("web", gatewayContentJSON(t,
		[]map[string]string{
			{"type": "IPAddress", "value": "34.120.0.99"},
			{"type": "Hostname", "value": "nope.elb.amazonaws.com"},
		},
		time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	assert.Empty(t, edges, "no matching LB must produce no edges")
}

// TestResolveGatewayCloudLBLinkage_EmptyStatus_Warn: a Gateway with an
// empty Status.Addresses gets zero edges. (Warn is a side-channel; the
// observable behavior is zero edges.)
func TestResolveGatewayCloudLBLinkage_EmptyStatus_Warn(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "gke_gw-w_us-central1_main"

	fake := newK8sFake()
	// Seed a forwardingRule so we exercise the inner decode path.
	fake.seed("gw-w", forwardingRuleNode("https://compute/gw-w/fr-warn", "34.120.0.7", ""))

	gw := gatewayResource("stale", gatewayContentJSON(t, nil, 3*time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	assert.Empty(t, edges,
		"Gateway with empty Status.Addresses[] must produce zero edges")
}

// TestResolveGatewayCloudLBLinkage_NoCloudGraphs_NoOp: with no cloud
// graphs loaded (non-GKE kubeconfig flow), the resolver must be a silent
// no-op.
func TestResolveGatewayCloudLBLinkage_NoCloudGraphs_NoOp(t *testing.T) {
	ctx := newCtx(t)

	const k8sGraph = "bare-kubeconfig-gateway"

	fake := newK8sFake()
	gw := gatewayResource("web", gatewayContentJSON(t,
		[]map[string]string{
			{"type": "IPAddress", "value": "34.120.0.5"},
			{"type": "Hostname", "value": "lb.example.com"},
		},
		time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))
	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	assert.Empty(t, edges, "no cloud graphs loaded → no edges, no error")
}

// TestResolveGatewayCloudLBLinkage_DuplicateEntries_Dedup: two Gateway
// addresses resolving to the same forwardingRule collapse to a single
// edge via the seen map.
func TestResolveGatewayCloudLBLinkage_DuplicateEntries_Dedup(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/gw-d/global/forwardingRules/fr-dup"
		frIP     = "34.120.0.28"
		k8sGraph = "gke_gw-d_us-central1_main"
	)
	fake := newK8sFake()
	fake.seed("gw-d", forwardingRuleNode(frID, frIP, ""))

	gw := gatewayResource("dup", gatewayContentJSON(t,
		[]map[string]string{
			{"type": "IPAddress", "value": frIP},
			{"type": "IPAddress", "value": frIP},
		}, time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	assert.Len(t, edges, 1, "duplicate Gateway addresses must dedup to one edge")
}

// TestResolveGatewayCloudLBLinkage_GKE_TwoHopReachability: the critical
// cross-graph integration check. Builds a realistic GKE LB chain in the
// sibling GCP graph (forwardingRule → targetHttpsProxy → urlMap wired via
// EdgeTargets, matching what cloud/gcp/loadbalancer.go emits). Runs the
// Gateway resolver, walks Gateway → cross-graph proxy → (resolve) →
// forwardingRule → targetHttpsProxy → urlMap, and asserts the chain is
// reachable within N hops.
//
// Proves emitting ONLY the entry-point edge from Gateway is sufficient:
// the intra-cloud GCP chain is already wired by the GCP collector, so the
// cross-graph proxy boundary + existing EdgeTargets chain gives full
// reachability without a per-hop resolver.
func TestResolveGatewayCloudLBLinkage_GKE_TwoHopReachability(t *testing.T) {
	ctx := newCtx(t)

	const (
		gcpAcct       = "gke-gw-chain"
		frID          = "projects/gke-gw-chain/global/forwardingRules/fr-chain"
		frIP          = "34.120.0.99"
		targetProxyID = "projects/gke-gw-chain/global/targetHttpsProxies/thp-chain"
		urlMapID      = "projects/gke-gw-chain/global/urlMaps/um-chain"
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

	// K8s graph with Gateway pointing at the forwardingRule's IP.
	gw := gatewayResource("chain", gatewayContentJSON(t,
		[]map[string]string{{"type": "IPAddress", "value": frIP}}, time.Hour))
	fake.seed(k8sGraph, gw)

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	// Hop 1: Gateway → proxy (in the K8s graph).
	edges := collectExposedByEdges(fake, k8sGraph, gw.Id)
	require.Len(t, edges, 1, "entry-point edge missing")
	proxyNodeID := edges[0].ToId

	// Resolve the proxy → sibling graph + foreign_id.
	proxyNode, ok := fake.nodeByID(k8sGraph, proxyNodeID)
	require.True(t, ok)
	account, foreignID, ok := proxyCloudInfo(proxyNode)
	require.True(t, ok, "proxyCloudInfo must resolve foreign graph metadata")
	assert.Equal(t, gcpAcct, account)
	assert.Equal(t, frID, foreignID)

	// Hop 2+3: walk EdgeTargets in the sibling graph: forwardingRule →
	// targetHttpsProxy → urlMap. Confirms the intra-GCP chain is reachable
	// through the proxy boundary.
	reached := bfsReach(fake, gcpAcct, foreignID, []kgtypes.EdgeType{kgtypes.EdgeTargets}, 3)
	assert.Contains(t, reached, targetProxyID,
		"targetHttpsProxy must be reachable one EdgeTargets hop from forwardingRule")
	assert.Contains(t, reached, urlMapID,
		"urlMap must be reachable two EdgeTargets hops from forwardingRule; "+
			"emitting only the entry-point Gateway→proxy edge is sufficient")
}

// TestResolveGatewayCloudLBLinkage_HTTPRouteViaGateway: the ticket
// explicitly claims HTTPRoute/GRPCRoute connect via existing ROUTES_TO to
// Gateway so external visibility lights up naturally once Gateway is
// cross-linked. This test seeds an HTTPRoute → Gateway ROUTES_TO edge
// (the exact edge sub_gatewayapi.go emits in extractRouteParentEdges),
// runs the Gateway resolver, and confirms a 2-hop BFS from HTTPRoute
// reaches the cross-graph cloud LB proxy.
func TestResolveGatewayCloudLBLinkage_HTTPRouteViaGateway(t *testing.T) {
	ctx := newCtx(t)

	const (
		frID     = "https://compute/gw-route/global/forwardingRules/fr-route"
		frIP     = "34.120.0.50"
		k8sGraph = "gke_gw-route_us-central1_main"
	)
	fake := newK8sFake()
	fake.seed("gw-route", forwardingRuleNode(frID, frIP, ""))

	gw := gatewayResource("edge", gatewayContentJSON(t,
		[]map[string]string{{"type": "IPAddress", "value": frIP}}, time.Hour))

	// Mirror sub_gatewayapi.go: HTTPRoute in default namespace pointing at
	// the Gateway via a parentRef (already emitted as EdgeRoutesTo by the
	// subcollector — here we seed the edge directly).
	const routeName = "web-route"
	route := &knowledgev1.Node{
		Id:         resourceID("default", "HTTPRoute", routeName),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: routeName,
		Source:     "cloud",
	}
	kgtypes.SetValue(route, "resource_type", "HTTPRoute")
	kgtypes.SetValue(route, "namespace", "default")

	fake.seed(k8sGraph, gw, route)
	fake.seedEdge(k8sGraph, &knowledgev1.Edge{FromId: route.Id, ToId: gw.Id, Type: string(kgtypes.EdgeRoutesTo)})

	require.NoError(t, resolveGatewayCloudLBLinkage(ctx, fake, k8sGraph))

	// 2-hop BFS from HTTPRoute: ROUTES_TO → Gateway, EXPOSED_BY → proxy.
	reached := bfsReach(fake, k8sGraph, route.Id,
		[]kgtypes.EdgeType{kgtypes.EdgeRoutesTo, kgtypes.EdgeExposedBy}, 2)
	assert.Contains(t, reached, gw.Id,
		"Gateway must be reachable one ROUTES_TO hop from HTTPRoute")

	// Confirm the cross-graph proxy for the forwardingRule is in the
	// 2-hop reachable set — this is the "external visibility lights up
	// naturally" claim from the ticket.
	var sawProxy bool
	for id := range reached {
		n, found := fake.nodeByID(k8sGraph, id)
		if !found {
			continue
		}
		_, foreignID, ok := proxyCloudInfo(n)
		if !ok {
			continue
		}
		if foreignID == frID {
			sawProxy = true
			break
		}
	}
	assert.True(t, sawProxy,
		"2-hop BFS HTTPRoute→Gateway→cloud-LB proxy must reach the forwardingRule proxy")
}
