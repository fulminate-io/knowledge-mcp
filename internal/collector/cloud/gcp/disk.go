// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// disksPairIter is the minimal iterator surface Collect needs.
// *compute.DisksScopedListPairIterator satisfies it directly. Tests
// substitute a fake to drive the resilience paths.
type disksPairIter interface {
	Next() (compute.DisksScopedListPair, error)
}

// disksAggregator wraps the single SDK call Collect makes. See
// instancesAggregator on compute.go for the rationale; same pattern.
type disksAggregator interface {
	AggregatedList(ctx context.Context, projectID string) disksPairIter
}

// disksClientAggregator is the production adapter over *compute.DisksClient.
type disksClientAggregator struct {
	client *compute.DisksClient
}

func (a disksClientAggregator) AggregatedList(
	ctx context.Context, projectID string,
) disksPairIter {
	return a.client.AggregatedList(ctx, &computepb.AggregatedListDisksRequest{
		Project: projectID,
	})
}

// diskSubCollector collects Compute Engine persistent disks across all zones.
// Creates the nodes that are targets of BOUND_TO edges emitted by the
// computeSubCollector for attached disks.
type diskSubCollector struct {
	client     *compute.DisksClient
	aggregator disksAggregator
	projectID  string
}

func newDiskSubCollector(client *compute.DisksClient, projectID string) *diskSubCollector {
	return &diskSubCollector{
		client:     client,
		aggregator: disksClientAggregator{client: client},
		projectID:  projectID,
	}
}

func (c *diskSubCollector) Name() string { return "gcp-compute-disks" }

func (c *diskSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
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
				slog.Debug("gcp-compute-disks: project-level permission denied, returning empty",
					"project", c.projectID, "err", err)
				return cloud.SubCollectorResult{}, nil
			case aggregatedIterSkipZone:
				slog.Debug("gcp-compute-disks: zone permission denied, stopping iteration",
					"project", c.projectID, "err", err)
				return result, nil
			default:
				return result, err
			}
		}
		yielded++

		if isZoneUnreachableWarning(pair.Value.GetWarning()) {
			slog.Debug("gcp-compute-disks: zone unreachable",
				"project", c.projectID, "zone", pair.Key,
				"message", pair.Value.GetWarning().GetMessage())
			continue
		}

		result.Resources, result.Edges = appendDiskZone(
			result.Resources, result.Edges, pair.Value.GetDisks())
	}

	return result, nil
}

// appendDiskZone extracts ResourceSpecs and EdgeSpecs from a single zone's
// disks and appends them to the running result accumulators.
func appendDiskZone(
	resources []cloud.ResourceSpec, edges []cloud.EdgeSpec,
	disks []*computepb.Disk,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	for _, disk := range disks {
		selfLink := disk.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(disk)
		if err != nil {
			continue
		}

		resources = append(resources, diskResourceSpec(disk, selfLink, content))
		edges = append(edges, diskEdges(selfLink, disk)...)
	}
	return resources, edges
}

// diskResourceSpec builds a ResourceSpec for a persistent disk.
func diskResourceSpec(disk *computepb.Disk, selfLink string, content []byte) cloud.ResourceSpec {
	region := extractLast(disk.GetZone())
	if region == "" {
		region = extractLast(disk.GetRegion())
	}
	return cloud.ResourceSpec{
		ID:           selfLink,
		Name:         disk.GetName(),
		ResourceType: "gcp:compute:disk",
		Region:       region,
		Content:      content,
		Metadata:     diskMetadata(disk),
	}
}

// diskMetadata builds the metadata map for a Compute Engine persistent disk.
// Returns a fresh map so callers can mutate it freely and so the function is
// trivially testable. Empty values are omitted so downstream queries never
// see e.g. creation_time="" placeholders.
//
// Conventions mirror computeInstanceMetadata:
//   - label/<key>=<value>  — mirrors cloud/k8s/helpers.go:labelsToMeta so
//     dream analyzers and extractLabels work uniformly across clouds.
//
// Disks do not expose tags (tags are an Instance concept), so no tag/ keys
// are emitted. Disks may be zonal OR regional; the helper emits "zone" when
// the disk has a zone URL and "region" when it has a region URL, so the
// Metadata shape distinguishes the two placements explicitly.
func diskMetadata(disk *computepb.Disk) map[string]string {
	m := make(map[string]string, 8)
	if status := disk.GetStatus(); status != "" {
		m["status"] = status
	}
	if dt := extractLast(disk.GetType()); dt != "" {
		m["type"] = dt
	}
	if sz := disk.GetSizeGb(); sz > 0 {
		m["sizeGb"] = strconv.FormatInt(sz, 10)
	}
	if zone := extractLast(disk.GetZone()); zone != "" {
		m["zone"] = zone
	}
	if region := extractLast(disk.GetRegion()); region != "" {
		m["region"] = region
	}
	if ts := disk.GetCreationTimestamp(); ts != "" {
		m["creation_time"] = ts
	}
	for k, v := range disk.GetLabels() {
		m["label/"+k] = v
	}
	return m
}

// diskEdges extracts edges for a persistent disk:
//
//   - EdgeEncryptsWith when CMEK (customer-managed encryption key) is set.
//   - EdgeBoundTo for each instance that has this disk attached (Users field).
//   - EdgeFromSnapshot when the disk was created from a snapshot.
//   - EdgeFromImage when the disk was created from an image.
//
// Both SourceSnapshot and SourceImage may be non-empty simultaneously
// (rare but possible); both edges are emitted in that case.
func diskEdges(selfLink string, disk *computepb.Disk) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// CMEK encryption.
	if key := disk.GetDiskEncryptionKey(); key != nil {
		if kmsKey := key.GetKmsKeyName(); kmsKey != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     kmsKey,
				Relationship: kgtypes.EdgeEncryptsWith,
				Metadata:     map[string]string{"encryption_scope": "disk"},
			})
		}
	}

	// BOUND_TO: disk → instance(s) that have it attached.
	for _, user := range disk.GetUsers() {
		if user != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     user,
				Relationship: kgtypes.EdgeBoundTo,
			})
		}
	}

	// FROM_SNAPSHOT: disk lineage from a snapshot.
	if snap := disk.GetSourceSnapshot(); snap != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     snap,
			Relationship: kgtypes.EdgeFromSnapshot,
		})
	}

	// FROM_IMAGE: disk lineage from an image.
	if img := disk.GetSourceImage(); img != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     img,
			Relationship: kgtypes.EdgeFromImage,
		})
	}

	return edges
}
