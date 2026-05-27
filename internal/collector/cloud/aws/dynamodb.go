// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// dynamodbAPI is the subset of the DynamoDB client surface used by the
// collector. Defining it as an interface lets tests mock DynamoDB.
type dynamodbAPI interface {
	ListTables(ctx context.Context, params *dynamodb.ListTablesInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
	DescribeTable(ctx context.Context, params *dynamodb.DescribeTableInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	DescribeContinuousBackups(ctx context.Context, params *dynamodb.DescribeContinuousBackupsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error)
	ListBackups(ctx context.Context, params *dynamodb.ListBackupsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ListBackupsOutput, error)
}

type dynamodbCollector struct {
	client    dynamodbAPI
	region    string
	accountID string
}

func newDynamoDBCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &dynamodbCollector{
		client:    dynamodb.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *dynamodbCollector) Name() string { return "dynamodb" }

func (c *dynamodbCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	var lastEval *string
	for {
		page, err := c.client.ListTables(ctx, &dynamodb.ListTablesInput{
			ExclusiveStartTableName: lastEval,
		})
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("dynamodb: list tables: %w", err)
		}
		for _, tableName := range page.TableNames {
			res, tEdges, tResources, err := c.collectTable(ctx, tableName)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, res)
			resources = append(resources, tResources...)
			edges = append(edges, tEdges...)
		}
		lastEval = page.LastEvaluatedTableName
		if lastEval == nil {
			break
		}
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// collectTable fetches a DynamoDB table description and builds its
// resource + edges (encryption, streams, PITR, backups).
func (c *dynamodbCollector) collectTable(
	ctx context.Context, tableName string,
) (cloud.ResourceSpec, []cloud.EdgeSpec, []cloud.ResourceSpec, error) {
	desc, err := c.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: awssdk.String(tableName),
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, nil, fmt.Errorf("dynamodb: describe table %s: %w", tableName, err)
	}

	table := desc.Table
	content, err := json.Marshal(table)
	if err != nil {
		return cloud.ResourceSpec{}, nil, nil, fmt.Errorf("dynamodb: marshal: %w", err)
	}

	tableARN := awssdk.ToString(table.TableArn)
	res := cloud.ResourceSpec{
		ID:           tableARN,
		Name:         tableName,
		ResourceType: "dynamodb-table",
		Region:       c.region,
		Content:      content,
		Metadata:     dynamodbTableMetadata(table),
	}

	var (
		edges          []cloud.EdgeSpec
		extraResources []cloud.ResourceSpec
	)

	edges = append(edges, tableEncryptionEdge(tableARN, table)...)
	edges = append(edges, tableStreamEdge(tableARN, table)...)

	pitrRes, pitrEdge := c.collectPITR(ctx, tableARN, tableName)
	if pitrRes != nil {
		extraResources = append(extraResources, *pitrRes)
		edges = append(edges, pitrEdge)
	}

	bkpRes, bkpEdges := c.collectBackups(ctx, tableARN, tableName)
	extraResources = append(extraResources, bkpRes...)
	edges = append(edges, bkpEdges...)

	return res, edges, extraResources, nil
}

// tableEncryptionEdge emits EdgeEncryptsWith when the table has KMS SSE.
func tableEncryptionEdge(tableARN string, table *ddbtypes.TableDescription) []cloud.EdgeSpec {
	if table.SSEDescription == nil || table.SSEDescription.KMSMasterKeyArn == nil {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     tableARN,
		TargetID:     awssdk.ToString(table.SSEDescription.KMSMasterKeyArn),
		Relationship: kgtypes.EdgeEncryptsWith,
		Metadata:     map[string]string{"encryption_scope": "table"},
	}}
}

// tableStreamEdge emits EdgeTriggers from table to its latest stream ARN
// when DynamoDB Streams is enabled. The actual Lambda→Stream wiring is
// handled by the Lambda subcollector via event source mappings.
func tableStreamEdge(tableARN string, table *ddbtypes.TableDescription) []cloud.EdgeSpec {
	if table.LatestStreamArn == nil {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     tableARN,
		TargetID:     awssdk.ToString(table.LatestStreamArn),
		Relationship: kgtypes.EdgeTriggers,
		Metadata:     map[string]string{"stream_view_type": streamViewType(table)},
	}}
}

func streamViewType(table *ddbtypes.TableDescription) string {
	if table.StreamSpecification != nil {
		return string(table.StreamSpecification.StreamViewType)
	}
	return ""
}

// dynamodbTableMetadata extracts discriminating fields from a DynamoDB table.
func dynamodbTableMetadata(t *ddbtypes.TableDescription) map[string]string {
	if t == nil {
		return nil
	}
	m := make(map[string]string, 3)
	if s := string(t.TableStatus); s != "" {
		m["status"] = s
	}
	if t.BillingModeSummary != nil {
		if bm := string(t.BillingModeSummary.BillingMode); bm != "" {
			m["billing_mode"] = bm
		}
	}
	if t.ItemCount != nil {
		m["item_count"] = fmt.Sprintf("%d", awssdk.ToInt64(t.ItemCount))
	}
	return m
}
