// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// apigatewayv2API is the subset of the API Gateway v2 client surface used by
// apigatewayv2Collector. Defining it as an interface lets tests mock the SDK
// without AWS credentials.
type apigatewayv2API interface {
	GetApis(ctx context.Context, params *apigatewayv2.GetApisInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error)
	GetRoutes(ctx context.Context, params *apigatewayv2.GetRoutesInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetRoutesOutput, error)
	GetIntegrations(ctx context.Context, params *apigatewayv2.GetIntegrationsInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetIntegrationsOutput, error)
	GetDomainNames(ctx context.Context, params *apigatewayv2.GetDomainNamesInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetDomainNamesOutput, error)
}

type apigatewayv2Collector struct {
	client apigatewayv2API
	region string
}

func newAPIGatewayV2Collector(cfg awssdk.Config, region string) cloud.SubCollector {
	return &apigatewayv2Collector{
		client: apigatewayv2.NewFromConfig(cfg),
		region: region,
	}
}

func (c *apigatewayv2Collector) Name() string { return "apigatewayv2" }

// v2APIContent is the envelope marshaled into node.Content for each v2 API.
// Embeds the raw Api plus a flattened route list with each route's
// authorization type, consumed by the topology/public_exposure analyzer.
type v2APIContent struct {
	API    apigwv2types.Api `json:"api"`
	Routes []v2APIRoute     `json:"routes,omitempty"`
}

// v2APIRoute captures the public-exposure-relevant subset of a v2 route.
// AuthorizationType values: NONE, AWS_IAM, JWT, CUSTOM.
type v2APIRoute struct {
	RouteKey          string `json:"route_key"`
	AuthorizationType string `json:"authorization_type"`
	APIKeyRequired    bool   `json:"api_key_required,omitempty"`
}

func (c *apigatewayv2Collector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	var nextToken *string
	for {
		page, err := c.client.GetApis(ctx, &apigatewayv2.GetApisInput{NextToken: nextToken})
		if err != nil {
			slog.Warn("apigatewayv2: get apis", "region", c.region, "error", err)
			return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
		}

		for _, api := range page.Items {
			spec, apiEdges, err := c.buildV2APIResource(ctx, api)
			if err != nil {
				slog.Warn("apigatewayv2: build api resource", "api_id", awssdk.ToString(api.ApiId), "error", err)
				continue
			}
			resources = append(resources, spec)
			edges = append(edges, apiEdges...)
		}

		if page.NextToken == nil || awssdk.ToString(page.NextToken) == "" {
			break
		}
		nextToken = page.NextToken
	}

	// Collect custom domain names.
	domResources, domEdges := c.collectV2DomainNames(ctx)
	resources = append(resources, domResources...)
	edges = append(edges, domEdges...)

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// buildV2APIResource enumerates routes and integrations of a single v2 API and
// returns a ResourceSpec plus TARGETS edges to integration backends.
func (c *apigatewayv2Collector) buildV2APIResource(ctx context.Context, api apigwv2types.Api) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	apiID := awssdk.ToString(api.ApiId)
	apiName := awssdk.ToString(api.Name)

	routes := c.collectV2APIRoutes(ctx, apiID)
	content, err := json.Marshal(v2APIContent{API: api, Routes: routes})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("marshal: %w", err)
	}

	// API Gateway v2 ARN has an empty account segment (same as v1).
	apiARN := fmt.Sprintf("arn:aws:apigateway:%s::/apis/%s", c.region, apiID)

	edges := c.collectV2IntegrationEdges(ctx, apiID, apiARN)

	return cloud.ResourceSpec{
		ID:           apiARN,
		Name:         apiName,
		ResourceType: resourceTypeForProtocol(api.ProtocolType),
		Region:       c.region,
		Content:      content,
		Metadata:     apigatewayV2APIMetadata(api),
	}, edges, nil
}

// apigatewayV2APIMetadata extracts discriminating fields from a v2 API.
func apigatewayV2APIMetadata(api apigwv2types.Api) map[string]string {
	m := make(map[string]string, 2)
	if p := string(api.ProtocolType); p != "" {
		m["protocol_type"] = p
	}
	if api.DisableExecuteApiEndpoint != nil && *api.DisableExecuteApiEndpoint {
		m["disable_execute_api_endpoint"] = "true"
	}
	return m
}

// resourceTypeForProtocol maps the v2 ProtocolType to the canonical
// resource_type string used by the seed catalog.
func resourceTypeForProtocol(p apigwv2types.ProtocolType) string {
	switch p {
	case apigwv2types.ProtocolTypeWebsocket:
		return "apigw:wsapi"
	default:
		return "apigw:httpapi"
	}
}

// collectV2APIRoutes walks all routes of a v2 API via manual NextToken
// pagination (v2 SDK does not expose NewGetRoutesPaginator). Fail-open on
// error: returns routes collected so far.
func (c *apigatewayv2Collector) collectV2APIRoutes(ctx context.Context, apiID string) []v2APIRoute {
	var routes []v2APIRoute

	var nextToken *string
	for {
		page, err := c.client.GetRoutes(ctx, &apigatewayv2.GetRoutesInput{
			ApiId:     awssdk.String(apiID),
			NextToken: nextToken,
		})
		if err != nil {
			slog.Warn("apigatewayv2: get routes", "api_id", apiID, "error", err)
			return routes
		}
		for _, route := range page.Items {
			routes = append(routes, v2APIRoute{
				RouteKey:          awssdk.ToString(route.RouteKey),
				AuthorizationType: string(route.AuthorizationType),
				APIKeyRequired:    awssdk.ToBool(route.ApiKeyRequired),
			})
		}
		if page.NextToken == nil || awssdk.ToString(page.NextToken) == "" {
			break
		}
		nextToken = page.NextToken
	}
	return routes
}

// collectV2IntegrationEdges fetches integrations for a v2 API and returns
// TARGETS edges for Lambda and HTTP backends. Fail-open on error.
func (c *apigatewayv2Collector) collectV2IntegrationEdges(ctx context.Context, apiID, apiARN string) []cloud.EdgeSpec {
	var (
		edges []cloud.EdgeSpec
		seen  = make(map[string]struct{})
		next  *string
	)
	for {
		page, err := c.client.GetIntegrations(ctx, &apigatewayv2.GetIntegrationsInput{
			ApiId:     awssdk.String(apiID),
			NextToken: next,
		})
		if err != nil {
			slog.Warn("apigatewayv2: get integrations", "api_id", apiID, "error", err)
			return edges
		}
		for _, integ := range page.Items {
			uri := awssdk.ToString(integ.IntegrationUri)
			if uri == "" {
				continue
			}
			// Lambda integrations: URI is the Lambda function ARN.
			if !strings.HasPrefix(uri, "arn:aws:lambda:") {
				continue
			}
			if _, ok := seen[uri]; ok {
				continue
			}
			seen[uri] = struct{}{}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     apiARN,
				TargetID:     uri,
				Relationship: kgtypes.EdgeTargets,
			})
		}
		if page.NextToken == nil || awssdk.ToString(page.NextToken) == "" {
			break
		}
		next = page.NextToken
	}
	return edges
}

// collectV2DomainNames fetches custom domain names and returns them as
// ResourceSpecs with USES_CERT edges for each domain name configuration
// that references an ACM certificate.
func (c *apigatewayv2Collector) collectV2DomainNames(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
		next      *string
	)
	for {
		page, err := c.client.GetDomainNames(ctx, &apigatewayv2.GetDomainNamesInput{NextToken: next})
		if err != nil {
			slog.Warn("apigatewayv2: get domain names", "region", c.region, "error", err)
			return resources, edges
		}
		for _, dom := range page.Items {
			domName := awssdk.ToString(dom.DomainName)
			domARN := awssdk.ToString(dom.DomainNameArn)
			if domARN == "" {
				domARN = fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s", c.region, domName)
			}

			content, err := json.Marshal(dom)
			if err != nil {
				slog.Warn("apigatewayv2: marshal domain", "domain", domName, "error", err)
				continue
			}
			resources = append(resources, cloud.ResourceSpec{
				ID:           domARN,
				Name:         domName,
				ResourceType: "apigw:domain",
				Region:       c.region,
				Content:      content,
				Metadata:     apigatewayV2DomainMetadata(dom),
			})

			// Each domain name configuration may reference an ACM certificate.
			for _, cfg := range dom.DomainNameConfigurations {
				if certARN := awssdk.ToString(cfg.CertificateArn); certARN != "" {
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     domARN,
						TargetID:     certARN,
						Relationship: kgtypes.EdgeUsesCert,
					})
				}
			}
		}
		if page.NextToken == nil || awssdk.ToString(page.NextToken) == "" {
			break
		}
		next = page.NextToken
	}
	return resources, edges
}

// apigatewayV2DomainMetadata extracts discriminating fields from a v2 domain.
func apigatewayV2DomainMetadata(d apigwv2types.DomainName) map[string]string {
	m := make(map[string]string, 1)
	if len(d.DomainNameConfigurations) > 0 {
		if et := string(d.DomainNameConfigurations[0].EndpointType); et != "" {
			m["endpoint_type"] = et
		}
	}
	return m
}
