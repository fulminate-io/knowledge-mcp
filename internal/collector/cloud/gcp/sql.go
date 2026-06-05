// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"

	sqladmin "google.golang.org/api/sqladmin/v1beta4"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sqlSubCollector collects Cloud SQL instances.
// Uses the REST-based google.golang.org/api (not gRPC) per the design decision.
type sqlSubCollector struct {
	service   *sqladmin.Service
	projectID string
}

func newSQLSubCollector(service *sqladmin.Service, projectID string) *sqlSubCollector {
	return &sqlSubCollector{service: service, projectID: projectID}
}

func (c *sqlSubCollector) Name() string { return "gcp-cloud-sql" }

func (c *sqlSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		call := c.service.Instances.List(c.projectID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return result, err
		}

		for _, inst := range resp.Items {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			appendSQLInstance(inst, &result)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return result, nil
}

// appendSQLInstance converts a Cloud SQL instance into resource + edges and
// appends them to result. No-op when SelfLink is empty or content fails to
// marshal.
func appendSQLInstance(inst *sqladmin.DatabaseInstance, result *cloud.SubCollectorResult) {
	selfLink := inst.SelfLink
	if selfLink == "" {
		return
	}
	content, err := json.Marshal(inst)
	if err != nil {
		return
	}

	tier := ""
	if inst.Settings != nil {
		tier = inst.Settings.Tier
	}
	result.Resources = append(result.Resources, cloud.ResourceSpec{
		ID:           selfLink,
		Name:         inst.Name,
		ResourceType: "gcp:sql:instance",
		Region:       inst.Region,
		Content:      content,
		Metadata: map[string]string{
			"databaseVersion": inst.DatabaseVersion,
			"state":           inst.State,
			"tier":            tier,
			"backendType":     inst.BackendType,
		},
	})

	if inst.DiskEncryptionConfiguration != nil && inst.DiskEncryptionConfiguration.KmsKeyName != "" {
		result.Edges = append(result.Edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     inst.DiskEncryptionConfiguration.KmsKeyName,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	if inst.Settings != nil && inst.Settings.IpConfiguration != nil {
		if network := inst.Settings.IpConfiguration.PrivateNetwork; network != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     network,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}
}
