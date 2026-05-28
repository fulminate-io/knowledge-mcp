// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveDNSRecordTargets rewrites dangling DNS ROUTES_TO edges that point at
// raw IP addresses to the resolved Azure resource ID. The DNS collector emits
// ROUTES_TO for A/AAAA/CNAME records with raw rdata values as target IDs. This
// PostPopulate pass builds an IP index from VMs and LBs, then rewrites matches.
func resolveDNSRecordTargets(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	ipIndex, err := buildAzureIPIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("azure dns resolve: build IP index: %w", err)
	}
	if len(ipIndex) == 0 {
		return nil
	}

	records, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "Microsoft.Network/dnsZones/recordSets"},
		"limit": 0,
	})
	if err != nil {
		return fmt.Errorf("azure dns resolve: query records: %w", err)
	}

	var (
		removals []azureDNSRemoval
		newEdges []knowledgev1.Edge
	)

	for _, rec := range records {
		edges, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, rec.Id, postpopulate.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeRoutesTo})
		if err != nil {
			slog.Warn("azure dns resolve: browse edges failed", "record", rec.Id, "err", err)
			continue
		}
		for i := range edges {
			e := &edges[i]
			if isAzureResourceID(e.ToId) {
				continue // already resolved
			}
			resolved := ipIndex[e.ToId]
			if resolved == "" {
				continue
			}
			removals = append(removals, azureDNSRemoval{
				fromID: e.FromId, toID: e.ToId, edgeType: kgtypes.EdgeType(e.Type),
			})
			newEdges = append(newEdges, knowledgev1.Edge{
				FromId:   e.FromId,
				ToId:     resolved,
				Type:     string(kgtypes.EdgeRoutesTo),
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
			slog.Warn("azure dns resolve: remove edge failed",
				"from", r.fromID, "to", r.toID, "err", err)
		}
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, newEdges); err != nil {
		return fmt.Errorf("azure dns resolve: link batch: %w", err)
	}
	slog.Debug("azure postPopulate: resolved DNS record targets", "count", len(newEdges))
	return nil
}

type azureDNSRemoval struct {
	fromID, toID string
	edgeType     kgtypes.EdgeType
}

// isAzureResourceID returns true if the ID looks like an already-resolved
// Azure resource path (/subscriptions/...).
func isAzureResourceID(id string) bool {
	return strings.HasPrefix(id, "/subscriptions/")
}

// buildAzureIPIndex queries VMs and LBs, extracting IPs from their Content.
func buildAzureIPIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]string, error) {
	index := make(map[string]string)
	if err := indexLBFrontendIPs(ctx, gc, graphName, index); err != nil {
		return nil, err
	}
	return index, nil
}

// indexLBFrontendIPs extracts private IPs from Azure Load Balancer frontend
// configurations stored in Content JSON.
func indexLBFrontendIPs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, index map[string]string) error {
	lbs, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "Microsoft.Network/loadBalancers"},
		"limit": 0,
	})
	if err != nil {
		return err
	}
	for _, n := range lbs {
		for _, ip := range parseLBFrontendIPs(n.Content) {
			index[ip] = n.Id
		}
	}
	return nil
}

type azureLBContent struct {
	Properties *struct {
		FrontendIPConfigurations []struct {
			Properties *struct {
				PrivateIPAddress *string `json:"privateIPAddress"`
			} `json:"properties"`
		} `json:"frontendIPConfigurations"`
	} `json:"properties"`
}

func parseLBFrontendIPs(content string) []string {
	if content == "" {
		return nil
	}
	var lb azureLBContent
	if err := json.Unmarshal([]byte(content), &lb); err != nil || lb.Properties == nil {
		return nil
	}
	var ips []string
	for _, fip := range lb.Properties.FrontendIPConfigurations {
		if fip.Properties == nil || fip.Properties.PrivateIPAddress == nil {
			continue
		}
		ip := *fip.Properties.PrivateIPAddress
		if net.ParseIP(ip) != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}
