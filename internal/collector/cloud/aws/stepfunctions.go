// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type stepFunctionsCollector struct {
	client *sfn.Client
	region string
}

func newStepFunctionsCollector(cfg awssdk.Config, region string) cloud.SubCollector {
	return &stepFunctionsCollector{
		client: sfn.NewFromConfig(cfg),
		region: region,
	}
}

func (c *stepFunctionsCollector) Name() string { return "stepfunctions" }

func (c *stepFunctionsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	machines, err := c.listStateMachines(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}

	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	for _, sm := range machines {
		res, smEdges, err := c.describeStateMachine(ctx, awssdk.ToString(sm.StateMachineArn))
		if err != nil {
			return cloud.SubCollectorResult{}, err
		}
		resources = append(resources, res)
		edges = append(edges, smEdges...)
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// listStateMachines paginates through all state machines.
func (c *stepFunctionsCollector) listStateMachines(ctx context.Context) ([]sfnStateMachineEntry, error) {
	var machines []sfnStateMachineEntry

	paginator := sfn.NewListStateMachinesPaginator(c.client, &sfn.ListStateMachinesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("stepfunctions: list state machines: %w", err)
		}
		for _, sm := range page.StateMachines {
			machines = append(machines, sfnStateMachineEntry{
				StateMachineArn: sm.StateMachineArn,
				Name:            sm.Name,
			})
		}
	}

	return machines, nil
}

// sfnStateMachineEntry is a minimal struct to avoid importing sfn types globally.
type sfnStateMachineEntry struct {
	StateMachineArn *string
	Name            *string
}

// describeStateMachine fetches state machine details and extracts edges.
func (c *stepFunctionsCollector) describeStateMachine(ctx context.Context, smARN string) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	desc, err := c.client.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
		StateMachineArn: awssdk.String(smARN),
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("stepfunctions: describe %s: %w", smARN, err)
	}

	content, err := json.Marshal(desc)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("stepfunctions: marshal: %w", err)
	}

	name := awssdk.ToString(desc.Name)

	res := cloud.ResourceSpec{
		ID:           smARN,
		Name:         name,
		ResourceType: "stepfunctions-statemachine",
		Region:       c.region,
		Content:      content,
		Metadata:     stepFunctionsStateMachineMetadata(desc),
	}

	var edges []cloud.EdgeSpec

	// State machine → IAM role
	if desc.RoleArn != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     smARN,
			TargetID:     awssdk.ToString(desc.RoleArn),
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "execution_role"},
		})
	}

	// Best-effort ASL parsing: extract target ARNs from the definition JSON.
	if desc.Definition != nil {
		targets := extractASLTargets(awssdk.ToString(desc.Definition))
		for _, targetARN := range targets {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     smARN,
				TargetID:     targetARN,
				Relationship: kgtypes.EdgeTargets,
			})
		}
	}

	return res, edges, nil
}

// aslARNPattern matches ARN-like strings in ASL definition JSON. It captures
// Lambda, ECS, SQS, SNS, Step Functions, DynamoDB, and S3 access point ARNs.
// This is best-effort — not a full ASL parser.
var aslARNPattern = regexp.MustCompile(
	`arn:aws:(?:lambda|ecs|sqs|sns|states|dynamodb|s3):[a-z0-9-]+:\d{12}:[^\s"\\]+`,
)

// aslS3BucketPattern matches S3 bucket ARNs which lack the region:account
// segment (arn:aws:s3:::bucket-name).
var aslS3BucketPattern = regexp.MustCompile(
	`arn:aws:s3:::[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]`,
)

// extractASLTargets extracts resource ARNs from an ASL definition JSON string.
// It uses regex to find ARN patterns for Lambda, ECS, SQS, SNS, Step Functions,
// DynamoDB, and S3. Duplicate ARNs are deduplicated.
func extractASLTargets(definition string) []string {
	matches := aslARNPattern.FindAllString(definition, -1)
	matches = append(matches, aslS3BucketPattern.FindAllString(definition, -1)...)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	var unique []string
	for _, m := range matches {
		// Trim trailing punctuation that regex might capture from JSON.
		m = strings.TrimRight(m, `",}]`)
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		unique = append(unique, m)
	}

	return unique
}

// stepFunctionsStateMachineMetadata extracts discriminating fields from a state machine.
func stepFunctionsStateMachineMetadata(desc *sfn.DescribeStateMachineOutput) map[string]string {
	if desc == nil {
		return nil
	}
	m := make(map[string]string, 3)
	if t := string(desc.Type); t != "" {
		m["type"] = t
	}
	if s := string(desc.Status); s != "" {
		m["status"] = s
	}
	if l := desc.LoggingConfiguration; l != nil {
		if lvl := string(l.Level); lvl != "" {
			m["logging_level"] = lvl
		}
	}
	return m
}
