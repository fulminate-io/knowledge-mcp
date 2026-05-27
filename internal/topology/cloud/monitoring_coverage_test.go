// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// monitoring_coverage_test.go covers MonitoringCoverageAnalyzer: monitored
// resource not flagged, unmonitored resource flagged, non-monitorable
// ignored, coverage summary finding, and TopK capping.

const mcAccount = "monitoring-cov-test"

func runMonitoringCoverage(t *testing.T, fx *cloudFixture, topK int) []foundation.Finding {
	t.Helper()
	findings, err := MonitoringCoverageAnalyzer{}.Run(context.Background(), fx.cloudReq(mcAccount, topK))
	require.NoError(t, err)
	return findings
}

func TestMonitoringCoverageAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "monitoring_coverage", MonitoringCoverageAnalyzer{}.Name())
}

// TestMonitoringCoverage_MonitoredNotFlagged verifies an EC2 instance
// with a MONITORS edge does not produce an unmonitored finding.
func TestMonitoringCoverage_MonitoredNotFlagged(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(mcAccount, "ec2-monitored", "ec2-monitored", "ec2-instance", nil)
	fx.AddCloudResource(mcAccount, "cw-alarm-1", "cw-alarm-1", "cloudwatch-alarm", nil)
	fx.AddEdge(mcAccount, "cw-alarm-1", "ec2-monitored", kgtypes.EdgeMonitors)

	findings := runMonitoringCoverage(t, fx, 0)

	for _, f := range findings {
		if f.Severity != foundation.SeverityInfo {
			assert.NotContains(t, f.Evidence, "ec2-monitored",
				"monitored resource should not appear in unmonitored findings")
		}
	}
	var summary *foundation.Finding
	for i := range findings {
		if findings[i].Severity == foundation.SeverityInfo && findings[i].Metadata["resource_type"] == "ec2-instance" {
			summary = &findings[i]
			break
		}
	}
	require.NotNil(t, summary, "expected coverage summary for ec2-instance")
	assert.InDelta(t, 100.0, summary.Metrics["coverage_pct"], 0.01)
}

// TestMonitoringCoverage_UnmonitoredFlagged verifies an EC2 instance
// WITHOUT a MONITORS edge IS flagged.
func TestMonitoringCoverage_UnmonitoredFlagged(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(mcAccount, "ec2-no-mon", "ec2-no-mon", "ec2-instance", nil)

	findings := runMonitoringCoverage(t, fx, 0)
	require.NotEmpty(t, findings)

	var unmonFinding *foundation.Finding
	for i := range findings {
		if findings[i].Severity == foundation.SeverityNotice {
			if slices.Contains(findings[i].Evidence, "ec2-no-mon") {
				unmonFinding = &findings[i]
			}
		}
	}
	require.NotNil(t, unmonFinding, "expected unmonitored finding for ec2-no-mon")
	assert.Equal(t, foundation.SeverityNotice, unmonFinding.Severity)
	assert.Equal(t, "monitoring_coverage", unmonFinding.Algorithm)
}

// TestMonitoringCoverage_CriticalType verifies a production-critical type
// (rds-instance) gets Warning severity when unmonitored.
func TestMonitoringCoverage_CriticalType(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(mcAccount, "rds-no-mon", "rds-no-mon", "rds-instance", nil)

	findings := runMonitoringCoverage(t, fx, 0)
	require.NotEmpty(t, findings)

	var rdsUnmon *foundation.Finding
	for i := range findings {
		if findings[i].Severity == foundation.SeverityWarning {
			rdsUnmon = &findings[i]
			break
		}
	}
	require.NotNil(t, rdsUnmon, "expected Warning-severity finding for unmonitored rds-instance")
	assert.Contains(t, rdsUnmon.Evidence, "rds-no-mon")
}

// TestMonitoringCoverage_NonMonitorableIgnored verifies a resource_type
// not in the monitorable set is silently ignored.
func TestMonitoringCoverage_NonMonitorableIgnored(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(mcAccount, "s3-bucket-1", "s3-bucket-1", "s3-bucket", nil)

	findings := runMonitoringCoverage(t, fx, 0)
	assert.Nil(t, findings, "s3-bucket is not monitorable; no findings expected")
}

// TestMonitoringCoverage_SummaryPercentage verifies the summary finding
// reports the correct coverage percentage with mixed monitored/unmonitored.
func TestMonitoringCoverage_SummaryPercentage(t *testing.T) {
	fx := newCloudFixture(t)
	// 2 ec2 instances: 1 monitored, 1 not.
	fx.AddCloudResource(mcAccount, "ec2-a", "ec2-a", "ec2-instance", nil)
	fx.AddCloudResource(mcAccount, "ec2-b", "ec2-b", "ec2-instance", nil)
	fx.AddCloudResource(mcAccount, "cw-a", "cw-a", "cloudwatch-alarm", nil)
	fx.AddEdge(mcAccount, "cw-a", "ec2-a", kgtypes.EdgeMonitors)

	findings := runMonitoringCoverage(t, fx, 0)
	require.NotEmpty(t, findings)

	var summary *foundation.Finding
	for i := range findings {
		if findings[i].Severity == foundation.SeverityInfo {
			summary = &findings[i]
			break
		}
	}
	require.NotNil(t, summary)
	assert.InDelta(t, 50.0, summary.Metrics["coverage_pct"], 0.01)
	assert.InDelta(t, 2, summary.Metrics["total_monitorable"], 0.01)
	assert.InDelta(t, 1, summary.Metrics["unmonitored_count"], 0.01)
}

// TestMonitoringCoverage_EmptyGraph verifies no monitorable resources
// means nil findings.
func TestMonitoringCoverage_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	fx.account(mcAccount)
	findings := runMonitoringCoverage(t, fx, 0)
	assert.Nil(t, findings)
}

// TestMonitoringCoverage_NonCloudGraph verifies error on wrong graph type.
func TestMonitoringCoverage_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	_, err := MonitoringCoverageAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires GraphCloud")
}

// TestMonitoringCoverage_TopK verifies the TopK cap.
func TestMonitoringCoverage_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	// Create 5 unmonitored ec2 instances to generate many findings.
	for i := range 5 {
		id := fmt.Sprintf("ec2-topk-%d", i)
		fx.AddCloudResource(mcAccount, id, id, "ec2-instance", nil)
	}

	findings := runMonitoringCoverage(t, fx, 2)
	assert.Len(t, findings, 2)
}
