// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// eventBridgeAPI is the subset of the EventBridge client surface used by
// eventBridgeCollector. Defining it as an interface lets tests inject a fake
// without AWS credentials. The concrete *eventbridge.Client satisfies this.
type eventBridgeAPI interface {
	ListRules(ctx context.Context, params *eventbridge.ListRulesInput, optFns ...func(*eventbridge.Options)) (*eventbridge.ListRulesOutput, error)
	ListTargetsByRule(ctx context.Context, params *eventbridge.ListTargetsByRuleInput, optFns ...func(*eventbridge.Options)) (*eventbridge.ListTargetsByRuleOutput, error)
}

type eventBridgeCollector struct {
	client    eventBridgeAPI
	region    string
	accountID string
}

func newEventBridgeCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &eventBridgeCollector{
		client:    eventbridge.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *eventBridgeCollector) Name() string { return "eventbridge" }

func (c *eventBridgeCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	rules, err := c.listAllRules(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}

	for _, rule := range rules {
		res, ruleEdges, err := c.collectRule(ctx, rule)
		if err != nil {
			return cloud.SubCollectorResult{}, err
		}
		resources = append(resources, res)
		edges = append(edges, ruleEdges...)
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// listAllRules paginates through all EventBridge rules using NextToken.
func (c *eventBridgeCollector) listAllRules(ctx context.Context) ([]ebtypes.Rule, error) {
	var (
		rules     []ebtypes.Rule
		nextToken *string
	)

	for {
		out, err := c.client.ListRules(ctx, &eventbridge.ListRulesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("eventbridge: list rules: %w", err)
		}

		rules = append(rules, out.Rules...)

		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	return rules, nil
}

// collectRule builds a resource + TARGETS edges for a single EventBridge rule.
func (c *eventBridgeCollector) collectRule(ctx context.Context, rule ebtypes.Rule) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	ruleARN := awssdk.ToString(rule.Arn)
	ruleName := awssdk.ToString(rule.Name)

	// Build metadata envelope for LLM summarization.
	envelope := eventBridgeRuleContent{
		Name:               ruleName,
		State:              string(rule.State),
		EventPattern:       awssdk.ToString(rule.EventPattern),
		ScheduleExpression: awssdk.ToString(rule.ScheduleExpression),
		Description:        awssdk.ToString(rule.Description),
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("eventbridge: marshal rule %s: %w", ruleName, err)
	}

	res := cloud.ResourceSpec{
		ID:           ruleARN,
		Name:         ruleName,
		ResourceType: "eventbridge-rule",
		Region:       c.region,
		Content:      content,
		Metadata:     eventBridgeRuleMetadata(rule),
	}

	edges, err := c.collectRuleTargets(ctx, ruleName, ruleARN)
	if err != nil {
		return cloud.ResourceSpec{}, nil, err
	}

	return res, edges, nil
}

// eventBridgeRuleContent is the envelope marshaled into node.Content for each rule.
type eventBridgeRuleContent struct {
	Name               string `json:"name"`
	State              string `json:"state"`
	EventPattern       string `json:"event_pattern,omitempty"`
	ScheduleExpression string `json:"schedule_expression,omitempty"`
	Description        string `json:"description,omitempty"`
}

// collectRuleTargets lists targets for a rule and returns TARGETS edges.
func (c *eventBridgeCollector) collectRuleTargets(ctx context.Context, ruleName, ruleARN string) ([]cloud.EdgeSpec, error) {
	var (
		edges     []cloud.EdgeSpec
		nextToken *string
	)

	for {
		out, err := c.client.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{
			Rule:      awssdk.String(ruleName),
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("eventbridge: list targets for rule %s: %w", ruleName, err)
		}

		for _, target := range out.Targets {
			targetARN := awssdk.ToString(target.Arn)
			if targetARN == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ruleARN,
				TargetID:     targetARN,
				Relationship: kgtypes.EdgeTargets,
			})
			// Per-target IAM role: EventBridge assumes this role to invoke
			// the target. Surfacing the linkage lets reachability analysis
			// answer "which rule can act as role X" without re-reading the
			// raw AWS payload.
			if roleARN := awssdk.ToString(target.RoleArn); roleARN != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     ruleARN,
					TargetID:     roleARN,
					Relationship: kgtypes.EdgeUsesSA,
				})
			}
		}

		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	return edges, nil
}

// eventBridgeRuleMetadata extracts discriminating fields from an EventBridge rule.
func eventBridgeRuleMetadata(r ebtypes.Rule) map[string]string {
	m := make(map[string]string, 3)
	if s := string(r.State); s != "" {
		m["state"] = s
	}
	if s := awssdk.ToString(r.ScheduleExpression); s != "" {
		m["schedule_expression"] = s
	}
	if eb := awssdk.ToString(r.EventBusName); eb != "" {
		m["event_bus_name"] = eb
	}
	return m
}
