// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCloudwatchCollector_Name(t *testing.T) {
	c := &cloudwatchCollector{}
	assert.Equal(t, "cloudwatch", c.Name())
}

func TestResolveAlarmDimensionARN(t *testing.T) {
	c := &cloudwatchCollector{
		region:    "us-east-1",
		accountID: "123456789012",
	}

	tests := []struct {
		name      string
		namespace string
		dimName   string
		dimValue  string
		want      string
	}{
		{
			name:      "EC2 instance",
			namespace: "AWS/EC2",
			dimName:   "InstanceId",
			dimValue:  "i-0123456789abcdef0",
			want:      "arn:aws:ec2:us-east-1:123456789012:instance/i-0123456789abcdef0",
		},
		{
			name:      "RDS instance",
			namespace: "AWS/RDS",
			dimName:   "DBInstanceIdentifier",
			dimValue:  "my-database",
			want:      "arn:aws:rds:us-east-1:123456789012:db:my-database",
		},
		{
			name:      "RDS cluster",
			namespace: "AWS/RDS",
			dimName:   "DBClusterIdentifier",
			dimValue:  "my-cluster",
			want:      "arn:aws:rds:us-east-1:123456789012:cluster:my-cluster",
		},
		{
			name:      "Lambda function",
			namespace: "AWS/Lambda",
			dimName:   "FunctionName",
			dimValue:  "my-handler",
			want:      "arn:aws:lambda:us-east-1:123456789012:function:my-handler",
		},
		{
			name:      "SQS queue",
			namespace: "AWS/SQS",
			dimName:   "QueueName",
			dimValue:  "my-queue",
			want:      "arn:aws:sqs:us-east-1:123456789012:my-queue",
		},
		{
			name:      "SNS topic",
			namespace: "AWS/SNS",
			dimName:   "TopicName",
			dimValue:  "my-topic",
			want:      "arn:aws:sns:us-east-1:123456789012:my-topic",
		},
		{
			name:      "S3 bucket no region no account",
			namespace: "AWS/S3",
			dimName:   "BucketName",
			dimValue:  "my-bucket",
			want:      "arn:aws:s3:::my-bucket",
		},
		{
			name:      "DynamoDB table",
			namespace: "AWS/DynamoDB",
			dimName:   "TableName",
			dimValue:  "users-table",
			want:      "arn:aws:dynamodb:us-east-1:123456789012:table/users-table",
		},
		{
			name:      "Kinesis stream",
			namespace: "AWS/Kinesis",
			dimName:   "StreamName",
			dimValue:  "data-stream",
			want:      "arn:aws:kinesis:us-east-1:123456789012:stream/data-stream",
		},
		{
			name:      "States dimension already an ARN",
			namespace: "AWS/States",
			dimName:   "StateMachineArn",
			dimValue:  "arn:aws:states:us-east-1:123456789012:stateMachine:my-sm",
			want:      "arn:aws:states:us-east-1:123456789012:stateMachine:my-sm",
		},
		{
			name:      "States dimension not an ARN returns empty",
			namespace: "AWS/States",
			dimName:   "StateMachineArn",
			dimValue:  "not-an-arn",
			want:      "",
		},
		{
			name:      "unknown namespace returns empty",
			namespace: "AWS/Unknown",
			dimName:   "SomeId",
			dimValue:  "some-value",
			want:      "",
		},
		{
			name:      "known namespace unknown dimension returns empty",
			namespace: "AWS/EC2",
			dimName:   "UnknownDim",
			dimValue:  "some-value",
			want:      "",
		},
		{
			name:      "ECS service",
			namespace: "AWS/ECS",
			dimName:   "ServiceName",
			dimValue:  "my-service",
			want:      "arn:aws:ecs:us-east-1:123456789012:service/my-service",
		},
		{
			name:      "ElastiCache cluster",
			namespace: "AWS/ElastiCache",
			dimName:   "CacheClusterId",
			dimValue:  "my-cache",
			want:      "arn:aws:elasticache:us-east-1:123456789012:cluster:my-cache",
		},
		{
			name:      "Elasticsearch domain",
			namespace: "AWS/ES",
			dimName:   "DomainName",
			dimValue:  "my-domain",
			want:      "arn:aws:es:us-east-1:123456789012:domain/my-domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.resolveAlarmDimensionARN(tt.namespace, tt.dimName, tt.dimValue)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestAlarmActionEdges_SNS verifies that AlarmActions targeting an SNS
// topic emit EdgeNotifiesVia with state=alarm metadata.
func TestAlarmActionEdges_SNS(t *testing.T) {
	alarmARN := "arn:aws:cloudwatch:us-east-1:111111111111:alarm:cpu-high"
	snsARN := "arn:aws:sns:us-east-1:111111111111:alerts"

	alarm := cwtypes.MetricAlarm{
		AlarmArn:     awssdk.String(alarmARN),
		AlarmName:    awssdk.String("cpu-high"),
		AlarmActions: []string{snsARN},
	}

	edges := alarmActionEdges(alarmARN, alarm)
	require.Len(t, edges, 1)
	assert.Equal(t, alarmARN, edges[0].SourceID)
	assert.Equal(t, snsARN, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeNotifiesVia, edges[0].Relationship)
	assert.Equal(t, "alarm", edges[0].Metadata["state"])
}

// TestAlarmActionEdges_AllStates verifies all three action states emit
// distinct NotifiesVia edges.
func TestAlarmActionEdges_AllStates(t *testing.T) {
	alarmARN := "arn:aws:cloudwatch:us-east-1:111:alarm:test"
	topic1 := "arn:aws:sns:us-east-1:111:alarm-topic"
	topic2 := "arn:aws:sns:us-east-1:111:ok-topic"
	topic3 := "arn:aws:sns:us-east-1:111:insufficient-topic"

	alarm := cwtypes.MetricAlarm{
		AlarmArn:                awssdk.String(alarmARN),
		AlarmName:               awssdk.String("test"),
		AlarmActions:            []string{topic1},
		OKActions:               []string{topic2},
		InsufficientDataActions: []string{topic3},
	}

	edges := alarmActionEdges(alarmARN, alarm)
	require.Len(t, edges, 3)

	states := make(map[string]string)
	for _, e := range edges {
		assert.Equal(t, kgtypes.EdgeNotifiesVia, e.Relationship)
		states[e.Metadata["state"]] = e.TargetID
	}
	assert.Equal(t, topic1, states["alarm"])
	assert.Equal(t, topic2, states["ok"])
	assert.Equal(t, topic3, states["insufficient_data"])
}

// TestAlarmActionEdges_Dedup verifies same target in multiple action
// lists with different states produces separate edges, but exact
// duplicates are deduped.
func TestAlarmActionEdges_Dedup(t *testing.T) {
	alarmARN := "arn:aws:cloudwatch:us-east-1:111:alarm:dup"
	topic := "arn:aws:sns:us-east-1:111:same-topic"

	alarm := cwtypes.MetricAlarm{
		AlarmArn:     awssdk.String(alarmARN),
		AlarmName:    awssdk.String("dup"),
		AlarmActions: []string{topic, topic}, // duplicate
		OKActions:    []string{topic},        // different state
	}

	edges := alarmActionEdges(alarmARN, alarm)
	// topic|alarm once, topic|ok once = 2
	assert.Len(t, edges, 2)
}
