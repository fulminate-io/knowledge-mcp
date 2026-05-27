// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// acmAPI is the subset of the ACM client surface used by acmCollector.
// Defining it as an interface lets tests mock ACM without AWS credentials.
type acmAPI interface {
	ListCertificates(ctx context.Context, params *acm.ListCertificatesInput, optFns ...func(*acm.Options)) (*acm.ListCertificatesOutput, error)
	DescribeCertificate(ctx context.Context, params *acm.DescribeCertificateInput, optFns ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error)
}

type acmCollector struct {
	client    acmAPI
	region    string
	accountID string
}

func newACMCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &acmCollector{
		client:    acm.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *acmCollector) Name() string { return "acm" }

func (c *acmCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	var nextToken *string
	for {
		page, err := c.client.ListCertificates(ctx, &acm.ListCertificatesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("acm: list certificates: %w", err)
		}

		for _, summary := range page.CertificateSummaryList {
			res, certEdges, err := c.collectCertificate(ctx, summary)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, res)
			edges = append(edges, certEdges...)
		}

		nextToken = page.NextToken
		if nextToken == nil {
			break
		}
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// collectCertificate calls DescribeCertificate for full details, builds a
// resource, and extracts DNS-validated domain edges.
func (c *acmCollector) collectCertificate(
	ctx context.Context,
	summary acmtypes.CertificateSummary,
) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	certARN := awssdk.ToString(summary.CertificateArn)

	detail, err := c.client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: summary.CertificateArn,
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("acm: describe certificate %s: %w", certARN, err)
	}

	cert := detail.Certificate
	envelope := acmCertificateContent{
		DomainName:   awssdk.ToString(cert.DomainName),
		SANs:         cert.SubjectAlternativeNames,
		Status:       string(cert.Status),
		Type:         string(cert.Type),
		Issuer:       awssdk.ToString(cert.Issuer),
		SerialNumber: awssdk.ToString(cert.Serial),
	}
	if cert.NotBefore != nil {
		envelope.NotBefore = cert.NotBefore.Format(time.RFC3339)
	}
	if cert.NotAfter != nil {
		envelope.NotAfter = cert.NotAfter.Format(time.RFC3339)
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("acm: marshal certificate %s: %w", certARN, err)
	}

	name := awssdk.ToString(cert.DomainName)
	if name == "" {
		parts := strings.Split(certARN, "/")
		name = parts[len(parts)-1]
	}

	edges := validationEdges(certARN, cert.DomainValidationOptions)

	return cloud.ResourceSpec{
		ID:           certARN,
		Name:         name,
		ResourceType: "acm-certificate",
		Region:       c.region,
		Content:      content,
		Metadata:     acmCertificateMetadata(cert),
	}, edges, nil
}

// acmCertificateMetadata extracts discriminating fields from an ACM certificate.
func acmCertificateMetadata(cert *acmtypes.CertificateDetail) map[string]string {
	m := make(map[string]string, 4)
	if s := string(cert.Status); s != "" {
		m["status"] = s
	}
	if t := string(cert.Type); t != "" {
		m["type"] = t
	}
	if d := awssdk.ToString(cert.DomainName); d != "" {
		m["domain_name"] = d
	}
	if cert.NotAfter != nil {
		m["not_after"] = cert.NotAfter.UTC().Format(time.RFC3339)
	}
	return m
}

// validationEdges extracts EdgeValidatedBy edges from ACM DNS validation
// entries. For each DNS-validated domain, we derive the apex domain from
// the validation CNAME record name and emit an edge to a best-effort
// Route53 identifier.
func validationEdges(certARN string, opts []acmtypes.DomainValidation) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	seen := make(map[string]struct{})
	for _, dv := range opts {
		if dv.ValidationMethod != acmtypes.ValidationMethodDns {
			continue
		}
		if dv.ResourceRecord == nil {
			continue
		}
		recordName := awssdk.ToString(dv.ResourceRecord.Name)
		if recordName == "" {
			continue
		}
		apex := domainApex(recordName)
		if apex == "" {
			continue
		}
		if _, ok := seen[apex]; ok {
			continue
		}
		seen[apex] = struct{}{}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     certARN,
			TargetID:     apex,
			Relationship: kgtypes.EdgeValidatedBy,
			Metadata:     map[string]string{"dns_record": recordName},
		})
	}
	return edges
}

// domainApex extracts the apex domain from a DNS validation CNAME record
// name. ACM validation records look like _abc123.foo.example.com — we
// strip leading underscored labels and subdomain labels to extract
// "example.com". The heuristic keeps the last two labels (handles .com,
// .org, .net, .io etc.) which is correct for standard TLDs. Country-code
// second-level domains (co.uk, com.au) would need a public-suffix list
// lookup — acceptable trade-off for v1.
func domainApex(recordName string) string {
	recordName = strings.TrimSuffix(recordName, ".")
	parts := strings.Split(recordName, ".")
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// acmCertificateContent is the envelope marshaled into node.Content for
// each certificate.
type acmCertificateContent struct {
	DomainName   string   `json:"domain_name"`
	SANs         []string `json:"sans,omitempty"`
	Status       string   `json:"status"`
	Type         string   `json:"type"`
	Issuer       string   `json:"issuer,omitempty"`
	SerialNumber string   `json:"serial_number,omitempty"`
	NotBefore    string   `json:"not_before,omitempty"`
	NotAfter     string   `json:"not_after,omitempty"`
}
