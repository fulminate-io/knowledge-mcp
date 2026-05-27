// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestForwardingRulesSubCollector_Name(t *testing.T) {
	c := &forwardingRulesSubCollector{}
	assert.Equal(t, "gcp-forwarding-rules", c.Name())
}

func TestTargetHTTPProxiesSubCollector_Name(t *testing.T) {
	c := &targetHTTPProxiesSubCollector{}
	assert.Equal(t, "gcp-target-http-proxies", c.Name())
}

func TestTargetHTTPSProxiesSubCollector_Name(t *testing.T) {
	c := &targetHTTPSProxiesSubCollector{}
	assert.Equal(t, "gcp-target-https-proxies", c.Name())
}

func TestURLMapsSubCollector_Name(t *testing.T) {
	c := &urlMapsSubCollector{}
	assert.Equal(t, "gcp-url-maps", c.Name())
}

func TestBackendServicesSubCollector_Name(t *testing.T) {
	c := &backendServicesSubCollector{}
	assert.Equal(t, "gcp-backend-services", c.Name())
}

func TestURLMapEdges_DefaultService(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/global/urlMaps/my-map"
	urlMap := &computepb.UrlMap{
		DefaultService: new("https://www.googleapis.com/compute/v1/projects/p/global/backendServices/my-backend"),
	}
	edges := urlMapEdges(selfLink, urlMap)
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
	assert.Equal(t, selfLink, edges[0].SourceID)
	assert.Contains(t, edges[0].TargetID, "backendServices/my-backend")
}

func TestURLMapEdges_PathMatchers(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/global/urlMaps/my-map"
	backend1 := "https://www.googleapis.com/compute/v1/projects/p/global/backendServices/backend-1"
	backend2 := "https://www.googleapis.com/compute/v1/projects/p/global/backendServices/backend-2"
	backend3 := "https://www.googleapis.com/compute/v1/projects/p/global/backendServices/backend-3"

	urlMap := &computepb.UrlMap{
		DefaultService: new(backend1),
		PathMatchers: []*computepb.PathMatcher{
			{
				DefaultService: new(backend2),
				PathRules: []*computepb.PathRule{
					{Service: new(backend3)},
					{Service: new(backend1)}, // duplicate of default — should be deduped
				},
			},
		},
	}
	edges := urlMapEdges(selfLink, urlMap)
	assert.Len(t, edges, 3) // backend1 (default), backend2 (pm default), backend3 (path rule)
	for _, edge := range edges {
		assert.Equal(t, kgtypes.EdgeRoutesTo, edge.Relationship)
	}
}

func TestURLMapEdges_NoService(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/global/urlMaps/my-map"
	urlMap := &computepb.UrlMap{}
	edges := urlMapEdges(selfLink, urlMap)
	assert.Empty(t, edges)
}

func TestHTTPSProxyEdges_UsesCert(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/global/targetHttpsProxies/my-proxy"
	cert1 := "https://www.googleapis.com/compute/v1/projects/p/global/sslCertificates/cert-1"
	cert2 := "https://www.googleapis.com/compute/v1/projects/p/global/sslCertificates/cert-2"
	urlMap := "https://www.googleapis.com/compute/v1/projects/p/global/urlMaps/my-map"

	t.Run("single cert", func(t *testing.T) {
		proxy := &computepb.TargetHttpsProxy{
			UrlMap:          new(urlMap),
			SslCertificates: []string{cert1},
		}
		edges := httpsProxyEdges(selfLink, proxy)
		assert.Len(t, edges, 2) // ROUTES_TO + USES_CERT
		assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
		assert.Equal(t, kgtypes.EdgeUsesCert, edges[1].Relationship)
		assert.Equal(t, selfLink, edges[1].SourceID)
		assert.Equal(t, cert1, edges[1].TargetID)
	})

	t.Run("multiple certs", func(t *testing.T) {
		proxy := &computepb.TargetHttpsProxy{
			UrlMap:          new(urlMap),
			SslCertificates: []string{cert1, cert2},
		}
		edges := httpsProxyEdges(selfLink, proxy)
		assert.Len(t, edges, 3) // ROUTES_TO + 2x USES_CERT
		assert.Equal(t, kgtypes.EdgeUsesCert, edges[1].Relationship)
		assert.Equal(t, cert1, edges[1].TargetID)
		assert.Equal(t, kgtypes.EdgeUsesCert, edges[2].Relationship)
		assert.Equal(t, cert2, edges[2].TargetID)
	})

	t.Run("no certs", func(t *testing.T) {
		proxy := &computepb.TargetHttpsProxy{
			UrlMap: new(urlMap),
		}
		edges := httpsProxyEdges(selfLink, proxy)
		assert.Len(t, edges, 1) // Only ROUTES_TO
		assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
	})
}

func TestBackendServicesSubCollector_CDNMetadata(t *testing.T) {
	t.Run("CDN enabled with cache mode", func(t *testing.T) {
		bs := &computepb.BackendService{
			LoadBalancingScheme: new("EXTERNAL_MANAGED"),
			EnableCDN:           new(true),
			CdnPolicy: &computepb.BackendServiceCdnPolicy{
				CacheMode: new("CACHE_ALL_STATIC"),
			},
		}
		meta := backendServiceMetadata(bs)
		assert.Equal(t, "EXTERNAL_MANAGED", meta["loadBalancingScheme"])
		assert.Equal(t, "true", meta["cdnEnabled"])
		assert.Equal(t, "CACHE_ALL_STATIC", meta["cdnCacheMode"])
	})

	t.Run("CDN disabled", func(t *testing.T) {
		bs := &computepb.BackendService{
			LoadBalancingScheme: new("INTERNAL"),
			EnableCDN:           new(false),
		}
		meta := backendServiceMetadata(bs)
		assert.Equal(t, "INTERNAL", meta["loadBalancingScheme"])
		assert.Equal(t, "false", meta["cdnEnabled"])
		_, hasCacheMode := meta["cdnCacheMode"]
		assert.False(t, hasCacheMode)
	})

	t.Run("CDN enabled without cdn policy", func(t *testing.T) {
		bs := &computepb.BackendService{
			LoadBalancingScheme: new("EXTERNAL_MANAGED"),
			EnableCDN:           new(true),
		}
		meta := backendServiceMetadata(bs)
		assert.Equal(t, "true", meta["cdnEnabled"])
		_, hasCacheMode := meta["cdnCacheMode"]
		assert.False(t, hasCacheMode)
	})
}
