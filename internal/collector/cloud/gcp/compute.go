// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// instancesPairIter is the minimal iterator surface Collect needs from the
// compute SDK. *compute.InstancesScopedListPairIterator satisfies it
// directly. Tests substitute a fake to drive the resilience paths
// (per-zone 403, project-level 403, UNREACHABLE warning, generic error).
type instancesPairIter interface {
	Next() (compute.InstancesScopedListPair, error)
}

// instancesAggregator wraps the single SDK call Collect makes. Production
// uses a thin adapter over *compute.InstancesClient; tests inject a fake
// returning a fake instancesPairIter. Kept unexported — this is a test
// seam, not API surface.
type instancesAggregator interface {
	AggregatedList(ctx context.Context, projectID string) instancesPairIter
}

// instancesClientAggregator is the production adapter that forwards to the
// real SDK client.
type instancesClientAggregator struct {
	client *compute.InstancesClient
}

func (a instancesClientAggregator) AggregatedList(
	ctx context.Context, projectID string,
) instancesPairIter {
	return a.client.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{
		Project: projectID,
	})
}

// computeSubCollector collects GCE instances across all zones.
type computeSubCollector struct {
	client     *compute.InstancesClient
	aggregator instancesAggregator
	projectID  string
}

func newComputeSubCollector(client *compute.InstancesClient, projectID string) *computeSubCollector {
	return &computeSubCollector{
		client:     client,
		aggregator: instancesClientAggregator{client: client},
		projectID:  projectID,
	}
}

func (c *computeSubCollector) Name() string { return "gcp-compute-instances" }

func (c *computeSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.aggregator.AggregatedList(ctx, c.projectID)

	yielded := 0
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			switch classifyAggregatedIterErr(err, yielded) {
			case aggregatedIterEmpty:
				slog.Debug("gcp-compute-instances: project-level permission denied, returning empty",
					"project", c.projectID, "err", err)
				return cloud.SubCollectorResult{}, nil
			case aggregatedIterSkipZone:
				slog.Debug("gcp-compute-instances: zone permission denied, stopping iteration",
					"project", c.projectID, "err", err)
				return result, nil
			default:
				return result, err
			}
		}
		yielded++

		// Skip unreachable zones announced via the partial-success warning.
		if isZoneUnreachableWarning(pair.Value.GetWarning()) {
			slog.Debug("gcp-compute-instances: zone unreachable",
				"project", c.projectID, "zone", pair.Key,
				"message", pair.Value.GetWarning().GetMessage())
			continue
		}

		result.Resources, result.Edges = c.appendZone(
			result.Resources, result.Edges, pair.Value.GetInstances())
	}

	return result, nil
}

// appendZone extracts ResourceSpecs and EdgeSpecs from a single zone's
// instances and appends them to the running result accumulators.
func (c *computeSubCollector) appendZone(
	resources []cloud.ResourceSpec, edges []cloud.EdgeSpec,
	instances []*computepb.Instance,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	for _, inst := range instances {
		selfLink := inst.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(inst)
		if err != nil {
			continue
		}

		resources = append(resources, computeResourceSpec(selfLink, inst, content))
		edges = append(edges, computeEdges(c.projectID, selfLink, inst)...)
	}
	return resources, edges
}

// computeResourceSpec builds a ResourceSpec for a Compute Engine instance.
func computeResourceSpec(selfLink string, inst *computepb.Instance, content []byte) cloud.ResourceSpec {
	return cloud.ResourceSpec{
		ID:           selfLink,
		Name:         inst.GetName(),
		ResourceType: "gcp:compute:instance",
		Region:       extractZone(inst.GetZone()),
		Content:      content,
		Metadata:     computeInstanceMetadata(inst),
	}
}

// computeInstanceMetadata builds the metadata map for a Compute Engine
// instance. Returns a fresh map so callers can mutate it freely and so the
// function is trivially testable. Empty values are omitted so downstream
// queries never see e.g. creation_time="" placeholders.
//
// Conventions:
//   - label/<key>=<value>   — mirrors cloud/k8s/helpers.go:labelsToMeta so
//     dream analyzers and extractLabels work uniformly across clouds.
//   - tag/<name>=""         — presence of the key is the signal; empty value
//     keeps symmetry with the label/ prefix for search.
//   - primary_ip / external_ip — first NIC only; subnet/network remain as
//     edges.
//
// SA emails are intentionally NOT duplicated here — USES_SA edges are the
// canonical representation.
func computeInstanceMetadata(inst *computepb.Instance) map[string]string {
	m := make(map[string]string, 8)
	if status := inst.GetStatus(); status != "" {
		m["status"] = status
	}
	if mt := extractLast(inst.GetMachineType()); mt != "" {
		m["machineType"] = mt
	}
	if zone := extractZone(inst.GetZone()); zone != "" {
		m["zone"] = zone
	}
	if ts := inst.GetCreationTimestamp(); ts != "" {
		m["creation_time"] = ts
	}
	for k, v := range inst.GetLabels() {
		m["label/"+k] = v
	}
	// GetTags() returns a nil-safe *Tags; GetItems() on nil returns nil.
	for _, t := range inst.GetTags().GetItems() {
		if t == "" {
			continue
		}
		m["tag/"+t] = ""
	}
	// Primary NIC only (first interface). Subnet/network are already edges.
	if nics := inst.GetNetworkInterfaces(); len(nics) > 0 {
		nic := nics[0]
		if ip := nic.GetNetworkIP(); ip != "" {
			m["primary_ip"] = ip
		}
		// External IP = first non-empty NatIP on first AccessConfig.
		for _, ac := range nic.GetAccessConfigs() {
			if nat := ac.GetNatIP(); nat != "" {
				m["external_ip"] = nat
				break
			}
		}
	}
	return m
}

// computeEdges extracts edges for a Compute Engine instance: network interfaces
// and service accounts.
func computeEdges(projectID, selfLink string, inst *computepb.Instance) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges for each network interface.
	for _, nic := range inst.GetNetworkInterfaces() {
		if subnet := nic.GetSubnetwork(); subnet != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     subnet,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
		if network := nic.GetNetwork(); network != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     network,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	// Edge for attached service accounts.
	for _, sa := range inst.GetServiceAccounts() {
		if email := sa.GetEmail(); email != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     saResourceName(projectID, email),
				Relationship: kgtypes.EdgeUsesSA,
			})
		}
	}

	// BOUND_TO edges for attached persistent disks.
	for _, disk := range inst.GetDisks() {
		if source := disk.GetSource(); source != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     source,
				TargetID:     selfLink,
				Relationship: kgtypes.EdgeBoundTo,
			})
		}
	}

	return edges
}

// extractZone extracts the zone name from a full zone URL.
// e.g. "projects/p/zones/us-central1-a" -> "us-central1-a"
func extractZone(zoneURL string) string {
	return extractLast(zoneURL)
}

// extractLast returns the last path segment of a URL-like string.
// e.g. "projects/p/zones/us-central1-a" -> "us-central1-a"
func extractLast(urlPath string) string {
	if i := strings.LastIndex(urlPath, "/"); i >= 0 {
		return urlPath[i+1:]
	}
	return urlPath
}

// saResourceName builds the full IAM service account resource name from an email.
func saResourceName(projectID, email string) string {
	return "projects/" + projectID + "/serviceAccounts/" + email
}
