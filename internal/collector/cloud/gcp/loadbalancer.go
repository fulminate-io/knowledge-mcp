// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Forwarding Rules subcollector ---

// forwardingRulesSubCollector collects global forwarding rules.
type forwardingRulesSubCollector struct {
	client    *compute.GlobalForwardingRulesClient
	projectID string
}

func newForwardingRulesSubCollector(client *compute.GlobalForwardingRulesClient, projectID string) *forwardingRulesSubCollector {
	return &forwardingRulesSubCollector{client: client, projectID: projectID}
}

func (c *forwardingRulesSubCollector) Name() string { return "gcp-forwarding-rules" }

func (c *forwardingRulesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListGlobalForwardingRulesRequest{
		Project: c.projectID,
	})

	for {
		rule, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := rule.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildForwardingRuleContent(rule))
		if err != nil {
			return result, fmt.Errorf("gcp forwarding rules: marshal forwarding rule content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         rule.GetName(),
			ResourceType: "gcp:compute:forwardingRule",
			Region:       extractLast(rule.GetRegion()),
			Content:      content,
			Metadata: map[string]string{
				"ipAddress":           rule.GetIPAddress(),
				"ipProtocol":          rule.GetIPProtocol(),
				"portRange":           rule.GetPortRange(),
				"loadBalancingScheme": rule.GetLoadBalancingScheme(),
			},
		}
		result.Resources = append(result.Resources, spec)

		// ForwardingRule -> target proxy (HTTP or HTTPS).
		if target := rule.GetTarget(); target != "" {
			meta := map[string]string{}
			if v := rule.GetIPProtocol(); v != "" {
				meta["ip_protocol"] = v
			}
			if v := rule.GetPortRange(); v != "" {
				meta["port_range"] = v
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     target,
				Relationship: kgtypes.EdgeTargets,
				Metadata:     meta,
			})
		}
	}

	return result, nil
}

// --- Target HTTP Proxies subcollector ---

// targetHTTPProxiesSubCollector collects global target HTTP proxies.
type targetHTTPProxiesSubCollector struct {
	client    *compute.TargetHttpProxiesClient
	projectID string
}

func newTargetHTTPProxiesSubCollector(client *compute.TargetHttpProxiesClient, projectID string) *targetHTTPProxiesSubCollector {
	return &targetHTTPProxiesSubCollector{client: client, projectID: projectID}
}

func (c *targetHTTPProxiesSubCollector) Name() string { return "gcp-target-http-proxies" }

func (c *targetHTTPProxiesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListTargetHttpProxiesRequest{
		Project: c.projectID,
	})

	for {
		proxy, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := proxy.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildTargetHTTPProxyContent(proxy))
		if err != nil {
			return result, fmt.Errorf("gcp target http proxies: marshal target http proxy content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         proxy.GetName(),
			ResourceType: "gcp:compute:targetHttpProxy",
			Content:      content,
		}
		result.Resources = append(result.Resources, spec)

		// Target HTTP proxy -> URL map.
		if urlMap := proxy.GetUrlMap(); urlMap != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     urlMap,
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}
	}

	return result, nil
}

// --- Target HTTPS Proxies subcollector ---

// targetHTTPSProxiesSubCollector collects global target HTTPS proxies.
type targetHTTPSProxiesSubCollector struct {
	client    *compute.TargetHttpsProxiesClient
	projectID string
}

func newTargetHTTPSProxiesSubCollector(client *compute.TargetHttpsProxiesClient, projectID string) *targetHTTPSProxiesSubCollector {
	return &targetHTTPSProxiesSubCollector{client: client, projectID: projectID}
}

func (c *targetHTTPSProxiesSubCollector) Name() string { return "gcp-target-https-proxies" }

func (c *targetHTTPSProxiesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListTargetHttpsProxiesRequest{
		Project: c.projectID,
	})

	for {
		proxy, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := proxy.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildTargetHTTPSProxyContent(proxy))
		if err != nil {
			return result, fmt.Errorf("gcp target https proxies: marshal target https proxy content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         proxy.GetName(),
			ResourceType: "gcp:compute:targetHttpsProxy",
			Content:      content,
		}
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, httpsProxyEdges(selfLink, proxy)...)
	}

	return result, nil
}

// httpsProxyEdges extracts edges from a target HTTPS proxy:
// ROUTES_TO the URL map and USES_CERT for each SSL certificate.
func httpsProxyEdges(selfLink string, proxy *computepb.TargetHttpsProxy) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	if urlMap := proxy.GetUrlMap(); urlMap != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     urlMap,
			Relationship: kgtypes.EdgeRoutesTo,
		})
	}

	for _, certSelfLink := range proxy.GetSslCertificates() {
		if certSelfLink != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     certSelfLink,
				Relationship: kgtypes.EdgeUsesCert,
			})
		}
	}

	return edges
}

// --- URL Maps subcollector ---

// urlMapsSubCollector collects global URL maps.
type urlMapsSubCollector struct {
	client    *compute.UrlMapsClient
	projectID string
}

func newURLMapsSubCollector(client *compute.UrlMapsClient, projectID string) *urlMapsSubCollector {
	return &urlMapsSubCollector{client: client, projectID: projectID}
}

func (c *urlMapsSubCollector) Name() string { return "gcp-url-maps" }

func (c *urlMapsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListUrlMapsRequest{
		Project: c.projectID,
	})

	for {
		urlMap, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := urlMap.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildURLMapContent(urlMap))
		if err != nil {
			return result, fmt.Errorf("gcp url maps: marshal url map content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         urlMap.GetName(),
			ResourceType: "gcp:compute:urlMap",
			Region:       extractLast(urlMap.GetRegion()),
			Content:      content,
		}
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, urlMapEdges(selfLink, urlMap)...)
	}

	return result, nil
}

// urlMapEdges extracts backend service edges from a URL map.
func urlMapEdges(selfLink string, urlMap *computepb.UrlMap) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Default backend service.
	if ds := urlMap.GetDefaultService(); ds != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     ds,
			Relationship: kgtypes.EdgeRoutesTo,
		})
	}

	// Path matcher default services and path rule services.
	seen := make(map[string]bool)
	if ds := urlMap.GetDefaultService(); ds != "" {
		seen[ds] = true
	}
	for _, pm := range urlMap.GetPathMatchers() {
		if ds := pm.GetDefaultService(); ds != "" && !seen[ds] {
			seen[ds] = true
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     ds,
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}
		for _, pr := range pm.GetPathRules() {
			if svc := pr.GetService(); svc != "" && !seen[svc] {
				seen[svc] = true
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     selfLink,
					TargetID:     svc,
					Relationship: kgtypes.EdgeRoutesTo,
				})
			}
		}
	}

	return edges
}

// --- Backend Services subcollector ---

// backendServicesSubCollector collects global backend services.
type backendServicesSubCollector struct {
	client    *compute.BackendServicesClient
	projectID string
}

func newBackendServicesSubCollector(client *compute.BackendServicesClient, projectID string) *backendServicesSubCollector {
	return &backendServicesSubCollector{client: client, projectID: projectID}
}

func (c *backendServicesSubCollector) Name() string { return "gcp-backend-services" }

func (c *backendServicesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListBackendServicesRequest{
		Project: c.projectID,
	})

	for {
		bs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := bs.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildBackendServiceContent(bs))
		if err != nil {
			return result, fmt.Errorf("gcp backend services: marshal backend service content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         bs.GetName(),
			ResourceType: "gcp:compute:backendService",
			Region:       extractLast(bs.GetRegion()),
			Content:      content,
			Metadata:     backendServiceMetadata(bs),
		}
		result.Resources = append(result.Resources, spec)

		// Backend service -> instance groups / NEGs.
		for _, backend := range bs.GetBackends() {
			if group := backend.GetGroup(); group != "" {
				meta := map[string]string{
					"balancing_mode": backend.GetBalancingMode(),
				}
				result.Edges = append(result.Edges, cloud.EdgeSpec{
					SourceID:     selfLink,
					TargetID:     group,
					Relationship: kgtypes.EdgeTargets,
					Metadata:     meta,
				})
			}
		}

		// Cloud Armor security policy -> backend service.
		if secPolicy := bs.GetSecurityPolicy(); secPolicy != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     secPolicy,
				TargetID:     selfLink,
				Relationship: kgtypes.EdgeProtects,
			})
		}
	}

	return result, nil
}

// backendServiceMetadata builds metadata for a backend service including
// Cloud CDN configuration (cdnEnabled, cdnCacheMode) when present.
func backendServiceMetadata(bs *computepb.BackendService) map[string]string {
	meta := map[string]string{
		"loadBalancingScheme": bs.GetLoadBalancingScheme(),
		"cdnEnabled":          boolStr(bs.GetEnableCDN()),
	}
	if policy := bs.GetCdnPolicy(); policy != nil {
		if mode := policy.GetCacheMode(); mode != "" {
			meta["cdnCacheMode"] = mode
		}
	}
	return meta
}
