// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestACMCollector_Name(t *testing.T) {
	c := &acmCollector{}
	assert.Equal(t, "acm", c.Name())
}

func TestACMCertificateContent_ExpiryFields(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	later := now.Add(90 * 24 * time.Hour)

	envelope := acmCertificateContent{
		DomainName: "example.com",
		Status:     "ISSUED",
		Type:       "AMAZON_ISSUED",
		NotBefore:  now.Format(time.RFC3339),
		NotAfter:   later.Format(time.RFC3339),
	}

	data, err := json.Marshal(envelope)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, now.Format(time.RFC3339), parsed["not_before"])
	assert.Equal(t, later.Format(time.RFC3339), parsed["not_after"])
}

func TestACMCertificateContent_OmitsEmptyExpiry(t *testing.T) {
	envelope := acmCertificateContent{
		DomainName: "example.com",
		Status:     "ISSUED",
		Type:       "AMAZON_ISSUED",
	}

	data, err := json.Marshal(envelope)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	_, hasNotBefore := parsed["not_before"]
	_, hasNotAfter := parsed["not_after"]
	assert.False(t, hasNotBefore, "not_before should be omitted when empty")
	assert.False(t, hasNotAfter, "not_after should be omitted when empty")
}

// --- Fake ACM client for collect-time tests ---

type fakeACMAPI struct {
	summaries []acmtypes.CertificateSummary
	details   map[string]*acmtypes.CertificateDetail // keyed by ARN
}

func (f *fakeACMAPI) ListCertificates(_ context.Context, _ *acm.ListCertificatesInput, _ ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return &acm.ListCertificatesOutput{
		CertificateSummaryList: f.summaries,
	}, nil
}

func (f *fakeACMAPI) DescribeCertificate(_ context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	arn := awssdk.ToString(in.CertificateArn)
	if detail, ok := f.details[arn]; ok {
		return &acm.DescribeCertificateOutput{Certificate: detail}, nil
	}
	return &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{
		CertificateArn: in.CertificateArn,
		DomainName:     awssdk.String("unknown.example.com"),
		Status:         acmtypes.CertificateStatusIssued,
		Type:           acmtypes.CertificateTypeAmazonIssued,
	}}, nil
}

// TestACMCollector_ValidatedByEdge verifies that a DNS-validated cert
// emits EdgeValidatedBy from the cert ARN to the apex domain.
func TestACMCollector_ValidatedByEdge(t *testing.T) {
	certARN := "arn:aws:acm:us-east-1:111111111111:certificate/abc-123"
	fake := &fakeACMAPI{
		summaries: []acmtypes.CertificateSummary{
			{CertificateArn: awssdk.String(certARN)},
		},
		details: map[string]*acmtypes.CertificateDetail{
			certARN: {
				CertificateArn: awssdk.String(certARN),
				DomainName:     awssdk.String("foo.example.com"),
				Status:         acmtypes.CertificateStatusIssued,
				Type:           acmtypes.CertificateTypeAmazonIssued,
				DomainValidationOptions: []acmtypes.DomainValidation{{
					DomainName:       awssdk.String("foo.example.com"),
					ValidationMethod: acmtypes.ValidationMethodDns,
					ResourceRecord: &acmtypes.ResourceRecord{
						Name: awssdk.String("_abc.foo.example.com."),
					},
				}},
			},
		},
	}

	c := &acmCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeValidatedBy {
			assert.Equal(t, certARN, e.SourceID)
			assert.Equal(t, "example.com", e.TargetID)
			assert.Contains(t, e.Metadata["dns_record"], "_abc.foo.example.com")
			found = true
		}
	}
	assert.True(t, found, "expected EdgeValidatedBy edge")
}

// TestACMCollector_NoEdgeForEmailValidation ensures email-validated certs
// do not emit ValidatedBy edges.
func TestACMCollector_NoEdgeForEmailValidation(t *testing.T) {
	certARN := "arn:aws:acm:us-east-1:111111111111:certificate/email-cert"
	fake := &fakeACMAPI{
		summaries: []acmtypes.CertificateSummary{
			{CertificateArn: awssdk.String(certARN)},
		},
		details: map[string]*acmtypes.CertificateDetail{
			certARN: {
				CertificateArn: awssdk.String(certARN),
				DomainName:     awssdk.String("example.com"),
				Status:         acmtypes.CertificateStatusIssued,
				Type:           acmtypes.CertificateTypeAmazonIssued,
				DomainValidationOptions: []acmtypes.DomainValidation{{
					DomainName:       awssdk.String("example.com"),
					ValidationMethod: acmtypes.ValidationMethodEmail,
				}},
			},
		},
	}

	c := &acmCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Edges)
}

// TestACMCollector_DeduplicatesApex verifies that two SANs sharing the
// same apex domain produce only one ValidatedBy edge.
func TestACMCollector_DeduplicatesApex(t *testing.T) {
	certARN := "arn:aws:acm:us-east-1:111111111111:certificate/dedup"
	fake := &fakeACMAPI{
		summaries: []acmtypes.CertificateSummary{
			{CertificateArn: awssdk.String(certARN)},
		},
		details: map[string]*acmtypes.CertificateDetail{
			certARN: {
				CertificateArn: awssdk.String(certARN),
				DomainName:     awssdk.String("example.com"),
				Status:         acmtypes.CertificateStatusIssued,
				Type:           acmtypes.CertificateTypeAmazonIssued,
				DomainValidationOptions: []acmtypes.DomainValidation{
					{
						DomainName:       awssdk.String("a.example.com"),
						ValidationMethod: acmtypes.ValidationMethodDns,
						ResourceRecord:   &acmtypes.ResourceRecord{Name: awssdk.String("_x.a.example.com.")},
					},
					{
						DomainName:       awssdk.String("b.example.com"),
						ValidationMethod: acmtypes.ValidationMethodDns,
						ResourceRecord:   &acmtypes.ResourceRecord{Name: awssdk.String("_y.b.example.com.")},
					},
				},
			},
		},
	}

	c := &acmCollector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var count int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeValidatedBy {
			count++
		}
	}
	assert.Equal(t, 1, count, "same-apex SANs should emit only one ValidatedBy edge")
}

func TestDomainApex(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"_abc.foo.example.com.", "example.com"},
		{"_abc.example.com", "example.com"},
		{"example.com", "example.com"},
		{"com", ""},
		{"", ""},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, domainApex(tc.input), "domainApex(%q)", tc.input)
	}
}
