// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"golang.org/x/oauth2/google"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&GCPCollector{})
}

// GCPCollector implements collector.Collector for Google Cloud Platform.
// It discovers cloud resources via GCP SDK clients and produces a cloud
// topology graph for a single GCP project.
type GCPCollector struct{}

// Name returns "gcp" — the collector type used for registry lookup.
func (c *GCPCollector) Name() string { return "gcp" }

// Collect discovers GCP resources for the given project ID. If id is empty,
// the project is resolved from Application Default Credentials. Auth is
// always via ADC environment variables.
func (c *GCPCollector) Collect(ctx context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	projectID, creds, err := resolveProject(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("gcp: resolve project: %w", err)
	}

	clients, err := newGCPClients(ctx, creds, projectID)
	if err != nil {
		return nil, fmt.Errorf("gcp: create clients: %w", err)
	}
	defer clients.Close()

	subs := buildSubCollectors(clients, projectID)

	nodes, edges, targets, subErr := cloud.RunSubCollectors(ctx, subs, cloud.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if subErr != nil {
		if len(nodes) == 0 {
			return nil, fmt.Errorf("gcp: all subcollectors failed: %w", subErr)
		}
		slog.Warn("gcp: some subcollectors failed, continuing with partial results",
			"project", projectID, "nodes", len(nodes), "error", subErr)
	}

	// Process cascade targets as side-effects. Each target triggers a full
	// collector.Collect pipeline that writes to its own graph (e.g. k8s).
	cs := cloud.CascadeSetFrom(ctx)
	rm := cloud.ResolutionMapFrom(ctx)
	for _, t := range targets {
		if cs != nil && !cs.Mark(t.Collector, t.ID) {
			continue // already visited
		}
		if rm != nil {
			rm.Record(t.ID, t.ResolutionID)
		}
		if cascadeErr := collector.Collect(ctx, t.Collector, t.ID, opts); cascadeErr != nil {
			slog.Warn("gcp: cascade collection failed",
				"collector", t.Collector, "id", t.ID, "error", cascadeErr)
		}
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: projectID,
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}

// resolveProject determines the GCP project ID. If id is non-empty, it is used
// directly. Otherwise, Application Default Credentials are inspected to extract
// the project from the credentials JSON (quota_project_id or project_id field).
func resolveProject(ctx context.Context, id string) (string, *google.Credentials, error) {
	creds, err := google.FindDefaultCredentials(ctx,
		"https://www.googleapis.com/auth/cloud-platform.read-only",
	)
	if err != nil {
		return "", nil, fmt.Errorf("find default credentials: %w", err)
	}

	if id != "" {
		return id, creds, nil
	}

	// Extract project from credentials JSON.
	projectID := creds.ProjectID
	if projectID == "" {
		// Try parsing the JSON for quota_project_id.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(creds.JSON, &raw); err == nil {
			var qp string
			if err := json.Unmarshal(raw["quota_project_id"], &qp); err == nil && qp != "" {
				projectID = qp
			}
		}
	}

	if projectID == "" {
		return "", nil, fmt.Errorf("no project ID: set GOOGLE_CLOUD_PROJECT or pass project ID explicitly")
	}

	return projectID, creds, nil
}

// buildSubCollectors creates all subcollectors with the given clients and project.
func buildSubCollectors(clients *gcpClients, projectID string) []cloud.SubCollector {
	return []cloud.SubCollector{
		newComputeSubCollector(clients.instances, projectID),
		newNetworksSubCollector(clients.networks, projectID),
		newSubnetsSubCollector(clients.subnets, projectID),
		newFirewallsSubCollector(clients.firewalls, projectID),
		newGKESubCollector(clients.container, clients.iamPolicy, projectID),
		newCloudRunSubCollector(clients.run, clients.iamPolicy, projectID),
		newFunctionsSubCollector(clients.functions, clients.iamPolicy, projectID),
		newSQLSubCollector(clients.sqladmin, projectID),
		newStorageSubCollector(clients.storage, projectID),
		newPubSubTopicsSubCollector(clients.pubsub, projectID),
		newPubSubSubscriptionsSubCollector(clients.pubsub, projectID),
		newSecretsSubCollector(clients.secrets, projectID),
		newIAMSubCollector(clients.iam, clients.resourceManager, projectID),
		newSharedVPCSubCollector(clients.computeProjects, projectID),
		newVPCConnectorSubCollector(clients.vpcConnectors, projectID),
		newArtifactRegistrySubCollector(clients.artifactRegistry, clients.iamPolicy, projectID),
		newForwardingRulesSubCollector(clients.forwardingRules, projectID),
		newTargetHTTPProxiesSubCollector(clients.targetHTTPProxies, projectID),
		newTargetHTTPSProxiesSubCollector(clients.targetHTTPSProxies, projectID),
		newURLMapsSubCollector(clients.urlMaps, projectID),
		newBackendServicesSubCollector(clients.backendServices, projectID),
		newMemorystoreSubCollector(clients.redis, projectID),
		newDNSSubCollector(clients.dns, projectID),
		newCloudArmorSubCollector(clients.securityPolicies, projectID),
		newRouterSubCollector(clients.routers, projectID),
		newTasksSubCollector(clients.cloudTasks, projectID),
		newSchedulerSubCollector(clients.cloudScheduler, projectID),
		newBigQuerySubCollector(clients.bigquery, projectID),
		newLoggingSubCollector(clients.loggingConfig, projectID),
		newInstanceGroupSubCollector(clients.instanceGroups, projectID),
		newSSLCertificatesSubCollector(clients.sslCertificates, projectID),
		newAlertPolicySubCollector(clients.alertPolicies, projectID),
		newEventarcSubCollector(clients.eventarcClient, projectID),
		newKMSSubCollector(clients.kms, clients.iamPolicy, projectID),
		newFirestoreSubCollector(clients.firestore, clients.iamPolicy, projectID),
		newDiskSubCollector(clients.disks, projectID),
		newFilestoreSubCollector(clients.filestore, projectID),
		newWorkflowsSubCollector(clients.workflows, projectID),
		newDataflowSubCollector(clients.dataflow, projectID),
		newIAMGroupsSubCollector(&cloudIdentityGroupsAPI{svc: clients.cloudIdentity}, projectID),
	}
}
