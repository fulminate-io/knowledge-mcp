// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type fakeDynamodbAPI struct {
	tables  map[string]*ddbtypes.TableDescription
	pitr    map[string]*ddbtypes.ContinuousBackupsDescription
	backups map[string][]ddbtypes.BackupSummary
}

func (f *fakeDynamodbAPI) ListTables(_ context.Context, _ *dynamodb.ListTablesInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
	var names []string
	for n := range f.tables {
		names = append(names, n)
	}
	return &dynamodb.ListTablesOutput{TableNames: names}, nil
}

func (f *fakeDynamodbAPI) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	name := awssdk.ToString(in.TableName)
	if td, ok := f.tables[name]; ok {
		return &dynamodb.DescribeTableOutput{Table: td}, nil
	}
	return &dynamodb.DescribeTableOutput{Table: &ddbtypes.TableDescription{
		TableName: in.TableName,
		TableArn:  awssdk.String("arn:aws:dynamodb:us-east-1:111:table/" + name),
	}}, nil
}

func (f *fakeDynamodbAPI) DescribeContinuousBackups(_ context.Context, in *dynamodb.DescribeContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error) {
	name := awssdk.ToString(in.TableName)
	if desc, ok := f.pitr[name]; ok {
		return &dynamodb.DescribeContinuousBackupsOutput{
			ContinuousBackupsDescription: desc,
		}, nil
	}
	return &dynamodb.DescribeContinuousBackupsOutput{}, nil
}

func (f *fakeDynamodbAPI) ListBackups(_ context.Context, in *dynamodb.ListBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListBackupsOutput, error) {
	name := awssdk.ToString(in.TableName)
	return &dynamodb.ListBackupsOutput{BackupSummaries: f.backups[name]}, nil
}

const testTableName = "my-table"

func testTableARN() string {
	return "arn:aws:dynamodb:us-east-1:111111111111:table/" + testTableName
}

func baseTable() *ddbtypes.TableDescription {
	return &ddbtypes.TableDescription{
		TableName: awssdk.String(testTableName),
		TableArn:  awssdk.String(testTableARN()),
	}
}

// TestDynamoDB_StreamEdge verifies that a table with DynamoDB Streams
// emits EdgeTriggers to the stream ARN.
func TestDynamoDB_StreamEdge(t *testing.T) {
	streamARN := testTableARN() + "/stream/2024-01-01"
	td := baseTable()
	td.LatestStreamArn = awssdk.String(streamARN)
	td.StreamSpecification = &ddbtypes.StreamSpecification{
		StreamEnabled:  awssdk.Bool(true),
		StreamViewType: ddbtypes.StreamViewTypeNewAndOldImages,
	}

	fake := &fakeDynamodbAPI{
		tables: map[string]*ddbtypes.TableDescription{testTableName: td},
		pitr:   map[string]*ddbtypes.ContinuousBackupsDescription{},
	}
	c := &dynamodbCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeTriggers && e.TargetID == streamARN {
			assert.Equal(t, "NEW_AND_OLD_IMAGES", e.Metadata["stream_view_type"])
			found = true
		}
	}
	assert.True(t, found, "expected EdgeTriggers to stream ARN")
}

// TestDynamoDB_PITREnabled verifies PITR creates a proxy resource and
// EdgeBackedUpBy edge.
func TestDynamoDB_PITREnabled(t *testing.T) {
	now := time.Now()
	td := baseTable()
	fake := &fakeDynamodbAPI{
		tables: map[string]*ddbtypes.TableDescription{testTableName: td},
		pitr: map[string]*ddbtypes.ContinuousBackupsDescription{
			testTableName: {
				ContinuousBackupsStatus: ddbtypes.ContinuousBackupsStatusEnabled,
				PointInTimeRecoveryDescription: &ddbtypes.PointInTimeRecoveryDescription{
					PointInTimeRecoveryStatus:  ddbtypes.PointInTimeRecoveryStatusEnabled,
					EarliestRestorableDateTime: &now,
					LatestRestorableDateTime:   &now,
				},
			},
		},
	}
	c := &dynamodbCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// Should have table + PITR proxy resources.
	require.GreaterOrEqual(t, len(result.Resources), 2)
	var hasPITR bool
	for _, r := range result.Resources {
		if r.ResourceType == "dynamodb-pitr" {
			hasPITR = true
			assert.Equal(t, "true", r.Metadata["enabled"])
		}
	}
	assert.True(t, hasPITR, "expected PITR proxy resource")

	var hasBackedUpBy bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeBackedUpBy && e.SourceID == testTableARN() {
			hasBackedUpBy = true
		}
	}
	assert.True(t, hasBackedUpBy, "expected EdgeBackedUpBy edge")
}

// TestDynamoDB_Backups verifies on-demand backups emit resources and
// EdgeBackedUpBy edges.
func TestDynamoDB_Backups(t *testing.T) {
	bkpARN := "arn:aws:dynamodb:us-east-1:111:table/my-table/backup/01"
	td := baseTable()
	fake := &fakeDynamodbAPI{
		tables: map[string]*ddbtypes.TableDescription{testTableName: td},
		pitr:   map[string]*ddbtypes.ContinuousBackupsDescription{},
		backups: map[string][]ddbtypes.BackupSummary{
			testTableName: {{
				BackupArn:    awssdk.String(bkpARN),
				BackupName:   awssdk.String("daily-backup"),
				BackupStatus: ddbtypes.BackupStatusAvailable,
				BackupType:   ddbtypes.BackupTypeUser,
			}},
		},
	}
	c := &dynamodbCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var hasBkpResource bool
	for _, r := range result.Resources {
		if r.ResourceType == "dynamodb-backup" {
			hasBkpResource = true
			assert.Equal(t, "AVAILABLE", r.Metadata["backup_status"])
			assert.Equal(t, "USER", r.Metadata["backup_type"])
		}
	}
	assert.True(t, hasBkpResource, "expected backup resource")

	var hasEdge bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeBackedUpBy && e.TargetID == bkpARN {
			hasEdge = true
		}
	}
	assert.True(t, hasEdge, "expected EdgeBackedUpBy to backup ARN")
}

// TestDynamoDB_NoPITRNoBackups verifies clean path with no PITR or backups.
func TestDynamoDB_NoPITRNoBackups(t *testing.T) {
	td := baseTable()
	fake := &fakeDynamodbAPI{
		tables: map[string]*ddbtypes.TableDescription{testTableName: td},
		pitr:   map[string]*ddbtypes.ContinuousBackupsDescription{},
	}
	c := &dynamodbCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1) // just the table
	assert.Empty(t, result.Edges)
}
