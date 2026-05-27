// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectAlarms paginates through all CloudWatch metric alarms and builds
// resources + MONITORS edges from alarm dimensions.
func (c *cloudwatchCollector) collectAlarms(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := cloudwatch.NewDescribeAlarmsPaginator(c.cwClient, &cloudwatch.DescribeAlarmsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("cloudwatch: describe alarms: %w", err)
		}
		for _, alarm := range page.MetricAlarms {
			res, alarmEdges, err := c.buildAlarmResource(alarm)
			if err != nil {
				return nil, nil, err
			}
			resources = append(resources, res)
			edges = append(edges, alarmEdges...)
		}
	}

	return resources, edges, nil
}

// buildAlarmResource creates a ResourceSpec, MONITORS edges from
// dimensions, and NotifiesVia edges from alarm actions for a single alarm.
func (c *cloudwatchCollector) buildAlarmResource(alarm cwtypes.MetricAlarm) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	content, err := json.Marshal(alarm)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("cloudwatch: marshal alarm: %w", err)
	}

	alarmARN := awssdk.ToString(alarm.AlarmArn)
	name := awssdk.ToString(alarm.AlarmName)

	res := cloud.ResourceSpec{
		ID:           alarmARN,
		Name:         name,
		ResourceType: "cloudwatch-alarm",
		Region:       c.region,
		Content:      content,
		Metadata:     cloudwatchAlarmMetadata(alarm),
	}

	edges := c.alarmDimensionEdges(alarmARN, awssdk.ToString(alarm.Namespace), alarm.Dimensions)
	edges = append(edges, alarmActionEdges(alarmARN, alarm)...)

	return res, edges, nil
}

// alarmActionEdges emits EdgeNotifiesVia for each unique action target
// across AlarmActions, OKActions, and InsufficientDataActions. Each edge
// carries Metadata["state"] to indicate which alarm state triggers the
// notification.
func alarmActionEdges(alarmARN string, alarm cwtypes.MetricAlarm) []cloud.EdgeSpec {
	type actionEntry struct {
		arn   string
		state string
	}
	var entries []actionEntry
	for _, a := range alarm.AlarmActions {
		entries = append(entries, actionEntry{arn: a, state: "alarm"})
	}
	for _, a := range alarm.OKActions {
		entries = append(entries, actionEntry{arn: a, state: "ok"})
	}
	for _, a := range alarm.InsufficientDataActions {
		entries = append(entries, actionEntry{arn: a, state: "insufficient_data"})
	}

	seen := make(map[string]struct{})
	var edges []cloud.EdgeSpec
	for _, e := range entries {
		if e.arn == "" {
			continue
		}
		key := e.arn + "|" + e.state
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     alarmARN,
			TargetID:     e.arn,
			Relationship: kgtypes.EdgeNotifiesVia,
			Metadata:     map[string]string{"state": e.state},
		})
	}
	return edges
}

// alarmDimensionEdges extracts MONITORS edges from CloudWatch alarm dimensions.
// It reconstructs ARNs from the namespace + dimension values. This covers the
// broad set of AWS namespaces (not just top 5) by mapping dimension names to
// ARN patterns.
func (c *cloudwatchCollector) alarmDimensionEdges(alarmARN, namespace string, dims []cwtypes.Dimension) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	for _, dim := range dims {
		dimName := awssdk.ToString(dim.Name)
		dimValue := awssdk.ToString(dim.Value)
		if dimName == "" || dimValue == "" {
			continue
		}

		targetARN := c.resolveAlarmDimensionARN(namespace, dimName, dimValue)
		if targetARN == "" {
			continue
		}

		edges = append(edges, cloud.EdgeSpec{
			SourceID:     alarmARN,
			TargetID:     targetARN,
			Relationship: kgtypes.EdgeMonitors,
		})
	}

	return edges
}

// dimensionARNPatterns maps (namespace, dimension name) pairs to ARN format strings.
// The format string uses %s placeholders for region, account ID, and dimension value.
var dimensionARNPatterns = map[string]map[string]string{
	"AWS/EC2": {
		"InstanceId": "arn:aws:ec2:%s:%s:instance/%s",
	},
	"AWS/RDS": {
		"DBInstanceIdentifier": "arn:aws:rds:%s:%s:db:%s",
		"DBClusterIdentifier":  "arn:aws:rds:%s:%s:cluster:%s",
	},
	"AWS/Lambda": {
		"FunctionName": "arn:aws:lambda:%s:%s:function:%s",
	},
	"AWS/ECS": {
		"ServiceName": "arn:aws:ecs:%s:%s:service/%s",
		"ClusterName": "arn:aws:ecs:%s:%s:cluster/%s",
	},
	"AWS/SQS": {
		"QueueName": "arn:aws:sqs:%s:%s:%s",
	},
	"AWS/SNS": {
		"TopicName": "arn:aws:sns:%s:%s:%s",
	},
	"AWS/DynamoDB": {
		"TableName": "arn:aws:dynamodb:%s:%s:table/%s",
	},
	"AWS/S3": {
		"BucketName": "arn:aws:s3:::%s", // S3 ARNs have no region or account
	},
	"AWS/ELB": {
		"LoadBalancerName": "arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s",
	},
	"AWS/ApplicationELB": {
		"LoadBalancer": "arn:aws:elasticloadbalancing:%s:%s:loadbalancer/app/%s",
		"TargetGroup":  "arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s",
	},
	"AWS/NetworkELB": {
		"LoadBalancer": "arn:aws:elasticloadbalancing:%s:%s:loadbalancer/net/%s",
	},
	"AWS/Kinesis": {
		"StreamName": "arn:aws:kinesis:%s:%s:stream/%s",
	},
	"AWS/ElastiCache": {
		"CacheClusterId": "arn:aws:elasticache:%s:%s:cluster:%s",
	},
	"AWS/ES": {
		"DomainName": "arn:aws:es:%s:%s:domain/%s",
	},
	"AWS/ApiGateway": {
		"ApiName": "arn:aws:apigateway:%s::/restapis/%s",
	},
	"AWS/States": {
		"StateMachineArn": "", // already an ARN
	},
}

// resolveAlarmDimensionARN converts a CloudWatch dimension to a target resource ARN.
func (c *cloudwatchCollector) resolveAlarmDimensionARN(namespace, dimName, dimValue string) string {
	nsPatterns, ok := dimensionARNPatterns[namespace]
	if !ok {
		return ""
	}
	pattern, ok := nsPatterns[dimName]
	if !ok {
		return ""
	}

	// Some dimensions are already full ARNs.
	if pattern == "" {
		if strings.HasPrefix(dimValue, "arn:") {
			return dimValue
		}
		return ""
	}

	// S3 has a special ARN format without region/account.
	if namespace == "AWS/S3" {
		return fmt.Sprintf(pattern, dimValue)
	}

	return fmt.Sprintf(pattern, c.region, c.accountID, dimValue)
}

// cloudwatchAlarmMetadata extracts discriminating fields from a metric alarm.
func cloudwatchAlarmMetadata(a cwtypes.MetricAlarm) map[string]string {
	m := make(map[string]string, 4)
	if v := awssdk.ToString(a.Namespace); v != "" {
		m["namespace"] = v
	}
	if v := awssdk.ToString(a.MetricName); v != "" {
		m["metric_name"] = v
	}
	if s := string(a.StateValue); s != "" {
		m["state"] = s
	}
	if c := string(a.ComparisonOperator); c != "" {
		m["comparison_operator"] = c
	}
	return m
}
