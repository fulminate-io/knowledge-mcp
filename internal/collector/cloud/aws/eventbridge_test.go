// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestEventBridgeCollector_Name(t *testing.T) {
	c := &eventBridgeCollector{}
	assert.Equal(t, "eventbridge", c.Name())
}

// fakeEventBridgeAPI is a minimal in-memory EventBridge client for unit tests.
type fakeEventBridgeAPI struct {
	rules   []ebtypes.Rule
	targets map[string][]ebtypes.Target // keyed by rule name
}

func (f *fakeEventBridgeAPI) ListRules(_ context.Context, _ *eventbridge.ListRulesInput, _ ...func(*eventbridge.Options)) (*eventbridge.ListRulesOutput, error) {
	return &eventbridge.ListRulesOutput{Rules: f.rules}, nil
}

func (f *fakeEventBridgeAPI) ListTargetsByRule(_ context.Context, in *eventbridge.ListTargetsByRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.ListTargetsByRuleOutput, error) {
	ruleName := awssdk.ToString(in.Rule)
	return &eventbridge.ListTargetsByRuleOutput{
		Targets: f.targets[ruleName],
	}, nil
}

func TestEventBridgeCollector_TargetEdges(t *testing.T) {
	ruleARN := "arn:aws:events:us-east-1:111111111111:rule/my-rule"
	lambdaARN := "arn:aws:lambda:us-east-1:111111111111:function:handler"
	sqsARN := "arn:aws:sqs:us-east-1:111111111111:my-queue"

	fake := &fakeEventBridgeAPI{
		rules: []ebtypes.Rule{{
			Name: awssdk.String("my-rule"),
			Arn:  awssdk.String(ruleARN),
		}},
		targets: map[string][]ebtypes.Target{
			"my-rule": {
				{Arn: awssdk.String(lambdaARN)},
				{Arn: awssdk.String(sqsARN)},
			},
		},
	}

	c := &eventBridgeCollector{
		client:    fake,
		region:    "us-east-1",
		accountID: "111111111111",
	}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Equal(t, ruleARN, result.Resources[0].ID)
	assert.Equal(t, "my-rule", result.Resources[0].Name)
	assert.Equal(t, "eventbridge-rule", result.Resources[0].ResourceType)

	require.Len(t, result.Edges, 2)
	assert.Equal(t, ruleARN, result.Edges[0].SourceID)
	assert.Equal(t, lambdaARN, result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, result.Edges[0].Relationship)

	assert.Equal(t, ruleARN, result.Edges[1].SourceID)
	assert.Equal(t, sqsARN, result.Edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, result.Edges[1].Relationship)
}

func TestEventBridgeCollector_EmptyTargetARNSkipped(t *testing.T) {
	ruleARN := "arn:aws:events:us-east-1:111111111111:rule/skip-rule"

	fake := &fakeEventBridgeAPI{
		rules: []ebtypes.Rule{{
			Name: awssdk.String("skip-rule"),
			Arn:  awssdk.String(ruleARN),
		}},
		targets: map[string][]ebtypes.Target{
			"skip-rule": {
				{Arn: awssdk.String("")}, // empty ARN should be skipped
				{Arn: nil},               // nil ARN should be skipped
			},
		},
	}

	c := &eventBridgeCollector{
		client:    fake,
		region:    "us-east-1",
		accountID: "111111111111",
	}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges, "empty/nil target ARNs should produce no edges")
}

func TestEventBridgeCollector_NoRules(t *testing.T) {
	fake := &fakeEventBridgeAPI{
		rules:   nil,
		targets: map[string][]ebtypes.Target{},
	}

	c := &eventBridgeCollector{
		client:    fake,
		region:    "us-east-1",
		accountID: "111111111111",
	}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Resources)
	assert.Empty(t, result.Edges)
}
