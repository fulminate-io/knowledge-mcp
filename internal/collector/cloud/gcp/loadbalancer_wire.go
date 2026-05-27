// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	computepb "cloud.google.com/go/compute/apiv1/computepb"
)

// --- Wire structs (FUL-88: curated content envelopes for load-balancer
// resources, no SDK leak). Field sets frozen in Phase 1 audit
// (session ful-88-gcp-planning).

// forwardingRuleContent is the curated wire shape for gcp:compute:forwardingRule.
// IPAddress / IPProtocol JSON tags use the acronym uppercase form (NOT
// ipAddress / ipProtocol) — existing readers depend on this exact spelling
// (T3#1 reviewer finding; postpopulate_dns.go:207 + cloud/k8s/postpopulate_cloud_lb_index.go:62).
// This struct supersedes the local forwardingRuleContent in postpopulate_dns.go
// (Phase 3 reader convergence drops the duplicate).
type forwardingRuleContent struct {
	Name                string `json:"name,omitempty"`
	SelfLink            string `json:"selfLink,omitempty"`
	Region              string `json:"region,omitempty"`
	IPAddress           string `json:"IPAddress,omitempty"`
	IPProtocol          string `json:"IPProtocol,omitempty"`
	PortRange           string `json:"portRange,omitempty"`
	LoadBalancingScheme string `json:"loadBalancingScheme,omitempty"`
	Target              string `json:"target,omitempty"`
}

// targetHTTPProxyContent is the curated wire shape for gcp:compute:targetHttpProxy.
type targetHTTPProxyContent struct {
	Name     string `json:"name,omitempty"`
	SelfLink string `json:"selfLink,omitempty"`
	UrlMap   string `json:"urlMap,omitempty"`
}

// targetHTTPSProxyContent is the curated wire shape for gcp:compute:targetHttpsProxy.
type targetHTTPSProxyContent struct {
	Name            string   `json:"name,omitempty"`
	SelfLink        string   `json:"selfLink,omitempty"`
	UrlMap          string   `json:"urlMap,omitempty"`
	SslCertificates []string `json:"sslCertificates,omitempty"`
}

// urlMapContent is the curated wire shape for gcp:compute:urlMap.
type urlMapContent struct {
	Name           string                     `json:"name,omitempty"`
	SelfLink       string                     `json:"selfLink,omitempty"`
	Region         string                     `json:"region,omitempty"`
	DefaultService string                     `json:"defaultService,omitempty"`
	PathMatchers   []urlMapContentPathMatcher `json:"pathMatchers,omitempty"`
}

type urlMapContentPathMatcher struct {
	DefaultService string                  `json:"defaultService,omitempty"`
	PathRules      []urlMapContentPathRule `json:"pathRules,omitempty"`
}

type urlMapContentPathRule struct {
	Service string `json:"service,omitempty"`
}

// backendServiceContent is the curated wire shape for gcp:compute:backendService.
type backendServiceContent struct {
	Name                string                          `json:"name,omitempty"`
	SelfLink            string                          `json:"selfLink,omitempty"`
	Region              string                          `json:"region,omitempty"`
	LoadBalancingScheme string                          `json:"loadBalancingScheme,omitempty"`
	EnableCDN           *bool                           `json:"enableCDN,omitempty"`
	CdnPolicy           *backendServiceContentCDNPolicy `json:"cdnPolicy,omitempty"`
	Backends            []backendServiceContentBackend  `json:"backends,omitempty"`
	SecurityPolicy      string                          `json:"securityPolicy,omitempty"`
}

type backendServiceContentCDNPolicy struct {
	CacheMode string `json:"cacheMode,omitempty"`
}

type backendServiceContentBackend struct {
	Group         string `json:"group,omitempty"`
	BalancingMode string `json:"balancingMode,omitempty"`
}

// --- Projectors ---

// buildForwardingRuleContent projects a *computepb.ForwardingRule into the curated wire shape.
func buildForwardingRuleContent(r *computepb.ForwardingRule) forwardingRuleContent {
	return forwardingRuleContent{
		Name:                r.GetName(),
		SelfLink:            r.GetSelfLink(),
		Region:              r.GetRegion(),
		IPAddress:           r.GetIPAddress(),
		IPProtocol:          r.GetIPProtocol(),
		PortRange:           r.GetPortRange(),
		LoadBalancingScheme: r.GetLoadBalancingScheme(),
		Target:              r.GetTarget(),
	}
}

// buildTargetHTTPProxyContent projects a *computepb.TargetHttpProxy into the curated wire shape.
func buildTargetHTTPProxyContent(p *computepb.TargetHttpProxy) targetHTTPProxyContent {
	return targetHTTPProxyContent{
		Name:     p.GetName(),
		SelfLink: p.GetSelfLink(),
		UrlMap:   p.GetUrlMap(),
	}
}

// buildTargetHTTPSProxyContent projects a *computepb.TargetHttpsProxy into the curated wire shape.
func buildTargetHTTPSProxyContent(p *computepb.TargetHttpsProxy) targetHTTPSProxyContent {
	return targetHTTPSProxyContent{
		Name:            p.GetName(),
		SelfLink:        p.GetSelfLink(),
		UrlMap:          p.GetUrlMap(),
		SslCertificates: p.GetSslCertificates(),
	}
}

// buildURLMapContent projects a *computepb.UrlMap into the curated wire shape.
func buildURLMapContent(u *computepb.UrlMap) urlMapContent {
	out := urlMapContent{
		Name:           u.GetName(),
		SelfLink:       u.GetSelfLink(),
		Region:         u.GetRegion(),
		DefaultService: u.GetDefaultService(),
	}
	for _, pm := range u.GetPathMatchers() {
		if pm == nil {
			continue
		}
		out.PathMatchers = append(out.PathMatchers, projectURLMapPathMatcher(pm))
	}
	return out
}

func projectURLMapPathMatcher(pm *computepb.PathMatcher) urlMapContentPathMatcher {
	out := urlMapContentPathMatcher{
		DefaultService: pm.GetDefaultService(),
	}
	for _, pr := range pm.GetPathRules() {
		if pr == nil {
			continue
		}
		out.PathRules = append(out.PathRules, urlMapContentPathRule{
			Service: pr.GetService(),
		})
	}
	return out
}

// buildBackendServiceContent projects a *computepb.BackendService into the curated wire shape.
func buildBackendServiceContent(bs *computepb.BackendService) backendServiceContent {
	out := backendServiceContent{
		Name:                bs.GetName(),
		SelfLink:            bs.GetSelfLink(),
		Region:              bs.GetRegion(),
		LoadBalancingScheme: bs.GetLoadBalancingScheme(),
		SecurityPolicy:      bs.GetSecurityPolicy(),
	}
	if bs.EnableCDN != nil {
		b := *bs.EnableCDN
		out.EnableCDN = &b
	}
	out.CdnPolicy = projectBackendServiceCDNPolicy(bs.GetCdnPolicy())
	for _, b := range bs.GetBackends() {
		if b == nil {
			continue
		}
		out.Backends = append(out.Backends, backendServiceContentBackend{
			Group:         b.GetGroup(),
			BalancingMode: b.GetBalancingMode(),
		})
	}
	return out
}

func projectBackendServiceCDNPolicy(p *computepb.BackendServiceCdnPolicy) *backendServiceContentCDNPolicy {
	if p == nil {
		return nil
	}
	mode := p.GetCacheMode()
	if mode == "" {
		return nil
	}
	return &backendServiceContentCDNPolicy{CacheMode: mode}
}
