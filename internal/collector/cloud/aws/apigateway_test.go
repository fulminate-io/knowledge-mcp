// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- v1 fakes ---------------------------------------------------------------

type fakeAPIGatewayAPI struct {
	apis      []apigwtypes.RestApi
	resources map[string][]apigwtypes.Resource // keyed by RestApiId
	domains   []apigwtypes.DomainName
}

func (f *fakeAPIGatewayAPI) GetRestApis(_ context.Context, _ *apigateway.GetRestApisInput, _ ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error) {
	return &apigateway.GetRestApisOutput{Items: f.apis}, nil
}

func (f *fakeAPIGatewayAPI) GetResources(_ context.Context, in *apigateway.GetResourcesInput, _ ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error) {
	return &apigateway.GetResourcesOutput{Items: f.resources[awssdk.ToString(in.RestApiId)]}, nil
}

func (f *fakeAPIGatewayAPI) GetDomainNames(_ context.Context, _ *apigateway.GetDomainNamesInput, _ ...func(*apigateway.Options)) (*apigateway.GetDomainNamesOutput, error) {
	return &apigateway.GetDomainNamesOutput{Items: f.domains}, nil
}

func (f *fakeAPIGatewayAPI) GetAuthorizers(_ context.Context, _ *apigateway.GetAuthorizersInput, _ ...func(*apigateway.Options)) (*apigateway.GetAuthorizersOutput, error) {
	return &apigateway.GetAuthorizersOutput{}, nil
}

func TestAPIGatewayCollector_PublicRestAPI(t *testing.T) {
	apiID := "abc123"
	fake := &fakeAPIGatewayAPI{
		apis: []apigwtypes.RestApi{{
			Id:          awssdk.String(apiID),
			Name:        awssdk.String("pet-store"),
			Description: awssdk.String("demo"),
		}},
		resources: map[string][]apigwtypes.Resource{
			apiID: {{
				Id:   awssdk.String("rootres"),
				Path: awssdk.String("/pets"),
				ResourceMethods: map[string]apigwtypes.Method{
					"GET": {
						HttpMethod:        awssdk.String("GET"),
						AuthorizationType: awssdk.String("NONE"),
					},
					"POST": {
						HttpMethod:        awssdk.String("POST"),
						AuthorizationType: awssdk.String("AWS_IAM"),
						ApiKeyRequired:    awssdk.Bool(true),
					},
				},
			}},
		},
	}
	c := &apigatewayCollector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	spec := result.Resources[0]
	assert.Equal(t, "apigw:restapi", spec.ResourceType)
	assert.Equal(t, "arn:aws:apigateway:us-east-1::/restapis/abc123", spec.ID)
	assert.Equal(t, "pet-store", spec.Name)

	var env restAPIContent
	require.NoError(t, json.Unmarshal(spec.Content, &env))
	require.Len(t, env.Methods, 2)

	var hasNone, hasIAM bool
	for _, m := range env.Methods {
		assert.Equal(t, "/pets", m.ResourcePath)
		switch m.AuthorizationType {
		case "NONE":
			hasNone = true
			assert.Equal(t, "GET", m.HTTPMethod)
		case "AWS_IAM":
			hasIAM = true
			assert.Equal(t, "POST", m.HTTPMethod)
			assert.True(t, m.APIKeyRequired)
		}
	}
	assert.True(t, hasNone)
	assert.True(t, hasIAM)
}

func TestAPIGatewayCollector_IntegrationEdges(t *testing.T) {
	lambdaARN := "arn:aws:lambda:us-east-1:111111111111:function:handler"
	apiID := "integ01"
	fake := &fakeAPIGatewayAPI{
		apis: []apigwtypes.RestApi{{Id: awssdk.String(apiID), Name: awssdk.String("with-lambda")}},
		resources: map[string][]apigwtypes.Resource{
			apiID: {{
				Id:   awssdk.String("r1"),
				Path: awssdk.String("/do"),
				ResourceMethods: map[string]apigwtypes.Method{
					"POST": {
						HttpMethod:        awssdk.String("POST"),
						AuthorizationType: awssdk.String("NONE"),
						MethodIntegration: &apigwtypes.Integration{
							Type: apigwtypes.IntegrationTypeAwsProxy,
							Uri:  awssdk.String("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" + lambdaARN + "/invocations"),
						},
					},
				},
			}},
		},
	}
	c := &apigatewayCollector{client: fake, region: "us-east-1"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Edges, 1)

	assert.Equal(t, "arn:aws:apigateway:us-east-1::/restapis/integ01", result.Edges[0].SourceID)
	assert.Equal(t, lambdaARN, result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, result.Edges[0].Relationship)
}

// --- v1 domain cert tests ---------------------------------------------------

func TestAPIGatewayCollector_DomainCertEdges(t *testing.T) {
	edgeCertARN := "arn:aws:acm:us-east-1:111111111111:certificate/edge-cert"
	regionalCertARN := "arn:aws:acm:us-east-1:111111111111:certificate/regional-cert"
	domARN := "arn:aws:apigateway:us-east-1::/domainnames/api.example.com"

	fake := &fakeAPIGatewayAPI{
		domains: []apigwtypes.DomainName{
			{
				DomainName:             awssdk.String("api.example.com"),
				DomainNameArn:          awssdk.String(domARN),
				CertificateArn:         awssdk.String(edgeCertARN),
				RegionalCertificateArn: awssdk.String(regionalCertARN),
			},
		},
	}
	c := &apigatewayCollector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// Should have 1 domain resource.
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "apigw:domain", result.Resources[0].ResourceType)
	assert.Equal(t, domARN, result.Resources[0].ID)
	assert.Equal(t, "api.example.com", result.Resources[0].Name)

	// Should have 2 USES_CERT edges (edge-optimized + regional).
	require.Len(t, result.Edges, 2)
	assert.Equal(t, kgtypes.EdgeUsesCert, result.Edges[0].Relationship)
	assert.Equal(t, edgeCertARN, result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesCert, result.Edges[1].Relationship)
	assert.Equal(t, regionalCertARN, result.Edges[1].TargetID)
}

func TestAPIGatewayCollector_DomainNoCert(t *testing.T) {
	fake := &fakeAPIGatewayAPI{
		domains: []apigwtypes.DomainName{
			{DomainName: awssdk.String("nocert.example.com")},
		},
	}
	c := &apigatewayCollector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges, "no cert ARNs should mean no USES_CERT edges")
}
