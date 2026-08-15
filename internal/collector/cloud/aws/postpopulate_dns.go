// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveRoute53AliasTargets rewrites dangling Route53 TARGETS edges that
// point at raw DNS hostnames (e.g. "my-lb-123.us-east-1.elb.amazonaws.com")
// to the resolved ARN of the ELB or CloudFront distribution node in the graph.
//
// The Route53 collector emits DNS names when it can't derive an ARN at
// collection time. This PostPopulate pass builds an index of DNS-name-to-ARN
// from the already-collected ELBv2 load balancer nodes and rewrites matches.
func resolveRoute53AliasTargets(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	dnsIndex, err := buildELBDNSIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("route53 dns resolve: build index: %w", err)
	}
	if len(dnsIndex) == 0 {
		return nil
	}

	zones, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "route53-hostedzone"},
	})
	if err != nil {
		return fmt.Errorf("route53 dns resolve: query zones: %w", err)
	}

	var (
		removals []edgeRemoval
		newEdges []knowledgev1.Edge
	)

	for _, zone := range zones {
		edges, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, zone.Id, postpopulate.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeTargets})
		if err != nil {
			slog.Warn("route53 dns resolve: browse edges failed", "zone", zone.Id, "err", err)
			continue
		}
		for i := range edges {
			e := &edges[i]
			resolved := matchDNSTarget(e.ToId, dnsIndex)
			if resolved == "" {
				continue
			}
			removals = append(removals, edgeRemoval{
				fromID:   e.FromId,
				toID:     e.ToId,
				edgeType: kgtypes.EdgeType(e.Type),
			})
			newEdges = append(newEdges, knowledgev1.Edge{
				FromId:   e.FromId,
				ToId:     resolved,
				Type:     string(kgtypes.EdgeTargets),
				Method:   "postpopulate:dns-resolve",
				Evidence: e.ToId,
			})
		}
	}

	if len(newEdges) == 0 {
		return nil
	}

	for _, r := range removals {
		if err := postpopulate.UnlinkEdge(ctx, gc, kgtypes.GraphCloud, graphName, r.fromID, r.toID, r.edgeType); err != nil {
			slog.Warn("route53 dns resolve: remove edge failed", "from", r.fromID, "to", r.toID, "err", err)
		}
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, newEdges); err != nil {
		return fmt.Errorf("route53 dns resolve: link batch: %w", err)
	}
	slog.Debug("aws postPopulate: resolved Route53 DNS aliases", "count", len(newEdges))
	return nil
}

type edgeRemoval struct {
	fromID   string
	toID     string
	edgeType kgtypes.EdgeType
}

// elbContentDNS is a minimal struct for parsing the DNSName field from the
// ELBv2 DescribeLoadBalancers response stored in node Content.
type elbContentDNS struct {
	DNSName string `json:"DNSName"`
}

// buildELBDNSIndex queries all elbv2-loadbalancer nodes and returns a map
// from lowercase DNS name to the node's ARN (ID).
func buildELBDNSIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]string, error) {
	lbs, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "elbv2-loadbalancer"},
	})
	if err != nil {
		return nil, err
	}
	index := make(map[string]string, len(lbs))
	for _, n := range lbs {
		var c elbContentDNS
		if err := json.Unmarshal([]byte(n.Content), &c); err != nil || c.DNSName == "" {
			continue
		}
		index[strings.ToLower(c.DNSName)] = n.Id
	}
	return index, nil
}

// matchDNSTarget checks whether a TARGETS edge ToID is a resolvable DNS
// hostname. Returns the resolved ARN if found, or "" if the target doesn't
// match the index (i.e. it's not an ELB/CloudFront DNS name).
func matchDNSTarget(toID string, dnsIndex map[string]string) string {
	lower := strings.ToLower(toID)
	if !isDNSHostname(lower) {
		return ""
	}
	return dnsIndex[lower]
}

// isDNSHostname returns true if the target looks like a dangling DNS hostname
// rather than an already-resolved ARN. We check for known AWS DNS patterns.
func isDNSHostname(lower string) bool {
	return strings.Contains(lower, "elb.amazonaws.com") ||
		strings.Contains(lower, "elasticloadbalancing") ||
		strings.HasSuffix(lower, ".cloudfront.net")
}
