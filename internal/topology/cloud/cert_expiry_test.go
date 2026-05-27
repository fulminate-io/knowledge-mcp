// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// cert_expiry_test.go covers CertExpiryAnalyzer: expired certs (Critical),
// expiring-soon certs (Warning/Notice), safe certs (no finding), and
// USES_CERT dependent count.

const ceAccount = "cert-expiry-test"

// addCert creates an acm-certificate node with the given NotAfter date,
// carrying the certificate JSON in Content (which the analyzer parses).
func addCert(t *testing.T, fx *cloudFixture, id, domain string, notAfter time.Time) {
	t.Helper()
	content, err := json.Marshal(map[string]string{
		"domain_name": domain,
		"not_after":   notAfter.Format(time.RFC3339),
	})
	require.NoError(t, err)
	fx.AddCloudResourceWithContent(ceAccount, id, id, "acm-certificate", string(content), nil)
}

func runCertExpiry(t *testing.T, fx *cloudFixture, topK int) []foundation.Finding { //nolint:unparam // topK=0 is the only test case for now
	t.Helper()
	findings, err := CertExpiryAnalyzer{}.Run(context.Background(), fx.cloudReq(ceAccount, topK))
	require.NoError(t, err)
	return findings
}

func TestCertExpiryAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "cert_expiry", CertExpiryAnalyzer{}.Name())
}

// TestCertExpiry_Expired verifies a certificate past its NotAfter date
// produces a Critical severity finding.
func TestCertExpiry_Expired(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })

	// Expired 10 days ago.
	addCert(t, fx, "cert-expired", "expired.example.com", now.AddDate(0, 0, -10))

	findings := runCertExpiry(t, fx, 0)
	require.Len(t, findings, 1)
	assert.Equal(t, foundation.SeverityCritical, findings[0].Severity)
	assert.Equal(t, "cert_expiry", findings[0].Algorithm)
	assert.Contains(t, findings[0].Title, "Expired")
	assert.Contains(t, findings[0].Evidence, "cert-expired")
	assert.Negative(t, findings[0].Metrics["days_until_expiry"],
		"days_until_expiry should be negative for expired cert")
}

// TestCertExpiry_WarningWindow verifies a certificate expiring within
// 30 days produces a Warning.
func TestCertExpiry_WarningWindow(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })

	// Expires in 15 days.
	addCert(t, fx, "cert-warning", "warning.example.com", now.AddDate(0, 0, 15))

	findings := runCertExpiry(t, fx, 0)
	require.Len(t, findings, 1)
	assert.Equal(t, foundation.SeverityWarning, findings[0].Severity)
	assert.Contains(t, findings[0].Title, "expiring")
}

// TestCertExpiry_NoticeWindow verifies a certificate expiring within
// 90 days (but > 30 days) produces a Notice.
func TestCertExpiry_NoticeWindow(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })

	// Expires in 60 days.
	addCert(t, fx, "cert-notice", "notice.example.com", now.AddDate(0, 0, 60))

	findings := runCertExpiry(t, fx, 0)
	require.Len(t, findings, 1)
	assert.Equal(t, foundation.SeverityNotice, findings[0].Severity)
}

// TestCertExpiry_SafeCert verifies a certificate with >90 days remaining
// produces no finding.
func TestCertExpiry_SafeCert(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })

	// Expires in 180 days.
	addCert(t, fx, "cert-safe", "safe.example.com", now.AddDate(0, 0, 180))

	findings := runCertExpiry(t, fx, 0)
	assert.Nil(t, findings, "cert with >90 days remaining should produce no finding")
}

// TestCertExpiry_Dependents verifies the dependents metric counts
// resources with USES_CERT edges pointing to the certificate.
func TestCertExpiry_Dependents(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })

	addCert(t, fx, "cert-deps", "deps.example.com", now.AddDate(0, 0, 15))
	fx.AddCloudResource(ceAccount, "alb-1", "alb-1", "elbv2-loadbalancer", nil)
	fx.AddCloudResource(ceAccount, "alb-2", "alb-2", "elbv2-loadbalancer", nil)
	fx.AddEdge(ceAccount, "alb-1", "cert-deps", kgtypes.EdgeUsesCert)
	fx.AddEdge(ceAccount, "alb-2", "cert-deps", kgtypes.EdgeUsesCert)

	findings := runCertExpiry(t, fx, 0)
	require.Len(t, findings, 1)
	assert.InDelta(t, 2, findings[0].Metrics["dependents"], 0.01)
	assert.Contains(t, findings[0].Summary, "2 resources")
}

// TestCertExpiry_NonCloudGraph verifies error on wrong graph type.
func TestCertExpiry_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	_, err := CertExpiryAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires GraphCloud")
}

// TestCertExpiry_EmptyGraph verifies no certs means nil findings.
func TestCertExpiry_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	now := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = orig })
	fx.account(ceAccount)

	findings := runCertExpiry(t, fx, 0)
	assert.Nil(t, findings)
}
