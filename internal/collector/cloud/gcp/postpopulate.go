// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// postPopulate is the GCP CollectResult.PostPopulate hook. It runs after all
// subcollector nodes and raw attachment edges have been written to the graph
// and derives higher-level structural edges that require cross-node lookups:
//
//  1. Firewall rule pre-resolution -- parses each firewall node's Content JSON,
//     builds an instance index by network tags and service accounts, matches
//     firewalls to target/source instances, and emits directional
//     EdgeAllowsIngressFrom / EdgeAllowsEgressTo edges between instances and
//     CIDR sentinels (see postpopulate_firewall.go).
//
// Helpers tolerate missing prerequisite data and log-and-continue rather than
// returning hard errors so postPopulate never fails the whole collection for a
// partial gap. Each helper is independently unit-tested.
func postPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	if err := resolveFirewallRules(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveFirewallRules failed", "err", err)
	}
	if err := resolveSharedVPCEdges(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveSharedVPCEdges failed", "err", err)
	}
	// Cloud Run image lineage: match container images against AR repos.
	if err := resolveCloudRunImageLineage(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveCloudRunImageLineage failed", "err", err)
	}
	// DNS record target resolution: rewrite raw IP targets to resource paths.
	if err := resolveDNSRecordTargets(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveDNSRecordTargets failed", "err", err)
	}
	// Cross-project trust: detect SA impersonation across GCP projects.
	if err := resolveCrossProjectTrust(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveCrossProjectTrust failed", "err", err)
	}
	// IAM binding group resolution: resolve group: targets to group node IDs.
	if err := resolveIAMBindingGroups(ctx, gc, graphName); err != nil {
		slog.Warn("gcp postPopulate: resolveIAMBindingGroups failed", "err", err)
	}
	return nil
}
