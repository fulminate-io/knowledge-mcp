// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// postPopulate is the Azure CollectResult.PostPopulate hook. It runs after all
// subcollector nodes and raw attachment edges have been written to the graph
// and derives higher-level structural edges that require cross-node lookups:
//
//  1. NSG rule pre-resolution -- re-parses each NSG node's SecurityRules JSON
//     and emits directional EdgeAllowsIngressFrom / EdgeAllowsEgressTo edges
//     for CIDR-based rules (see postpopulate_nsg.go).
//
// Helpers tolerate missing prerequisite data and log-and-continue rather than
// returning hard errors so postPopulate never fails the whole collection for a
// partial gap. Each helper is independently unit-tested.
func postPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	if err := resolveNSGRules(ctx, gc, graphName); err != nil {
		slog.Warn("azure postPopulate: resolveNSGRules failed", "err", err)
	}
	// DNS record target resolution: rewrite raw IP targets to resource IDs.
	if err := resolveDNSRecordTargets(ctx, gc, graphName); err != nil {
		slog.Warn("azure postPopulate: resolveDNSRecordTargets failed", "err", err)
	}
	// Image lineage: match App Service/Functions container images to ACR.
	if err := resolveAzureImageLineage(ctx, gc, graphName); err != nil {
		slog.Warn("azure postPopulate: resolveAzureImageLineage failed", "err", err)
	}
	// Cross-tenant trust: detect guest/foreign principals and external federations.
	if err := resolveCrossTenantTrust(ctx, gc, graphName); err != nil {
		slog.Warn("azure postPopulate: resolveCrossTenantTrust failed", "err", err)
	}
	// AAD group resolution: link role assignments/access policies to group nodes.
	if err := resolveAADGroupAssignments(ctx, gc, graphName); err != nil {
		slog.Warn("azure postPopulate: resolveAADGroupAssignments failed", "err", err)
	}
	return nil
}
