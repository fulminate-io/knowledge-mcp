// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// apigatewayAPI is the subset of the API Gateway v1 client surface used by
// apigatewayCollector. Defining it as an interface lets tests mock the SDK
// without AWS credentials.
type apigatewayAPI interface {
	GetRestApis(ctx context.Context, params *apigateway.GetRestApisInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	GetResources(ctx context.Context, params *apigateway.GetResourcesInput, optFns ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	GetDomainNames(ctx context.Context, params *apigateway.GetDomainNamesInput, optFns ...func(*apigateway.Options)) (*apigateway.GetDomainNamesOutput, error)
	GetAuthorizers(ctx context.Context, params *apigateway.GetAuthorizersInput, optFns ...func(*apigateway.Options)) (*apigateway.GetAuthorizersOutput, error)
}

type apigatewayCollector struct {
	client apigatewayAPI
	region string
}

func newAPIGatewayCollector(cfg awssdk.Config, region string) cloud.SubCollector {
	return &apigatewayCollector{
		client: apigateway.NewFromConfig(cfg),
		region: region,
	}
}

func (c *apigatewayCollector) Name() string { return "apigateway" }

// restAPIContent is the envelope marshaled into node.Content for each REST
// API. Embeds the raw RestApi plus a flattened method list with each method's
// authorization type, consumed by the topology/public_exposure analyzer.
type restAPIContent struct {
	RestAPI apigwtypes.RestApi `json:"rest_api"`
	Methods []restAPIMethod    `json:"methods,omitempty"`
}

// restAPIMethod captures the public-exposure-relevant subset of a v1 method.
// AuthorizationType values: NONE, AWS_IAM, COGNITO_USER_POOLS, CUSTOM.
type restAPIMethod struct {
	HTTPMethod        string `json:"http_method"`
	ResourcePath      string `json:"resource_path"`
	AuthorizationType string `json:"authorization_type"`
	APIKeyRequired    bool   `json:"api_key_required,omitempty"`
}

func (c *apigatewayCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	apisPaginator := apigateway.NewGetRestApisPaginator(c.client, &apigateway.GetRestApisInput{})
	for apisPaginator.HasMorePages() {
		page, err := apisPaginator.NextPage(ctx)
		if err != nil {
			// Fail-open: the account may lack apigateway:GetRestApis.
			slog.Warn("apigateway: get rest apis", "region", c.region, "error", err)
			return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
		}

		for _, api := range page.Items {
			spec, apiEdges, err := c.buildRestAPIResource(ctx, api)
			if err != nil {
				slog.Warn("apigateway: build rest api resource", "api_id", awssdk.ToString(api.Id), "error", err)
				continue
			}
			resources = append(resources, spec)
			edges = append(edges, apiEdges...)
		}
	}

	// Collect custom domain names.
	domResources, domEdges := c.collectDomainNames(ctx)
	resources = append(resources, domResources...)
	edges = append(edges, domEdges...)

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// buildRestAPIResource enumerates all resources+methods of a single REST API
// and returns a ResourceSpec, integration edges, and the populated envelope.
func (c *apigatewayCollector) buildRestAPIResource(ctx context.Context, api apigwtypes.RestApi) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	apiID := awssdk.ToString(api.Id)
	apiName := awssdk.ToString(api.Name)

	methods, integrationEdges := c.collectRestAPIMethods(ctx, apiID)
	content, err := json.Marshal(restAPIContent{RestAPI: api, Methods: methods})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("marshal: %w", err)
	}

	// API Gateway v1 REST API ARN has an empty account segment.
	apiARN := fmt.Sprintf("arn:aws:apigateway:%s::/restapis/%s", c.region, apiID)

	// Rewrite edge sources: collectRestAPIMethods uses "" placeholder.
	for i := range integrationEdges {
		integrationEdges[i].SourceID = apiARN
	}

	// Collect authorizer Lambda edges.
	authEdges := c.collectAuthorizerEdges(ctx, apiID, apiARN)
	integrationEdges = append(integrationEdges, authEdges...)

	return cloud.ResourceSpec{
		ID:           apiARN,
		Name:         apiName,
		ResourceType: "apigw:restapi",
		Region:       c.region,
		Content:      content,
		Metadata:     apigatewayRestAPIMetadata(api),
	}, integrationEdges, nil
}

// apigatewayRestAPIMetadata extracts discriminating fields from a v1 REST API.
func apigatewayRestAPIMetadata(api apigwtypes.RestApi) map[string]string {
	m := make(map[string]string, 2)
	if api.EndpointConfiguration != nil && len(api.EndpointConfiguration.Types) > 0 {
		m["endpoint_type"] = string(api.EndpointConfiguration.Types[0])
	}
	if api.DisableExecuteApiEndpoint {
		m["disable_execute_api_endpoint"] = "true"
	}
	return m
}

// collectRestAPIMethods walks all resources of a REST API (using embed=methods
// to inline Method objects) and returns a flattened method list plus TARGETS
// edges for Lambda integration backends. Edge SourceID is empty; the caller
// fills it in after building the API ARN. Fail-open on error.
func (c *apigatewayCollector) collectRestAPIMethods(ctx context.Context, apiID string) ([]restAPIMethod, []cloud.EdgeSpec) {
	var (
		methods []restAPIMethod
		edges   []cloud.EdgeSpec
		seen    = make(map[string]struct{})
	)

	resPaginator := apigateway.NewGetResourcesPaginator(c.client, &apigateway.GetResourcesInput{
		RestApiId: awssdk.String(apiID),
		Embed:     []string{"methods"},
	})
	for resPaginator.HasMorePages() {
		page, err := resPaginator.NextPage(ctx)
		if err != nil {
			slog.Warn("apigateway: get resources", "api_id", apiID, "error", err)
			return methods, edges
		}
		for _, res := range page.Items {
			resourcePath := awssdk.ToString(res.Path)
			for httpMethod, method := range res.ResourceMethods {
				methods = append(methods, restAPIMethod{
					HTTPMethod:        httpMethod,
					ResourcePath:      resourcePath,
					AuthorizationType: awssdk.ToString(method.AuthorizationType),
					APIKeyRequired:    awssdk.ToBool(method.ApiKeyRequired),
				})

				if lambdaARN := extractLambdaARNFromIntegrationURI(method.MethodIntegration); lambdaARN != "" {
					if _, ok := seen[lambdaARN]; !ok {
						seen[lambdaARN] = struct{}{}
						edges = append(edges, cloud.EdgeSpec{
							TargetID:     lambdaARN,
							Relationship: kgtypes.EdgeTargets,
						})
					}
				}
			}
		}
	}
	return methods, edges
}

// collectAuthorizerEdges fetches Lambda authorizers for a REST API and returns
// TARGETS edges to the authorizer Lambda functions.
func (c *apigatewayCollector) collectAuthorizerEdges(ctx context.Context, apiID, apiARN string) []cloud.EdgeSpec {
	out, err := c.client.GetAuthorizers(ctx, &apigateway.GetAuthorizersInput{
		RestApiId: awssdk.String(apiID),
	})
	if err != nil {
		slog.Debug("apigateway: get authorizers", "api_id", apiID, "error", err)
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, auth := range out.Items {
		lambdaARN := extractLambdaARNFromAuthorizerURI(awssdk.ToString(auth.AuthorizerUri))
		if lambdaARN != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     apiARN,
				TargetID:     lambdaARN,
				Relationship: kgtypes.EdgeTargets,
				Metadata:     map[string]string{"target_type": "authorizer"},
			})
		}
	}
	return edges
}

// extractLambdaARNFromAuthorizerURI extracts a Lambda ARN from an authorizer
// URI. The format is the same as integration URIs:
// arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<arn>/invocations
func extractLambdaARNFromAuthorizerURI(uri string) string {
	const marker = "/functions/"
	_, after, ok := strings.Cut(uri, marker)
	if !ok {
		return ""
	}
	rest := after
	if end := strings.Index(rest, "/"); end >= 0 {
		rest = rest[:end]
	}
	if !strings.HasPrefix(rest, "arn:aws:lambda:") {
		return ""
	}
	return rest
}

// collectDomainNames fetches custom domain names and returns them as
// ResourceSpecs with USES_CERT edges for any associated ACM certificates.
func (c *apigatewayCollector) collectDomainNames(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := apigateway.NewGetDomainNamesPaginator(c.client, &apigateway.GetDomainNamesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			slog.Warn("apigateway: get domain names", "region", c.region, "error", err)
			return resources, edges
		}
		for _, dom := range page.Items {
			domName := awssdk.ToString(dom.DomainName)
			domARN := awssdk.ToString(dom.DomainNameArn)
			if domARN == "" {
				// Fallback: construct a synthetic ID when ARN is absent.
				domARN = fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s", c.region, domName)
			}

			content, err := json.Marshal(dom)
			if err != nil {
				slog.Warn("apigateway: marshal domain", "domain", domName, "error", err)
				continue
			}
			resources = append(resources, cloud.ResourceSpec{
				ID:           domARN,
				Name:         domName,
				ResourceType: "apigw:domain",
				Region:       c.region,
				Content:      content,
				Metadata:     apigatewayDomainMetadata(dom),
			})

			// Edge-optimized endpoint certificate.
			if certARN := awssdk.ToString(dom.CertificateArn); certARN != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     domARN,
					TargetID:     certARN,
					Relationship: kgtypes.EdgeUsesCert,
				})
			}
			// Regional endpoint certificate.
			if certARN := awssdk.ToString(dom.RegionalCertificateArn); certARN != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     domARN,
					TargetID:     certARN,
					Relationship: kgtypes.EdgeUsesCert,
				})
			}
		}
	}
	return resources, edges
}

// extractLambdaARNFromIntegrationURI parses a Lambda function ARN from an API
// Gateway integration URI. Lambda proxy integrations use the format:
// arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<arn>/invocations
func extractLambdaARNFromIntegrationURI(integration *apigwtypes.Integration) string {
	if integration == nil || integration.Uri == nil {
		return ""
	}
	uri := awssdk.ToString(integration.Uri)
	const marker = "/functions/"
	_, after, ok := strings.Cut(uri, marker)
	if !ok {
		return ""
	}
	rest := after
	if end := strings.Index(rest, "/"); end >= 0 {
		rest = rest[:end]
	}
	if !strings.HasPrefix(rest, "arn:aws:lambda:") {
		return ""
	}
	return rest
}

// apigatewayDomainMetadata extracts discriminating fields from a v1 domain.
func apigatewayDomainMetadata(d apigwtypes.DomainName) map[string]string {
	m := make(map[string]string, 2)
	if d.EndpointConfiguration != nil && len(d.EndpointConfiguration.Types) > 0 {
		m["endpoint_type"] = string(d.EndpointConfiguration.Types[0])
	}
	if s := string(d.DomainNameStatus); s != "" {
		m["status"] = s
	}
	return m
}
