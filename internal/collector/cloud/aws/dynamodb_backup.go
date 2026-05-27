// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectPITR calls DescribeContinuousBackups and, when PITR is enabled,
// emits a synthetic PITR proxy resource + EdgeBackedUpBy from the table.
func (c *dynamodbCollector) collectPITR(
	ctx context.Context, tableARN, tableName string,
) (*cloud.ResourceSpec, cloud.EdgeSpec) {
	out, err := c.client.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{
		TableName: awssdk.String(tableName),
	})
	if err != nil {
		slog.Debug("dynamodb: describe continuous backups", "table", tableName, "error", err)
		return nil, cloud.EdgeSpec{}
	}
	if out == nil || out.ContinuousBackupsDescription == nil {
		return nil, cloud.EdgeSpec{}
	}
	pitr := out.ContinuousBackupsDescription.PointInTimeRecoveryDescription
	if pitr == nil || pitr.PointInTimeRecoveryStatus != ddbtypes.PointInTimeRecoveryStatusEnabled {
		return nil, cloud.EdgeSpec{}
	}

	proxyID := fmt.Sprintf("aws:dynamodb:pitr/%s", tableName)
	meta := map[string]string{"enabled": "true"}
	if pitr.EarliestRestorableDateTime != nil {
		meta["earliest_restorable"] = pitr.EarliestRestorableDateTime.String()
	}
	if pitr.LatestRestorableDateTime != nil {
		meta["latest_restorable"] = pitr.LatestRestorableDateTime.String()
	}

	res := cloud.ResourceSpec{
		ID:           proxyID,
		Name:         fmt.Sprintf("PITR for %s", tableName),
		ResourceType: "dynamodb-pitr",
		Region:       c.region,
		Metadata:     meta,
	}

	edge := cloud.EdgeSpec{
		SourceID:     tableARN,
		TargetID:     proxyID,
		Relationship: kgtypes.EdgeBackedUpBy,
	}

	return &res, edge
}

// collectBackups calls ListBackups for a specific table and emits a
// resource + EdgeBackedUpBy for each on-demand backup.
func (c *dynamodbCollector) collectBackups(
	ctx context.Context, tableARN, tableName string,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	out, err := c.client.ListBackups(ctx, &dynamodb.ListBackupsInput{
		TableName: awssdk.String(tableName),
	})
	if err != nil {
		slog.Debug("dynamodb: list backups", "table", tableName, "error", err)
		return nil, nil
	}
	if out == nil {
		return nil, nil
	}

	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)
	for _, bkp := range out.BackupSummaries {
		bkpARN := awssdk.ToString(bkp.BackupArn)
		if bkpARN == "" {
			continue
		}
		bkpName := awssdk.ToString(bkp.BackupName)
		if bkpName == "" {
			bkpName = bkpARN
		}

		resources = append(resources, cloud.ResourceSpec{
			ID:           bkpARN,
			Name:         bkpName,
			ResourceType: "dynamodb-backup",
			Region:       c.region,
			Metadata: map[string]string{
				"backup_status": string(bkp.BackupStatus),
				"backup_type":   string(bkp.BackupType),
			},
		})
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     tableARN,
			TargetID:     bkpARN,
			Relationship: kgtypes.EdgeBackedUpBy,
		})
	}
	return resources, edges
}
