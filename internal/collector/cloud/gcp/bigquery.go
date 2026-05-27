// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	bq "google.golang.org/api/bigquery/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// bigquerySubCollector collects BigQuery datasets and tables.
// Uses the REST-based google.golang.org/api (same pattern as dns, sqladmin).
type bigquerySubCollector struct {
	service   *bq.Service
	projectID string
}

func newBigQuerySubCollector(service *bq.Service, projectID string) *bigquerySubCollector {
	return &bigquerySubCollector{service: service, projectID: projectID}
}

func (c *bigquerySubCollector) Name() string { return "gcp-bigquery" }

func (c *bigquerySubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	datasetIDs, err := c.listDatasetIDs(ctx)
	if err != nil {
		return result, err
	}

	for _, dsID := range datasetIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := c.collectDataset(ctx, dsID, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// listDatasetIDs returns all dataset IDs in the project.
func (c *bigquerySubCollector) listDatasetIDs(ctx context.Context) ([]string, error) {
	var ids []string

	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := c.service.Datasets.List(c.projectID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("bigquery: list datasets: %w", err)
		}

		for _, ds := range resp.Datasets {
			if ds.DatasetReference != nil && ds.DatasetReference.DatasetId != "" {
				ids = append(ids, ds.DatasetReference.DatasetId)
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return ids, nil
}

// collectDataset fetches a single dataset, builds its resource spec and edges,
// then lists tables within it.
func (c *bigquerySubCollector) collectDataset(
	ctx context.Context, datasetID string, result *cloud.SubCollectorResult,
) error {
	ds, err := c.service.Datasets.Get(c.projectID, datasetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("bigquery: get dataset %s: %w", datasetID, err)
	}

	dsResourceID := fmt.Sprintf("projects/%s/datasets/%s", c.projectID, datasetID)

	content, _ := json.Marshal(ds) //nolint:errchkjson // best-effort content envelope
	spec := cloud.ResourceSpec{
		ID:           dsResourceID,
		Name:         datasetID,
		ResourceType: "gcp:bigquery:dataset",
		Region:       ds.Location,
		Content:      content,
		Metadata: map[string]string{
			"location": ds.Location,
		},
	}
	if ds.DefaultTableExpirationMs != 0 {
		spec.Metadata["defaultTableExpirationMs"] = strconv.FormatInt(
			ds.DefaultTableExpirationMs, 10)
	}
	result.Resources = append(result.Resources, spec)

	result.Edges = append(result.Edges, bigqueryDatasetEdges(dsResourceID, ds)...)

	return c.collectTables(ctx, datasetID, dsResourceID, result)
}

// collectTables lists tables in a dataset and appends resources + edges.
func (c *bigquerySubCollector) collectTables(
	ctx context.Context, datasetID, dsResourceID string,
	result *cloud.SubCollectorResult,
) error {
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		call := c.service.Tables.List(c.projectID, datasetID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("bigquery: list tables in %s: %w", datasetID, err)
		}

		for _, t := range resp.Tables {
			if t.TableReference == nil || t.TableReference.TableId == "" {
				continue
			}

			tableID := fmt.Sprintf("%s/tables/%s", dsResourceID, t.TableReference.TableId)

			content, _ := json.Marshal(t) //nolint:errchkjson // best-effort content envelope
			spec := cloud.ResourceSpec{
				ID:           tableID,
				Name:         t.TableReference.TableId,
				ResourceType: "gcp:bigquery:table",
				Content:      content,
				Metadata: map[string]string{
					"type": t.Type,
				},
			}
			result.Resources = append(result.Resources, spec)

			// Dataset contains table.
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     dsResourceID,
				TargetID:     tableID,
				Relationship: kgtypes.EdgeContains,
			})
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return nil
}

// bigqueryDatasetEdges extracts ENCRYPTS_WITH edges for customer-managed
// encryption keys on a BigQuery dataset.
func bigqueryDatasetEdges(dsResourceID string, ds *bq.Dataset) []cloud.EdgeSpec {
	if ds.DefaultEncryptionConfiguration == nil {
		return nil
	}
	kmsKey := ds.DefaultEncryptionConfiguration.KmsKeyName
	if kmsKey == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     dsResourceID,
		TargetID:     kmsKey,
		Relationship: kgtypes.EdgeEncryptsWith,
		Metadata:     map[string]string{"encryption_scope": "dataset"},
	}}
}
