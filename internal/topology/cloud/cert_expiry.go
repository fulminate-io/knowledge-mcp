// SPDX-License-Identifier: Apache-2.0

// cert_expiry.go implements CertExpiryAnalyzer — a cloud topology analyzer
// that identifies ACM certificates approaching or past their expiry date.
//
// The analyzer iterates all acm-certificate nodes, parses the NotAfter field
// from each node's Content JSON (populated by the ACM collector), and emits
// findings at three severity tiers:
//
//   - Critical — certificate has already expired (NotAfter < now).
//   - Warning  — certificate expires within warning_days (default 30).
//   - Notice   — certificate expires within notice_days (default 90).
//   - Beyond notice_days — no finding emitted.
//
// Each finding includes a dependent count: the number of resources with
// USES_CERT edges pointing to the certificate. More dependents = higher
// operational impact when the certificate expires.
//
// CONFIGURATION via req.Extra:
//
//   - "warning_days" — override the 30-day Warning threshold.
//   - "notice_days"  — override the 90-day Notice threshold.
//
// AWS ACM only for v1.
//
// DATA ACCESS — one foundation.FetchNodesByType(NodeCloudResource) browse
// supplies the candidate nodes; one bulk foundation.FetchEdges over the
// certificate node set (filtered to USES_CERT) supplies the dependent counts
// in-memory. No per-cert edge fetch.
package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// timeNow is a seam for testing — tests replace it to control "now".
var timeNow = time.Now

// Default expiry thresholds in days.
const (
	defaultWarningDays = 30
	defaultNoticeDays  = 90
)

// CertExpiryAnalyzer identifies ACM certificates that are expired or
// approaching expiry. Zero-value usable; self-registers via init().
type CertExpiryAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (CertExpiryAnalyzer) Name() string { return "cert_expiry" }

// Run scopes to a single cloud account, walks every acm-certificate node,
// and emits findings for certificates that are expired or nearing expiry.
func (CertExpiryAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/cert_expiry: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/cert_expiry: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/cert_expiry: req.Caller must not be nil")
	}

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeCloudResource)
	if err != nil {
		return nil, fmt.Errorf("topology/cert_expiry: fetch nodes cloud/%s: %w", req.Name, err)
	}

	warningDays := resolveThreshold(req, "warning_days", defaultWarningDays)
	noticeDays := resolveThreshold(req, "notice_days", defaultNoticeDays)
	now := timeNow()

	// Filter to candidate certificate nodes (honoring subset) so the bulk
	// edge fetch is scoped to exactly the cert IDs whose dependents we count.
	var certs []*knowledgev1.Node
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/cert_expiry: %w", err)
		}
		if n == nil || metaValue(n, "resource_type") != "acm-certificate" {
			continue
		}
		if req.Subset != nil && !req.Subset(n) {
			continue
		}
		certs = append(certs, n)
	}
	if len(certs) == 0 {
		return nil, nil
	}

	idx, err := buildCertIndex(ctx, req, certs)
	if err != nil {
		return nil, err
	}

	var findings []foundation.Finding
	for _, n := range certs {
		if f, ok := evaluateCertExpiry(idx, n, now, warningDays, noticeDays); ok {
			findings = append(findings, f)
		}
	}

	sortCertFindings(findings)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// buildCertIndex fetches every USES_CERT edge incident to the certificate set
// in ONE bulk read and returns the in-memory index used to count dependents.
func buildCertIndex(ctx context.Context, req foundation.Request, certs []*knowledgev1.Node) (*edgeIndex, error) {
	ids := make([]string, 0, len(certs))
	for _, n := range certs {
		ids = append(ids, n.Id)
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, []kgtypes.EdgeType{kgtypes.EdgeUsesCert})
	if err != nil {
		return nil, fmt.Errorf("topology/cert_expiry: fetch edges: %w", err)
	}
	return newEdgeIndex(edges), nil
}

// certContent is the local struct used to parse the ACM certificate's
// Content JSON. Only the fields the analyzer cares about are declared;
// unknown keys are silently ignored by json.Unmarshal.
type certContent struct {
	DomainName string `json:"domain_name"`
	NotAfter   string `json:"not_after"`
}

// evaluateCertExpiry parses a single certificate node's content and returns
// a Finding if the certificate is expired or within the threshold windows.
// Returns (Finding{}, false) for certs outside all windows.
func evaluateCertExpiry(
	idx *edgeIndex,
	n *knowledgev1.Node,
	now time.Time,
	warningDays, noticeDays int,
) (foundation.Finding, bool) {
	if n.Content == "" {
		return foundation.Finding{}, false
	}
	var cc certContent
	if err := json.Unmarshal([]byte(n.Content), &cc); err != nil {
		return foundation.Finding{}, false // best-effort: skip malformed content
	}
	if cc.NotAfter == "" {
		return foundation.Finding{}, false
	}
	notAfter, err := time.Parse(time.RFC3339, cc.NotAfter)
	if err != nil {
		return foundation.Finding{}, false // best-effort: skip unparseable dates
	}

	daysUntil := notAfter.Sub(now).Hours() / 24
	severity := classifyCertSeverity(daysUntil, warningDays, noticeDays)
	if severity == "" {
		return foundation.Finding{}, false
	}

	dependents := idx.incomingCount(n.Id, kgtypes.EdgeUsesCert)

	domain := cc.DomainName
	if domain == "" {
		domain = displayName(n)
	}

	title := certTitle(severity, domain, daysUntil)
	summary := certSummary(domain, daysUntil, dependents)

	f := foundation.Finding{
		Algorithm: "cert_expiry",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  []string{n.Id},
		Metrics: map[string]float64{
			"days_until_expiry": math.Round(daysUntil*100) / 100,
			"dependents":        float64(dependents),
		},
		Metadata: map[string]string{
			"domain": domain,
		},
	}
	return f, true
}

// classifyCertSeverity maps days-until-expiry to a Severity. Returns ""
// when the certificate is outside all windows (no finding needed).
func classifyCertSeverity(daysUntil float64, warningDays, noticeDays int) foundation.Severity {
	switch {
	case daysUntil < 0:
		return foundation.SeverityCritical
	case daysUntil <= float64(warningDays):
		return foundation.SeverityWarning
	case daysUntil <= float64(noticeDays):
		return foundation.SeverityNotice
	default:
		return ""
	}
}

// certTitle builds a short headline for the finding.
func certTitle(_ foundation.Severity, domain string, daysUntil float64) string {
	if daysUntil < 0 {
		return fmt.Sprintf("Expired certificate: %s (%.0f days ago)", domain, -daysUntil)
	}
	return fmt.Sprintf("Certificate expiring: %s (%.0f days)", domain, daysUntil)
}

// certSummary builds the longer finding summary.
func certSummary(domain string, daysUntil float64, dependents int) string {
	depStr := "no resources"
	if dependents == 1 {
		depStr = "1 resource"
	} else if dependents > 1 {
		depStr = fmt.Sprintf("%d resources", dependents)
	}
	if daysUntil < 0 {
		return fmt.Sprintf(
			"ACM certificate %s expired %.0f days ago. %s depend on this certificate via USES_CERT.",
			domain, -daysUntil, depStr,
		)
	}
	return fmt.Sprintf(
		"ACM certificate %s expires in %.0f days. %s depend on this certificate via USES_CERT.",
		domain, daysUntil, depStr,
	)
}

// resolveThreshold reads an integer day threshold from req.Extra. Returns
// def when the key is missing or invalid.
func resolveThreshold(req foundation.Request, key string, def int) int {
	v := foundation.ExtraFloat(req, key, float64(def), func(f float64) bool {
		return f >= 1 && f <= 3650
	})
	return int(v)
}

// sortCertFindings orders findings: most urgent first (lowest days_until_expiry),
// then by dependents descending, then by primary evidence ID for stability.
func sortCertFindings(findings []foundation.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		di := findings[i].Metrics["days_until_expiry"]
		dj := findings[j].Metrics["days_until_expiry"]
		if di != dj {
			return di < dj
		}
		ci := findings[i].Metrics["dependents"]
		cj := findings[j].Metrics["dependents"]
		if ci != cj {
			return ci > cj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}

func init() {
	foundation.Register(CertExpiryAnalyzer{})
}
