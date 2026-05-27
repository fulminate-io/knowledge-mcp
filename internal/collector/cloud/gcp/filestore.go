// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"strconv"

	filestore "cloud.google.com/go/filestore/apiv1"
	"cloud.google.com/go/filestore/apiv1/filestorepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// filestoreSubCollector collects Cloud Filestore instances across all locations.
type filestoreSubCollector struct {
	client    *filestore.CloudFilestoreManagerClient
	projectID string
}

func newFilestoreSubCollector(client *filestore.CloudFilestoreManagerClient, projectID string) *filestoreSubCollector {
	return &filestoreSubCollector{client: client, projectID: projectID}
}

func (c *filestoreSubCollector) Name() string { return "gcp-filestore" }

func (c *filestoreSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	parent := "projects/" + c.projectID + "/locations/-"
	it := c.client.ListInstances(ctx, &filestorepb.ListInstancesRequest{Parent: parent})
	for {
		inst, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}
		name := inst.GetName()
		if name == "" {
			continue
		}

		content, _ := json.Marshal(inst) //nolint:errchkjson // best-effort content envelope
		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:file:instance",
			Region:       filestoreLocationFromName(name),
			Content:      content,
			Metadata:     filestoreInstanceMetadata(inst),
		})
		result.Edges = append(result.Edges, filestoreInstanceEdges(name, inst)...)
	}

	return result, nil
}

// filestoreInstanceMetadata extracts searchable metadata from a Filestore instance.
func filestoreInstanceMetadata(inst *filestorepb.Instance) map[string]string {
	meta := map[string]string{
		"tier":  inst.GetTier().String(),
		"state": inst.GetState().String(),
	}
	if shares := inst.GetFileShares(); len(shares) > 0 {
		meta["fileShareName"] = shares[0].GetName()
		meta["capacityGb"] = strconv.FormatInt(shares[0].GetCapacityGb(), 10)
	}
	if p := inst.GetProtocol(); p != filestorepb.Instance_FILE_PROTOCOL_UNSPECIFIED {
		meta["protocol"] = p.String()
	}
	return meta
}

// filestoreInstanceEdges emits USES_NETWORK edges to VPCs and ENCRYPTS_WITH
// edges to KMS keys when CMEK is configured.
func filestoreInstanceEdges(instanceID string, inst *filestorepb.Instance) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	for _, nc := range inst.GetNetworks() {
		if net := nc.GetNetwork(); net != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     instanceID,
				TargetID:     filestoreNetworkResourceName(instanceID, net),
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	if key := inst.GetKmsKeyName(); key != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     instanceID,
			TargetID:     key,
			Relationship: kgtypes.EdgeEncryptsWith,
			Metadata:     map[string]string{"encryption_scope": "instance"},
		})
	}

	return edges
}

// filestoreLocationFromName extracts the location from a Filestore instance
// name of the form "projects/P/locations/L/instances/I".
func filestoreLocationFromName(name string) string {
	return kmsLocationFromName(name)
}

// filestoreNetworkResourceName resolves a Filestore network reference to a
// full VPC network resource name. Filestore stores networks as short names
// (e.g. "default"), so we expand them to the canonical project-scoped form.
func filestoreNetworkResourceName(instanceID, network string) string {
	if network == "" {
		return ""
	}
	// Already a full resource name.
	if len(network) > len("projects/") && network[:len("projects/")] == "projects/" {
		return network
	}
	// Extract the project ID from the instance name
	// (projects/P/locations/L/instances/I → P).
	projectID := filestoreProjectFromInstanceName(instanceID)
	if projectID == "" {
		return network
	}
	return "projects/" + projectID + "/global/networks/" + network
}

// filestoreProjectFromInstanceName extracts the project ID from a resource
// name of the form "projects/P/...".
func filestoreProjectFromInstanceName(name string) string {
	const prefix = "projects/"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return ""
	}
	rest := name[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i]
		}
	}
	return rest
}
