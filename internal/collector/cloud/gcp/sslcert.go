// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// sslCertificatesSubCollector collects Compute Engine SSL certificates.
// The privateKey field is redacted from Content for security.
//
// LEAF NODE: SSL certificates emit no outbound edges. They are consumed
// by target HTTP(S) proxies via EdgeUsesCert (emitted from
// cloud/gcp/loadbalancer.go). The intended topology shape is:
//
//	targetHttpsProxy → sslCertificate  (EdgeUsesCert)
//
// An orphan rule in topology/orphan_rules_gcp.go flags certificates
// with no incoming EdgeUsesCert edges as cleanup candidates.
type sslCertificatesSubCollector struct {
	client    *compute.SslCertificatesClient
	projectID string
}

func newSSLCertificatesSubCollector(client *compute.SslCertificatesClient, projectID string) *sslCertificatesSubCollector {
	return &sslCertificatesSubCollector{client: client, projectID: projectID}
}

func (c *sslCertificatesSubCollector) Name() string { return "gcp-ssl-certificates" }

func (c *sslCertificatesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListSslCertificatesRequest{
		Project: c.projectID,
	})

	for {
		cert, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := cert.GetSelfLink()
		if selfLink == "" {
			continue
		}

		spec := sslCertResourceSpec(selfLink, cert)
		result.Resources = append(result.Resources, spec)
	}

	return result, nil
}

// sslCertResourceSpec builds a ResourceSpec for an SSL certificate.
// Only metadata is marshaled — privateKey is redacted for security.
func sslCertResourceSpec(selfLink string, cert *computepb.SslCertificate) cloud.ResourceSpec {
	meta := map[string]string{
		"type":       cert.GetType(),
		"expireTime": cert.GetExpireTime(),
	}

	if managed := cert.GetManaged(); managed != nil {
		meta["managed_status"] = managed.GetStatus()
		meta["domains"] = strings.Join(managed.GetDomains(), ",")
	}

	// Marshal only safe metadata — no privateKey or certificate PEM.
	safeContent := map[string]any{
		"name":                  cert.GetName(),
		"selfLink":              selfLink,
		"type":                  cert.GetType(),
		"expireTime":            cert.GetExpireTime(),
		"subjectAlternateNames": cert.GetSubjectAlternativeNames(),
	}
	content, _ := json.Marshal(safeContent) //nolint:errchkjson // best-effort content envelope

	return cloud.ResourceSpec{
		ID:           selfLink,
		Name:         cert.GetName(),
		ResourceType: "gcp:compute:sslCertificate",
		Content:      content,
		Metadata:     meta,
	}
}
