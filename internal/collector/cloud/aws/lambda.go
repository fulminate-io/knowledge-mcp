// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// lambdaAPI is the subset of the Lambda client surface used by lambdaCollector.
// Defining it as an interface lets tests mock the Lambda API without AWS
// credentials. The concrete *lambda.Client satisfies this interface.
type lambdaAPI interface {
	ListFunctions(ctx context.Context, params *lambda.ListFunctionsInput, optFns ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
	ListEventSourceMappings(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error)
	GetFunctionUrlConfig(ctx context.Context, params *lambda.GetFunctionUrlConfigInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionUrlConfigOutput, error)
}

type lambdaCollector struct {
	client    lambdaAPI
	region    string
	accountID string
}

func newLambdaCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &lambdaCollector{
		client:    lambda.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *lambdaCollector) Name() string { return "lambda" }

// lambdaFunctionContent is the envelope marshaled into node.Content for each
// Lambda function. It embeds the raw FunctionConfiguration plus optional
// function_url_config populated via a separate GetFunctionUrlConfig call. The
// extra fields are consumed by the topology/public_exposure analyzer seed
// rules (see finding public_exposure: seed catalog v2).
type lambdaFunctionContent struct {
	Function          lambdatypes.FunctionConfiguration `json:"function"`
	FunctionURLConfig *functionURLConfig                `json:"function_url_config,omitempty"`
}

// functionURLConfig captures the public-exposure-relevant subset of the
// Lambda function URL configuration. AuthType=NONE means the endpoint is
// open to the internet. We intentionally omit CreationTime/LastModifiedTime
// to keep the envelope stable across reindexes.
type functionURLConfig struct {
	AuthType    string            `json:"auth_type"`
	FunctionURL string            `json:"function_url,omitempty"`
	InvokeMode  string            `json:"invoke_mode,omitempty"`
	Cors        *lambdatypes.Cors `json:"cors,omitempty"`
}

func (c *lambdaCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := lambda.NewListFunctionsPaginator(c.client, &lambda.ListFunctionsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("lambda: list functions: %w", err)
		}

		for _, fn := range page.Functions {
			fnARN := awssdk.ToString(fn.FunctionArn)
			fnName := awssdk.ToString(fn.FunctionName)

			urlCfg := c.fetchFunctionURLConfig(ctx, fnARN, fnName)
			envelope := lambdaFunctionContent{Function: fn, FunctionURLConfig: urlCfg}
			content, err := json.Marshal(envelope)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("lambda: marshal: %w", err)
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           fnARN,
				Name:         fnName,
				ResourceType: "lambda-function",
				Region:       c.region,
				Content:      content,
				Metadata:     lambdaFunctionMetadata(fn, urlCfg),
			})

			edges = append(edges, c.functionEdges(fnARN, fn)...)

			// Event source mappings: SQS, DynamoDB, Kinesis → Lambda
			esmEdges, err := c.collectEventSourceMappings(ctx, fnARN)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			edges = append(edges, esmEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// fetchFunctionURLConfig calls GetFunctionUrlConfig for a single function.
// Functions without a URL return ResourceNotFoundException — we treat that
// as "no URL configured" and return nil (no error). Other errors fail-open:
// they are logged at warn level and nil is returned so collection continues.
func (c *lambdaCollector) fetchFunctionURLConfig(ctx context.Context, fnARN, fnName string) *functionURLConfig {
	out, err := c.client.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{
		FunctionName: awssdk.String(fnName),
	})
	if err != nil {
		var notFound *lambdatypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		slog.Warn("lambda: get function url config", "function", fnARN, "error", err)
		return nil
	}
	if out == nil {
		return nil
	}
	return &functionURLConfig{
		AuthType:    string(out.AuthType),
		FunctionURL: awssdk.ToString(out.FunctionUrl),
		InvokeMode:  string(out.InvokeMode),
		Cors:        out.Cors,
	}
}

// functionEdges extracts IAM role and VPC edges for a Lambda function.
func (c *lambdaCollector) functionEdges(fnARN string, fn lambdatypes.FunctionConfiguration) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Lambda → IAM Role
	if fn.Role != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     fnARN,
			TargetID:     awssdk.ToString(fn.Role),
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "execution_role"},
		})
	}

	// VPC-connected Lambda → Subnets and Security Groups
	if fn.VpcConfig != nil {
		for _, subnetID := range fn.VpcConfig.SubnetIds {
			subnetARN := ec2ARN(c.region, c.accountID, "subnet", subnetID)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     fnARN,
				TargetID:     subnetARN,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
		for _, sgID := range fn.VpcConfig.SecurityGroupIds {
			sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     fnARN,
				TargetID:     sgARN,
				Relationship: kgtypes.EdgeUsesSecurityGroup,
			})
		}
	}

	return edges
}

// collectEventSourceMappings discovers event triggers (SQS, DynamoDB, Kinesis)
// that feed into a Lambda function and returns TRIGGERS edges.
func (c *lambdaCollector) collectEventSourceMappings(ctx context.Context, fnARN string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	esmPaginator := lambda.NewListEventSourceMappingsPaginator(c.client, &lambda.ListEventSourceMappingsInput{
		FunctionName: awssdk.String(fnARN),
	})
	for esmPaginator.HasMorePages() {
		page, err := esmPaginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("lambda: list event source mappings for %s: %w", fnARN, err)
		}

		for _, mapping := range page.EventSourceMappings {
			if mapping.EventSourceArn == nil {
				continue
			}
			// Edge direction: event source → Lambda function (source triggers function).
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     awssdk.ToString(mapping.EventSourceArn),
				TargetID:     fnARN,
				Relationship: kgtypes.EdgeTriggers,
			})
		}
	}

	return edges, nil
}

// lambdaFunctionMetadata extracts discriminating fields from a Lambda function.
// Includes function URL auth_type when configured (NONE = publicly invokable).
func lambdaFunctionMetadata(fn lambdatypes.FunctionConfiguration, urlCfg *functionURLConfig) map[string]string {
	m := make(map[string]string, 4)
	if r := string(fn.Runtime); r != "" {
		m["runtime"] = r
	}
	if h := awssdk.ToString(fn.Handler); h != "" {
		m["handler"] = h
	}
	if fn.MemorySize != nil {
		m["memory_size"] = fmt.Sprintf("%d", awssdk.ToInt32(fn.MemorySize))
	}
	if urlCfg != nil && urlCfg.AuthType != "" {
		m["function_url_auth_type"] = urlCfg.AuthType
	}
	return m
}
