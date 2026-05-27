// SPDX-License-Identifier: Apache-2.0

// monitoring_coverage.go implements MonitoringCoverageAnalyzer — a
// rule-table topology analyzer that identifies cloud resources lacking
// monitoring coverage. It checks whether monitorable resource types
// have incoming MONITORS edges (from CloudWatch alarms or equivalent
// monitoring resources) and reports gaps.
//
// MONITORABLE TYPES — a package-level set defines which resource_type
// values are expected to have monitoring. The set is intentionally
// conservative: only resource types where missing monitoring is a
// genuine operational risk are included.
//
// SEVERITY — production-critical types (rds-instance,
// elbv2-loadbalancer) are Warning; others are Notice. Summary findings
// that aggregate per-type coverage are Info.
//
// FINDINGS — two categories:
//  1. Per-resource findings for each unmonitored resource (actionable).
//  2. Per-type summary findings showing the coverage percentage
//     (situational awareness).
//
// DATA ACCESS — one foundation.FetchNodesByType(NodeCloudResource) browse
// supplies the candidate nodes; one bulk foundation.FetchEdges over the
// monitorable node set (filtered to MONITORS) supplies the incoming-edge
// presence the coverage check reads in-memory. No per-node edge fetch.
package cloud

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// monitorableTypes maps resource_type values to whether they are
// considered production-critical. Critical types get Warning severity
// when unmonitored; non-critical get Notice.
var monitorableTypes = map[string]bool{
	"ec2-instance":       false,
	"rds-instance":       true, // production-critical
	"lambda-function":    false,
	"elbv2-loadbalancer": true, // production-critical
	"sqs-queue":          false,
	"dynamodb-table":     false,
	"nat-gateway":        false,
}

// MonitoringCoverageAnalyzer identifies cloud resources that lack
// monitoring. Zero-value usable; self-registers via init().
type MonitoringCoverageAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (MonitoringCoverageAnalyzer) Name() string { return "monitoring_coverage" }

// Run executes the monitoring coverage check against a cloud graph.
func (a MonitoringCoverageAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/monitoring_coverage: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/monitoring_coverage: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/monitoring_coverage: req.Caller must not be nil")
	}

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeCloudResource)
	if err != nil {
		return nil, fmt.Errorf("topology/monitoring_coverage: fetch nodes cloud/%s: %w", req.Name, err)
	}

	stats, err := collectMonitoringStats(ctx, req, nodes, req.Subset)
	if err != nil {
		return nil, err
	}
	if len(stats) == 0 {
		return nil, nil
	}

	findings := buildMonitoringFindings(stats)
	sortMonitoringFindings(findings)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// monitoringTypeStats aggregates monitoring coverage for one
// resource_type.
type monitoringTypeStats struct {
	resourceType string
	critical     bool
	total        int
	unmonitored  []*knowledgev1.Node
}

// collectMonitoringStats groups monitorable resources by type and checks
// each for incoming MONITORS edges, reading the presence from an in-memory
// index built from ONE bulk edge fetch over the monitorable node set.
func collectMonitoringStats(
	ctx context.Context,
	req foundation.Request,
	nodes []*knowledgev1.Node,
	subset func(*knowledgev1.Node) bool,
) (map[string]*monitoringTypeStats, error) {
	// First pass: filter to monitorable nodes (honoring subset) so the bulk
	// edge fetch is scoped to exactly the IDs the coverage check inspects.
	var monitorable []*knowledgev1.Node
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if subset != nil && !subset(n) {
			continue
		}
		if _, ok := monitorableTypes[metaValue(n, "resource_type")]; !ok {
			continue
		}
		monitorable = append(monitorable, n)
	}
	if len(monitorable) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(monitorable))
	for _, n := range monitorable {
		ids = append(ids, n.Id)
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, []kgtypes.EdgeType{kgtypes.EdgeMonitors})
	if err != nil {
		return nil, fmt.Errorf("topology/monitoring_coverage: fetch edges: %w", err)
	}
	idx := newEdgeIndex(edges)

	stats := make(map[string]*monitoringTypeStats)
	for _, n := range monitorable {
		rt := metaValue(n, "resource_type")
		critical := monitorableTypes[rt]
		s, exists := stats[rt]
		if !exists {
			s = &monitoringTypeStats{resourceType: rt, critical: critical}
			stats[rt] = s
		}
		s.total++
		if !idx.hasIncoming(n.Id, kgtypes.EdgeMonitors) {
			s.unmonitored = append(s.unmonitored, n)
		}
	}
	return stats, nil
}

// buildMonitoringFindings generates per-resource and per-type summary
// findings from the collected monitoring stats.
func buildMonitoringFindings(stats map[string]*monitoringTypeStats) []foundation.Finding {
	var findings []foundation.Finding
	for _, s := range stats {
		// Per-resource findings for unmonitored resources.
		for _, n := range s.unmonitored {
			findings = append(findings, buildUnmonitoredFinding(n, s))
		}
		// Per-type summary finding.
		if s.total > 0 {
			findings = append(findings, buildCoverageSummaryFinding(s))
		}
	}
	return findings
}

// buildUnmonitoredFinding constructs a Finding for a single
// unmonitored resource.
func buildUnmonitoredFinding(n *knowledgev1.Node, s *monitoringTypeStats) foundation.Finding {
	severity := foundation.SeverityNotice
	if s.critical {
		severity = foundation.SeverityWarning
	}
	display := displayName(n)
	return foundation.Finding{
		Algorithm: "monitoring_coverage",
		Severity:  severity,
		Title:     fmt.Sprintf("Unmonitored %s: %s", s.resourceType, display),
		Summary: fmt.Sprintf(
			"Resource %s (%s) has no incoming MONITORS edges. "+
				"Consider adding CloudWatch alarms or equivalent monitoring.",
			display, s.resourceType,
		),
		Evidence: []string{n.Id},
		Metadata: map[string]string{
			"resource_type": s.resourceType,
		},
	}
}

// buildCoverageSummaryFinding constructs a summary Finding for one
// resource_type showing the monitoring coverage percentage.
func buildCoverageSummaryFinding(s *monitoringTypeStats) foundation.Finding {
	unmonCount := len(s.unmonitored)
	monitored := s.total - unmonCount
	coveragePct := 100.0
	if s.total > 0 {
		coveragePct = float64(monitored) / float64(s.total) * 100
	}
	return foundation.Finding{
		Algorithm: "monitoring_coverage",
		Severity:  foundation.SeverityInfo,
		Title:     fmt.Sprintf("Monitoring coverage: %s", s.resourceType),
		Summary: fmt.Sprintf(
			"%d of %d %s resources are monitored (%.0f%% coverage).",
			monitored, s.total, s.resourceType, coveragePct,
		),
		Evidence: []string{},
		Metrics: map[string]float64{
			"total_monitorable": float64(s.total),
			"unmonitored_count": float64(unmonCount),
			"coverage_pct":      coveragePct,
		},
		Metadata: map[string]string{
			"resource_type": s.resourceType,
		},
	}
}

// sortMonitoringFindings orders findings deterministically: per-resource
// findings before summaries, highest severity first, then by primary
// evidence ID for stable tie-breaking.
func sortMonitoringFindings(findings []foundation.Finding) {
	sevOrder := map[foundation.Severity]int{
		foundation.SeverityWarning: 0,
		foundation.SeverityNotice:  1,
		foundation.SeverityInfo:    2,
	}
	sort.SliceStable(findings, func(i, j int) bool {
		si := sevOrder[findings[i].Severity]
		sj := sevOrder[findings[j].Severity]
		if si != sj {
			return si < sj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}

// init self-registers MonitoringCoverageAnalyzer with the topology
// registry.
func init() {
	foundation.Register(MonitoringCoverageAnalyzer{})
}
