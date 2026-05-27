// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sync/errgroup"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_index.go builds and queries the per-account reachability
// lookup structure consumed by the KubernetesReachabilityAnalyzer. The index
// is built from a fully scoped cloud graph and walks:
//
//   - every Pod node — to populate the pods map keyed by pod ID
//   - every NetworkPolicy node — cached so the classifier can cite policies
//     in its findings without re-querying the graph
//   - every pod's inbound EdgeRestrictsIngress / EdgeRestrictsEgress edges —
//     to mark which pods have default-deny on each direction
//   - every pod's outbound EdgeAllowsIngressFrom / EdgeAllowsEgressTo edges —
//     to populate the per-direction allow maps with (protocol, port range)
//     tuples parsed from Edge.Evidence
//
// Phase 2 emits the reachability edges; Phase 2.5 stamps (protocol, port_from,
// port_to) metadata on every edge. Phase 3 consumes both: buildReachabilityIndex
// walks the edges with iterEdges queries, parses the port metadata via a local
// anonymous JSON struct (so topology/ stays free of cloud/ imports), and
// exposes canReach for classification queries.
//
// EDGE-DRIVEN CLASSIFIERS. Every sub-classifier (isolated, over-exposed,
// asymmetric, partial reachability, namespace-fully-open) is edge-driven or
// precondition-checked — none performs the naive O(P²) pod-pair walk. The
// per-pod classifiers iterate each pod's own allow-map degrees in
// O(P + E); partial reachability enumerates candidate pairs from the
// forward allow/ANP edges (provably complete — see
// k8s_reachability_findings_partial.go for the correctness proof);
// namespace-fully-open checks a cheap O(|namespace|) precondition that is
// necessary AND sufficient for the classification. See
// k8s_reachability_findings_streaming.go for the shared helpers the
// per-pod classifiers consume.
//
// HARD CAP. The index refuses to build when the cluster contains more than
// reachabilityPodCap pods — set high enough (50000) to cover any realistic
// cluster. This is pure runaway protection: the edge-driven classifiers
// scale well past this threshold, but the graph-query overhead during
// build plus the downstream matrix emitter make larger clusters
// impractical. On cap exceed the builder returns
// &reachabilityIndex{skipped: true, podCount: N} — a sentinel the
// classifier detects and surfaces as a single reachability_skipped notice
// finding. reachabilityPodCap is the ONLY cap; the previous pair-quadratic
// fallback cap was deleted once the classifiers were proven complete.

// reachabilityPodCap is the hard cap on pods per cluster. Exceeding the cap
// short-circuits index construction and surfaces a skipped sentinel. The
// value is intentionally loose — edge-driven classifiers are O(P + E) and
// real clusters rarely exceed a few thousand pods per account. Declared as
// a var (not const) so tests can lower it to exercise the sentinel path
// without allocating 50k fixture nodes.
var reachabilityPodCap = 50000

// reachabilityIndex, serviceInfo, ingressInfo, and podInfo are defined in
// k8s_reachability_index_types.go. Build and walk behavior lives below.

// buildReachabilityIndex walks the scoped cloud graph and returns a
// reachabilityIndex ready for classification. The builder counts pods
// BEFORE allocating the index so the hard-cap path can short-circuit
// without paying the O(P * edges) walk cost. Exceeding reachabilityPodCap
// returns a sentinel index with skipped=true and podCount populated.
//
// On the normal path, the builder:
//  1. Queries every Pod node.
//  2. Queries every NetworkPolicy node and caches them.
//  3. For each pod, walks its inbound EdgeRestricts{Ingress,Egress} edges
//     to populate the restricted flags.
//  4. For each pod, walks its outbound EdgeAllowsIngressFrom /
//     EdgeAllowsEgressTo edges — parsing each edge's port metadata via
//     parseEdgePortRange — to populate the per-peer allow maps.
//
// Edge-direction note: EdgeAllowsIngressFrom points `dst → src` by
// convention (dst_pod accepts ingress from src_pod). So from the dst pod's
// perspective, an OUTGOING EdgeAllowsIngressFrom edge identifies a source it
// allows. EdgeAllowsEgressTo points `src → dst`, so an outgoing edge from a
// pod identifies a destination that pod's egress policy permits.
func buildReachabilityIndex(ctx context.Context, scoped *cloudReader) (*reachabilityIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/k8s_reachability_index: %w", err)
	}
	if scoped == nil {
		return nil, fmt.Errorf("topology/k8s_reachability_index: scoped reader must not be nil")
	}

	// Pull all cloud-resource nodes and split into pods vs networkpolicies
	// by reading resource_type metadata inline. The meta query predicate is
	// not honored by the generic executor path, so filtering has to happen
	// here (matches orphan.go's dispatch pattern).
	allNodes, err := scoped.cloudResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cloud resources: %w", err)
	}

	var podNodes []*knowledgev1.Node
	var policyNodes []*knowledgev1.Node
	var serviceNodes []*knowledgev1.Node
	var ingressNodes []*knowledgev1.Node
	for i := range allNodes {
		n := allNodes[i]
		switch nodeMeta(n, "resource_type") {
		case "Pod":
			podNodes = append(podNodes, n)
		case "NetworkPolicy":
			policyNodes = append(policyNodes, n)
		case "Service":
			serviceNodes = append(serviceNodes, n)
		case "Ingress":
			ingressNodes = append(ingressNodes, n)
		}
	}
	podCount := len(podNodes)

	if podCount > reachabilityPodCap {
		return &reachabilityIndex{
			skipped:  true,
			podCount: podCount,
		}, nil
	}

	idx := &reachabilityIndex{
		pods:                  make(map[string]*podInfo, podCount),
		policies:              make(map[string]*knowledgev1.Node, len(policyNodes)),
		services:              make(map[string]*serviceInfo, len(serviceNodes)),
		podCount:              podCount,
		reverseAllowedIngress: make(map[string]map[string]struct{}, podCount),
		reverseAllowedEgress:  make(map[string]map[string]struct{}, podCount),
	}

	for i := range podNodes {
		n := podNodes[i]
		idx.pods[n.Id] = &podInfo{
			ID:                 n.Id,
			Namespace:          nodeMeta(n, "namespace"),
			Labels:             map[string]string{},
			AllowedIngressFrom: map[string][]portRange{},
			AllowedEgressTo:    map[string][]portRange{},
			ANPIngressFrom:     map[string][]anpRange{},
			ANPEgressTo:        map[string][]anpRange{},
		}
	}

	for i := range policyNodes {
		idx.policies[policyNodes[i].Id] = policyNodes[i]
	}

	if err := populatePodEdges(ctx, scoped, idx); err != nil {
		return nil, err
	}
	if err := populateServices(ctx, scoped, idx, serviceNodes); err != nil {
		return nil, err
	}
	if err := populateIngresses(ctx, scoped, idx, ingressNodes); err != nil {
		return nil, err
	}
	return idx, nil
}

// populatePodEdges fans out per-pod edge walks in parallel. Each pod's
// restricts-flags, allow-maps, and ANP-maps are written to its own
// *podInfo (no cross-pod contention). The shared reverse-allow maps
// (idx.reverseAllowedIngress/Egress) are protected by a mutex.
// idx.pods is pre-allocated and never grown during the parallel phase,
// so concurrent reads of different map keys are safe.
func populatePodEdges(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex) error {
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for podID, info := range idx.pods {
		g.Go(func() error {
			return populateOnePod(gctx, scoped, idx, &mu, podID, info)
		})
	}
	return g.Wait()
}

// populateOnePod walks a single pod's restricts + allow + ANP edges.
// The mu parameter protects writes to idx.reverseAllowedIngress/Egress.
func populateOnePod(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex, mu *sync.Mutex, podID string, info *podInfo) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("topology/k8s_reachability_index: %w", err)
	}

	if ingressRestricts, _ := scoped.iterEdges(ctx, podID, incomingEdges, []kgtypes.EdgeType{kgtypes.EdgeRestrictsIngress}); len(ingressRestricts) > 0 {
		info.IngressRestricted = true
	}

	if egressRestricts, _ := scoped.iterEdges(ctx, podID, incomingEdges, []kgtypes.EdgeType{kgtypes.EdgeRestrictsEgress}); len(egressRestricts) > 0 {
		info.EgressRestricted = true
	}

	if err := populatePodAllowEdges(ctx, scoped, idx, mu, podID, info); err != nil {
		return err
	}

	return populateANPEdges(ctx, scoped, idx, podID, info)
}

// populatePodAllowEdges walks the per-pod EdgeAllowsIngressFrom and
// EdgeAllowsEgressTo edges, populating the forward allow maps on podInfo and
// the symmetric reverse-allow lookups on the index. The mu parameter protects
// the shared idx.reverseAllowedIngress/Egress maps from concurrent writes;
// per-pod info.AllowedIngressFrom/EgressTo are owned by a single goroutine.
func populatePodAllowEdges(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex, mu *sync.Mutex, podID string, info *podInfo) error {
	ingress, _ := scoped.iterEdges(ctx, podID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsIngressFrom})
	for _, e := range ingress {
		if _, ok := idx.pods[e.ToId]; !ok {
			continue
		}
		info.AllowedIngressFrom[e.ToId] = append(info.AllowedIngressFrom[e.ToId], parseEdgePortRange(e.Evidence))
		mu.Lock()
		rev := idx.reverseAllowedIngress[e.ToId]
		if rev == nil {
			rev = map[string]struct{}{}
			idx.reverseAllowedIngress[e.ToId] = rev
		}
		rev[podID] = struct{}{}
		mu.Unlock()
	}

	egress, _ := scoped.iterEdges(ctx, podID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsEgressTo})
	for _, e := range egress {
		if _, ok := idx.pods[e.ToId]; !ok {
			continue
		}
		info.AllowedEgressTo[e.ToId] = append(info.AllowedEgressTo[e.ToId], parseEdgePortRange(e.Evidence))
		mu.Lock()
		rev := idx.reverseAllowedEgress[e.ToId]
		if rev == nil {
			rev = map[string]struct{}{}
			idx.reverseAllowedEgress[e.ToId] = rev
		}
		rev[podID] = struct{}{}
		mu.Unlock()
	}
	return nil
}

// populateANPEdges lives in k8s_reachability_anp.go to keep this file under
// the 300-line soft cap and to colocate ANP graph-walking with ANP semantics.

// canReach, rangeCovers, portRange, edgePortMetadata, and parseEdgePortRange
// live in k8s_reachability_ports.go so the port/protocol matching contract
// stays in one file independent of the graph-walking logic here.
