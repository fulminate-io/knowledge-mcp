// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

const methodGCPSharedVPC = "gcp-shared-vpc"

// resolveSharedVPCEdges detects Shared VPC relationships by comparing each
// subnet's parent network project against the current project. When a subnet
// uses a network from a different (host) project, it emits a SHARED_WITH edge
// from the host project's network to the service project's subnet.
//
// Edge direction: FROM host project network TO service project subnet.
// This makes traversal intuitive — walk forward from a host VPC to find all
// service subnets sharing it.
//
// Per decision: edges are written to both the current (service) project's
// graph and the host project's graph when both are collected.
func resolveSharedVPCEdges(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	subnets, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "gcp:compute:subnetwork"},
		"limit": 0,
	})
	if err != nil {
		return err
	}
	if len(subnets) == 0 {
		return nil
	}

	currentProject := detectGCPProject(subnets)
	if currentProject == "" {
		return nil
	}

	var localEdges []knowledgev1.Edge
	remoteEdgesByProject := make(map[string][]knowledgev1.Edge)

	for _, subnet := range subnets {
		networkSelfLink := extractNetworkFromSubnet(subnet)
		if networkSelfLink == "" {
			continue
		}
		networkProject := extractProjectFromSelfLink(networkSelfLink)
		if networkProject == "" || networkProject == currentProject {
			continue // same-project subnet — not Shared VPC
		}

		localEdges = append(localEdges, knowledgev1.Edge{
			FromId: networkSelfLink,
			ToId:   subnet.Id,
			Type:   string(kgtypes.EdgeSharedWith),
			Method: methodGCPSharedVPC,
		})
		remoteEdgesByProject[networkProject] = append(remoteEdgesByProject[networkProject], knowledgev1.Edge{
			FromId: networkSelfLink,
			ToId:   subnet.Id,
			Type:   string(kgtypes.EdgeSharedWith),
			Method: methodGCPSharedVPC,
		})
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, localEdges); err != nil {
		return err
	}

	// Write edges to host project graphs (bidirectional per decision).
	writeSharedVPCRemoteEdges(ctx, gc, remoteEdgesByProject)

	slog.Debug("gcp shared-vpc: emitted edges",
		"local", len(localEdges), "remote_projects", len(remoteEdgesByProject))
	return nil
}

// writeSharedVPCRemoteEdges writes SHARED_WITH edges to each host project's graph
// over the wire (one LinkEdgesBatch per host project, routed by Target.Account).
// A host project that was not collected simply has no backing graph; the write
// is best-effort (slog.Warn) so a missing host never fails the local pass.
func writeSharedVPCRemoteEdges(ctx context.Context, gc postpopulate.GraphCaller, edgesByProject map[string][]knowledgev1.Edge) {
	for project, edges := range edgesByProject {
		if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, project, edges); err != nil {
			slog.Warn("gcp shared-vpc: remote link failed", "project", project, "err", err)
		}
	}
}

// detectGCPProject extracts the project ID from the first subnet self-link.
// GCP self-links follow: https://www.googleapis.com/compute/v1/projects/{PROJECT}/...
func detectGCPProject(subnets []*knowledgev1.Node) string {
	for _, s := range subnets {
		if p := extractProjectFromSelfLink(s.Id); p != "" {
			return p
		}
	}
	return ""
}

// extractNetworkFromSubnet parses the subnet node's Content JSON to get the
// 'network' field (parent VPC self-link). Unmarshals into the curated wire
// struct subnetworkContent (defined in network.go) — FUL-88 reader convergence.
func extractNetworkFromSubnet(subnet *knowledgev1.Node) string {
	if subnet.Content == "" {
		return ""
	}
	var sn subnetworkContent
	if err := json.Unmarshal([]byte(subnet.Content), &sn); err != nil {
		return ""
	}
	return sn.Network
}

// extractProjectFromSelfLink extracts the project ID from a GCP self-link.
// Format: https://www.googleapis.com/compute/v1/projects/{PROJECT}/...
func extractProjectFromSelfLink(selfLink string) string {
	const marker = "/projects/"
	_, after, ok := strings.Cut(selfLink, marker)
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, "/"); ok {
		return before
	}
	return rest
}
