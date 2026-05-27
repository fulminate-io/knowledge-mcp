// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_index_services.go holds the Phase 4.5 Service and Ingress
// composition helpers for the reachability index. These helpers walk SELECTS
// (Service → Pod) and ROUTES_TO (Ingress → Service) edges at build time and
// cache the backing-pod / backing-service lists on the index so the per-pair
// classifiers can OR reachability results across every backing pod in
// constant-time lookups. The file is split out of k8s_reachability_index.go
// purely to stay under the topology package's 300-line soft cap.
//
// LAYERING. topology/ must not import cloud/k8s/, so these helpers walk
// generic graph edges (EdgeSelects, EdgeRoutesTo) emitted by the
// cloud/k8s/postpopulate*.go pipeline. No K8s-specific selector code lives in
// this file — it's just graph walking + a canReach OR.
//
// BUILD ORDER. populateServices must run after populatePodEdges (so every
// pod is present in idx.pods) but before populateIngresses (because ingress
// resolution filters to service IDs that exist in idx.services). The
// builder in k8s_reachability_index.go honors this ordering.

// populateServices walks every Service node's outbound EdgeSelects edges and
// stores the resulting backing-pod list on the index. Services with zero
// backing pods are still recorded so the classifier can surface them if
// needed, but their BackingPods slice is nil.
//
// Service IDs whose SELECTS edges point to pods that are not in idx.pods
// (stale edges, malformed writes) are silently filtered — the helpers
// downstream rely on every entry being a known pod.
func populateServices(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex, serviceNodes []*knowledgev1.Node) error {
	for i := range serviceNodes {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/k8s_reachability_index_services: %w", err)
		}
		svc := serviceNodes[i]
		info := &serviceInfo{
			ID:        svc.Id,
			Namespace: nodeMeta(svc, "namespace"),
		}
		edges, _ := scoped.iterEdges(ctx, svc.Id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeSelects})
		for _, e := range edges {
			if _, ok := idx.pods[e.ToId]; !ok {
				continue
			}
			info.BackingPods = append(info.BackingPods, e.ToId)
		}
		sort.Strings(info.BackingPods)
		idx.services[svc.Id] = info
	}
	return nil
}

// populateIngresses walks every Ingress node's outbound EdgeRoutesTo edges
// and stores the resulting backing-service list on the index. Ingress nodes
// with zero backing services are still recorded. Service IDs that are not
// in idx.services are filtered — the classifier downstream assumes every
// backing service is a known entry.
//
// When serviceNodes is empty on the caller side, populateIngresses skips
// allocating idx.ingresses entirely. Phase 4.5 Step 3 uses presence of
// idx.ingresses to decide whether to run the Ingress classifier.
func populateIngresses(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex, ingressNodes []*knowledgev1.Node) error {
	if len(ingressNodes) == 0 {
		return nil
	}
	idx.ingresses = make(map[string]*ingressInfo, len(ingressNodes))
	for i := range ingressNodes {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/k8s_reachability_index_services: %w", err)
		}
		ing := ingressNodes[i]
		info := &ingressInfo{
			ID:        ing.Id,
			Namespace: nodeMeta(ing, "namespace"),
		}
		seen := map[string]struct{}{}
		edges, _ := scoped.iterEdges(ctx, ing.Id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeRoutesTo})
		for _, e := range edges {
			if _, ok := idx.services[e.ToId]; !ok {
				continue
			}
			if _, dup := seen[e.ToId]; dup {
				continue
			}
			seen[e.ToId] = struct{}{}
			info.BackingServices = append(info.BackingServices, e.ToId)
		}
		sort.Strings(info.BackingServices)
		idx.ingresses[ing.Id] = info
	}
	return nil
}

// canReachService reports whether srcPod may reach at least one pod backing
// serviceID on the given (protocol, port). Returns false when the service is
// not present in the index, has zero backing pods, or when canReach rejects
// every backing pod. Unknown srcPod IDs also return false — callers are
// expected to pre-filter to pods that exist in the index.
//
// The helper OR's over every backing pod so the result models "the Service
// is reachable from srcPod" rather than "a specific backing pod is
// reachable". Phase 4.5 Step 2 uses this for the Service cross-namespace
// classifier, and downstream analyzers (unified public_exposure) call it to
// stitch Service → pod chains without re-walking SELECTS edges.
func (idx *reachabilityIndex) canReachService(srcPod, serviceID, protocol string, port int) bool {
	if idx == nil || idx.skipped {
		return false
	}
	if _, ok := idx.pods[srcPod]; !ok {
		return false
	}
	svc, ok := idx.services[serviceID]
	if !ok {
		return false
	}
	for _, backing := range svc.BackingPods {
		if idx.canReach(srcPod, backing, protocol, port) {
			return true
		}
	}
	return false
}
