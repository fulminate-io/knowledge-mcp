// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- v2 fakes ---------------------------------------------------------------

type fakeAPIGatewayV2API struct {
	apis         []apigwv2types.Api
	routes       map[string][]apigwv2types.Route       // keyed by ApiId
	integrations map[string][]apigwv2types.Integration // keyed by ApiId
	domains      []apigwv2types.DomainName
}

func (f *fakeAPIGatewayV2API) GetApis(_ context.Context, _ *apigatewayv2.GetApisInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error) {
	return &apigatewayv2.GetApisOutput{Items: f.apis}, nil
}

func (f *fakeAPIGatewayV2API) GetRoutes(_ context.Context, in *apigatewayv2.GetRoutesInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetRoutesOutput, error) {
	return &apigatewayv2.GetRoutesOutput{Items: f.routes[awssdk.ToString(in.ApiId)]}, nil
}

func (f *fakeAPIGatewayV2API) GetIntegrations(_ context.Context, in *apigatewayv2.GetIntegrationsInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetIntegrationsOutput, error) {
	return &apigatewayv2.GetIntegrationsOutput{Items: f.integrations[awssdk.ToString(in.ApiId)]}, nil
}

func (f *fakeAPIGatewayV2API) GetDomainNames(_ context.Context, _ *apigatewayv2.GetDomainNamesInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetDomainNamesOutput, error) {
	return &apigatewayv2.GetDomainNamesOutput{Items: f.domains}, nil
}

func TestAPIGatewayV2Collector_PublicHTTPAPI(t *testing.T) {
	apiID := "httpapi01"
	fake := &fakeAPIGatewayV2API{
		apis: []apigwv2types.Api{{
			ApiId:                    awssdk.String(apiID),
			Name:                     awssdk.String("orders-api"),
			ProtocolType:             apigwv2types.ProtocolTypeHttp,
			RouteSelectionExpression: awssdk.String("${request.method} ${request.path}"),
		}},
		routes: map[string][]apigwv2types.Route{
			apiID: {
				{
					RouteKey:          awssdk.String("GET /orders"),
					AuthorizationType: apigwv2types.AuthorizationTypeNone,
				},
				{
					RouteKey:          awssdk.String("POST /orders"),
					AuthorizationType: apigwv2types.AuthorizationTypeJwt,
				},
			},
		},
	}
	c := &apigatewayv2Collector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	spec := result.Resources[0]
	assert.Equal(t, "apigw:httpapi", spec.ResourceType)
	assert.Equal(t, "arn:aws:apigateway:us-east-1::/apis/httpapi01", spec.ID)
	assert.Equal(t, "orders-api", spec.Name)

	var env v2APIContent
	require.NoError(t, json.Unmarshal(spec.Content, &env))
	require.Len(t, env.Routes, 2)

	var hasNone bool
	for _, r := range env.Routes {
		if r.AuthorizationType == "NONE" {
			hasNone = true
		}
	}
	assert.True(t, hasNone, "expected at least one NONE route")
}

func TestAPIGatewayV2Collector_IntegrationEdges(t *testing.T) {
	lambdaARN := "arn:aws:lambda:us-east-1:111111111111:function:orders-handler"
	apiID := "httpinteg01"
	fake := &fakeAPIGatewayV2API{
		apis: []apigwv2types.Api{{
			ApiId:        awssdk.String(apiID),
			Name:         awssdk.String("orders"),
			ProtocolType: apigwv2types.ProtocolTypeHttp,
		}},
		routes: map[string][]apigwv2types.Route{
			apiID: {{RouteKey: awssdk.String("GET /orders"), AuthorizationType: apigwv2types.AuthorizationTypeNone}},
		},
		integrations: map[string][]apigwv2types.Integration{
			apiID: {
				{IntegrationId: awssdk.String("integ1"), IntegrationUri: awssdk.String(lambdaARN)},
				{IntegrationId: awssdk.String("integ2"), IntegrationUri: awssdk.String("http://example.com")}, // non-Lambda, skipped
			},
		},
	}
	c := &apigatewayv2Collector{client: fake, region: "us-east-1"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Edges, 1)

	assert.Equal(t, "arn:aws:apigateway:us-east-1::/apis/httpinteg01", result.Edges[0].SourceID)
	assert.Equal(t, lambdaARN, result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, result.Edges[0].Relationship)
}

func TestAPIGatewayV2Collector_WebSocketAPI(t *testing.T) {
	apiID := "wsapi01"
	fake := &fakeAPIGatewayV2API{
		apis: []apigwv2types.Api{{
			ApiId:                    awssdk.String(apiID),
			Name:                     awssdk.String("chat-ws"),
			ProtocolType:             apigwv2types.ProtocolTypeWebsocket,
			RouteSelectionExpression: awssdk.String("$request.body.action"),
		}},
		routes: map[string][]apigwv2types.Route{
			apiID: {{
				RouteKey:          awssdk.String("$connect"),
				AuthorizationType: apigwv2types.AuthorizationTypeAwsIam,
			}},
		},
	}
	c := &apigatewayv2Collector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "apigw:wsapi", result.Resources[0].ResourceType)
}

// --- v2 domain cert tests ---------------------------------------------------

func TestAPIGatewayV2Collector_DomainCertEdges(t *testing.T) {
	certARN := "arn:aws:acm:us-east-1:111111111111:certificate/v2-cert"
	domARN := "arn:aws:apigateway:us-east-1::/domainnames/v2.example.com"

	fake := &fakeAPIGatewayV2API{
		domains: []apigwv2types.DomainName{
			{
				DomainName:    awssdk.String("v2.example.com"),
				DomainNameArn: awssdk.String(domARN),
				DomainNameConfigurations: []apigwv2types.DomainNameConfiguration{
					{CertificateArn: awssdk.String(certARN)},
				},
			},
		},
	}
	c := &apigatewayv2Collector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Resources, 1)
	assert.Equal(t, "apigw:domain", result.Resources[0].ResourceType)
	assert.Equal(t, domARN, result.Resources[0].ID)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesCert, result.Edges[0].Relationship)
	assert.Equal(t, certARN, result.Edges[0].TargetID)
}

func TestAPIGatewayV2Collector_DomainNoCert(t *testing.T) {
	fake := &fakeAPIGatewayV2API{
		domains: []apigwv2types.DomainName{
			{DomainName: awssdk.String("nocert.example.com")},
		},
	}
	c := &apigatewayv2Collector{client: fake, region: "us-east-1"}

	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges)
}
