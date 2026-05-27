// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveDNSRecordTargets emits ROUTES_TO edges from DNS record nodes to the
// resolved GCP resource that hosts the rdata IP. The GCP DNS collector
// deliberately does NOT emit any ROUTES_TO edges itself — they're produced
// here against a typed IP index so dangling-string edges never enter the
// graph. CNAMEs and other hostname-valued records are skipped today; a
// hostname index can be added when LB/Cloud Run domain mappings are wired.
func resolveDNSRecordTargets(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	ipIndex, err := buildGCPIPIndex(ctx, gc, graphName)
	if err != nil {
		return fmt.Errorf("gcp dns resolve: build IP index: %w", err)
	}
	if len(ipIndex) == 0 {
		return nil
	}

	records, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "gcp:dns:recordSet"},
		"limit": 0,
	})
	if err != nil {
		return fmt.Errorf("gcp dns resolve: query records: %w", err)
	}

	var newEdges []knowledgev1.Edge
	for _, rec := range records {
		newEdges = append(newEdges, dnsRecordEdgesFromContent(rec.Id, rec.Content, ipIndex)...)
	}

	if len(newEdges) == 0 {
		return nil
	}
	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, newEdges); err != nil {
		return fmt.Errorf("gcp dns resolve: link batch: %w", err)
	}
	slog.Debug("gcp postPopulate: resolved DNS record targets", "count", len(newEdges))
	return nil
}

// dnsRecordEdgesFromContent parses a recordSet's Content blob and emits
// one ROUTES_TO edge per (rdata, resolved target) pair. A single IP may be
// shared by multiple resources (regional+global forwarding rules behind a
// reserved Address, anycast ILBs, ephemeral→static IP transitions); emit
// every match rather than picking one arbitrarily. Records whose rdata
// don't resolve produce no edge.
func dnsRecordEdgesFromContent(recordID, content string, ipIndex map[string][]string) []knowledgev1.Edge {
	if content == "" {
		return nil
	}
	var rs resourceRecordSetContent
	if err := json.Unmarshal([]byte(content), &rs); err != nil {
		return nil
	}
	if rs.Type != "A" && rs.Type != "AAAA" {
		return nil
	}

	var edges []knowledgev1.Edge
	for _, rdata := range rs.Rrdatas {
		ip := strings.TrimSuffix(rdata, ".")
		for _, resolved := range ipIndex[ip] {
			edges = append(edges, knowledgev1.Edge{
				FromId:   recordID,
				ToId:     resolved,
				Type:     string(kgtypes.EdgeRoutesTo),
				Method:   "postpopulate:dns-resolve",
				Evidence: ip,
			})
		}
	}
	return edges
}

// isGCPResourcePath returns true if the ID looks like an already-resolved GCP
// resource path (projects/...).
func isGCPResourcePath(id string) bool {
	return strings.HasPrefix(id, "projects/")
}

// buildGCPIPIndex queries GCE instances, Cloud SQL instances, and forwarding
// rules, extracting public IPs from their Content JSON. Returns a multi-map
// because a single IP may be referenced by multiple resources (regional +
// global forwarding rules pointing at the same reserved Address, anycast
// ILBs, ephemeral→static IP transitions during which both nodes coexist).
func buildGCPIPIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string][]string, error) {
	index := make(map[string][]string)

	if err := indexGCEIPs(ctx, gc, graphName, index); err != nil {
		return nil, err
	}
	if err := indexSQLIPs(ctx, gc, graphName, index); err != nil {
		return nil, err
	}
	indexForwardingRuleIPs(ctx, gc, graphName, index)
	return index, nil
}

// addIPMapping appends nodeID to the index entry for ip, deduping so the
// same (ip, nodeID) pair from a re-collection run doesn't double up. Logs
// at debug when an IP collides across distinct nodes — operators should
// know when one A record will route to multiple resources.
func addIPMapping(index map[string][]string, ip, nodeID string) {
	if slices.Contains(index[ip], nodeID) {
		return
	}
	if len(index[ip]) > 0 {
		slog.Debug("gcp dns resolve: shared IP across resources",
			"ip", ip, "existing", index[ip], "new", nodeID)
	}
	index[ip] = append(index[ip], nodeID)
}

// indexGCEIPs extracts external IPs from GCE instance Content JSON.
func indexGCEIPs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, index map[string][]string) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "gcp:compute:instance"},
		"limit": 0,
	})
	if err != nil {
		return err
	}
	for _, n := range nodes {
		for _, ip := range parseGCEInstanceIPs(n.Content) {
			addIPMapping(index, ip, n.Id)
		}
	}
	return nil
}

type gceInstanceContent struct {
	NetworkInterfaces []struct {
		AccessConfigs []struct {
			NatIP string `json:"natIP"`
		} `json:"accessConfigs"`
	} `json:"networkInterfaces"`
}

func parseGCEInstanceIPs(content string) []string {
	if content == "" {
		return nil
	}
	var inst gceInstanceContent
	if err := json.Unmarshal([]byte(content), &inst); err != nil {
		return nil
	}
	var ips []string
	for _, nic := range inst.NetworkInterfaces {
		for _, ac := range nic.AccessConfigs {
			if ac.NatIP != "" && net.ParseIP(ac.NatIP) != nil {
				ips = append(ips, ac.NatIP)
			}
		}
	}
	return ips
}

// indexSQLIPs extracts IPs from Cloud SQL instance Content JSON.
func indexSQLIPs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, index map[string][]string) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "gcp:sql:instance"},
		"limit": 0,
	})
	if err != nil {
		return err
	}
	for _, n := range nodes {
		for _, ip := range parseSQLInstanceIPs(n.Content) {
			addIPMapping(index, ip, n.Id)
		}
	}
	return nil
}

type sqlInstanceContent struct {
	IpAddresses []struct {
		IpAddress string `json:"ipAddress"`
	} `json:"ipAddresses"`
}

func parseSQLInstanceIPs(content string) []string {
	if content == "" {
		return nil
	}
	var inst sqlInstanceContent
	if err := json.Unmarshal([]byte(content), &inst); err != nil {
		return nil
	}
	var ips []string
	for _, addr := range inst.IpAddresses {
		if addr.IpAddress != "" && net.ParseIP(addr.IpAddress) != nil {
			ips = append(ips, addr.IpAddress)
		}
	}
	return ips
}

// indexForwardingRuleIPs extracts IPs from forwarding rule Content JSON.
func indexForwardingRuleIPs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, index map[string][]string) {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "gcp:compute:forwardingRule"},
		"limit": 0,
	})
	if err != nil {
		return // best-effort
	}
	for _, n := range nodes {
		if ip := parseForwardingRuleIP(n.Content); ip != "" {
			addIPMapping(index, ip, n.Id)
		}
	}
}

// parseForwardingRuleIP Unmarshals into the forwardingRuleContent wire struct
// defined in loadbalancer_wire.go (FUL-88 reader convergence — the Phase 2
// wire struct is the superset of the IP-only shape this function needs).
func parseForwardingRuleIP(content string) string {
	if content == "" {
		return ""
	}
	var fr forwardingRuleContent
	if err := json.Unmarshal([]byte(content), &fr); err != nil {
		return ""
	}
	if net.ParseIP(fr.IPAddress) != nil {
		return fr.IPAddress
	}
	return ""
}
