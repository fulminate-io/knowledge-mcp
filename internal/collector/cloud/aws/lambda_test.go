// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLambdaAPI is a minimal in-memory Lambda client for unit tests.
type fakeLambdaAPI struct {
	functions []lambdatypes.FunctionConfiguration
	// urlConfigs keyed by function name. Missing entry => NotFound.
	urlConfigs   map[string]*lambda.GetFunctionUrlConfigOutput
	urlErr       error
	eventSources map[string][]lambdatypes.EventSourceMappingConfiguration
}

func (f *fakeLambdaAPI) ListFunctions(_ context.Context, _ *lambda.ListFunctionsInput, _ ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error) {
	return &lambda.ListFunctionsOutput{Functions: f.functions}, nil
}

func (f *fakeLambdaAPI) ListEventSourceMappings(_ context.Context, in *lambda.ListEventSourceMappingsInput, _ ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
	return &lambda.ListEventSourceMappingsOutput{
		EventSourceMappings: f.eventSources[awssdk.ToString(in.FunctionName)],
	}, nil
}

func (f *fakeLambdaAPI) GetFunctionUrlConfig(_ context.Context, in *lambda.GetFunctionUrlConfigInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionUrlConfigOutput, error) {
	if f.urlErr != nil {
		return nil, f.urlErr
	}
	name := awssdk.ToString(in.FunctionName)
	if out, ok := f.urlConfigs[name]; ok {
		return out, nil
	}
	return nil, &lambdatypes.ResourceNotFoundException{Message: awssdk.String("no url for " + name)}
}

func TestLambdaCollector_FunctionURLPublic(t *testing.T) {
	fnName := "public-fn"
	fnARN := "arn:aws:lambda:us-east-1:111111111111:function:public-fn"
	fake := &fakeLambdaAPI{
		functions: []lambdatypes.FunctionConfiguration{{
			FunctionName: awssdk.String(fnName),
			FunctionArn:  awssdk.String(fnARN),
			Role:         awssdk.String("arn:aws:iam::111111111111:role/LambdaExec"),
		}},
		urlConfigs: map[string]*lambda.GetFunctionUrlConfigOutput{
			fnName: {
				AuthType:    lambdatypes.FunctionUrlAuthTypeNone,
				FunctionArn: awssdk.String(fnARN),
				FunctionUrl: awssdk.String("https://abc.lambda-url.us-east-1.on.aws/"),
			},
		},
	}
	c := &lambdaCollector{client: fake, region: "us-east-1", accountID: "111111111111"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var envelope lambdaFunctionContent
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &envelope))
	require.NotNil(t, envelope.FunctionURLConfig, "function_url_config should be present")
	assert.Equal(t, "NONE", envelope.FunctionURLConfig.AuthType)
	assert.Equal(t, "https://abc.lambda-url.us-east-1.on.aws/", envelope.FunctionURLConfig.FunctionURL)
	assert.Equal(t, fnName, awssdk.ToString(envelope.Function.FunctionName))
}

func TestLambdaCollector_FunctionURLMissing(t *testing.T) {
	fnName := "private-fn"
	fnARN := "arn:aws:lambda:us-east-1:111111111111:function:private-fn"
	fake := &fakeLambdaAPI{
		functions: []lambdatypes.FunctionConfiguration{{
			FunctionName: awssdk.String(fnName),
			FunctionArn:  awssdk.String(fnARN),
		}},
		urlConfigs: map[string]*lambda.GetFunctionUrlConfigOutput{}, // none: ResourceNotFoundException path
	}
	c := &lambdaCollector{client: fake, region: "us-east-1", accountID: "111111111111"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var envelope lambdaFunctionContent
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &envelope))
	assert.Nil(t, envelope.FunctionURLConfig, "function_url_config must be nil on ResourceNotFoundException")
}

func TestLambdaCollector_FunctionURLFailOpen(t *testing.T) {
	fnName := "broken-fn"
	fnARN := "arn:aws:lambda:us-east-1:111111111111:function:broken-fn"
	fake := &fakeLambdaAPI{
		functions: []lambdatypes.FunctionConfiguration{{
			FunctionName: awssdk.String(fnName),
			FunctionArn:  awssdk.String(fnARN),
		}},
		urlErr: errors.New("access denied"),
	}
	c := &lambdaCollector{client: fake, region: "us-east-1", accountID: "111111111111"}

	// Collection must succeed; the URL call failure is logged and swallowed.
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var envelope lambdaFunctionContent
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &envelope))
	assert.Nil(t, envelope.FunctionURLConfig)
}
