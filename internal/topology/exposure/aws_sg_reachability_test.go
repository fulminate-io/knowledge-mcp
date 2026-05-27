// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// aws_sg_reachability_test.go holds the recipe-driven tests for the AWS
// SG reachability analyzer. Each test builds a minimal synthetic graph
// via the sgFixture helpers and asserts the expected findings. All tests
// validate through AWSSGReachabilityAnalyzer.Run — reachability assertions
// check the matrix emitter output directly.

// findFindingWithTitle returns the first finding whose Title contains
// the given substring, or nil if none match.
func findFindingWithTitle(findings []Finding, substr string) *Finding {
	for i := range findings {
		if strings.Contains(findings[i].Title, substr) {
			return &findings[i]
		}
	}
	return nil
}

// countFindingsWithSeverity returns the number of findings at a given
// severity level. Used as a quick "there's at least one critical"
// assertion without needing exact title matches.
func countFindingsWithSeverity(findings []Finding, sev Severity) int {
	n := 0
	for i := range findings {
		if findings[i].Severity == sev {
			n++
		}
	}
	return n
}

// matrixContainsTCP80 reports whether the matrix finding's entries contain the
// standard test tuple (i-src → i-dst, tcp/80). All reachability tests use the
// same fixture pair, so this is hardcoded for clarity.
func matrixContainsTCP80(t *testing.T, findings []Finding) bool {
	t.Helper()
	for i := range findings {
		if findings[i].Algorithm != "aws_sg_reachability_matrix" {
			continue
		}
		var entries []sgMatrixEntry
		require.NoError(t, json.Unmarshal([]byte(findings[i].Summary), &entries))
		for _, e := range entries {
			if e.Src == "i-src" && e.Dst == "i-dst" && e.Protocol == "tcp" && e.PortFrom == 80 {
				return true
			}
		}
		return false
	}
	t.Fatal("no aws_sg_reachability_matrix finding emitted")
	return false
}

// TestReachability_SSHToWorld — an EC2 instance with an SG rule
// 0.0.0.0/0:22 must surface a critical world-open finding on SSH.
func TestReachability_SSHToWorld(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-web", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 22, PortTo: 22, CIDR: "0.0.0.0/0"}},
		nil,
	)
	fx.AddInstance("i-web", "vpc-a", "subnet-a", []string{"sg-web"})

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "reachable from 0.0.0.0/0 on SSH")
	require.NotNil(t, f, "expected SSH world-open finding")
	require.Equal(t, SeverityCritical, f.Severity)
	require.Equal(t, "i-web", f.Evidence[0])
}

// TestReachability_StatefulEgress — egress default-allow works as
// expected: a SG with no egress rules still permits outbound traffic.
// Validates via the matrix emitter — i-src → i-dst on tcp/80 must appear.
func TestReachability_StatefulEgress(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	// sg-src has no egress rules: default-allow-all-egress applies.
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	// sg-dst has ingress from sg-src on TCP 80.
	fx.AddSG("sg-dst", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-a", "subnet-a", []string{"sg-dst"})

	findings := fx.runSGAnalyzer()
	require.True(t, matrixContainsTCP80(t, findings),
		"stateful egress default-allow should let sg-src reach sg-dst:80")
}

// TestReachability_WildcardProtocol — a rule with Protocol "" (all
// protocols) matches any probe protocol.
func TestReachability_WildcardProtocol(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-any", "vpc-a",
		[]sgRuleSpec{{Protocol: "", PortFrom: 0, PortTo: 0, CIDR: "0.0.0.0/0"}},
		nil,
	)
	fx.AddInstance("i-any", "vpc-a", "subnet-a", []string{"sg-any"})

	scoped := fx.reader()
	idx, err := buildSGReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.True(t, idx.worldReachableOn("i-any", "udp", 53),
		"wildcard protocol rule should match UDP probe")
	require.True(t, idx.worldReachableOn("i-any", "tcp", 22),
		"wildcard protocol rule should match TCP SSH probe")
}

// TestReachability_ChainedSGs — ALB SG → EC2 SG → RDS SG transitive chain
// surfaces a transitive chain finding.
func TestReachability_ChainedSGs(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-alb", "vpc-a", nil, nil)
	fx.AddSG("sg-ec2", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 443, PortTo: 443, PeerSG: "sg-alb"}},
		nil,
	)
	fx.AddSG("sg-rds", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 3306, PortTo: 3306, PeerSG: "sg-ec2"}},
		nil,
	)
	fx.AddResource("alb-1", "vpc-a", "subnet-a", "elbv2-load-balancer", []string{"sg-alb"})
	fx.AddInstance("i-app", "vpc-a", "subnet-a", []string{"sg-ec2"})
	fx.AddResource("rds-db", "vpc-a", "subnet-a", "rds-instance", []string{"sg-rds"})

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "Transitive SG chain")
	require.NotNil(t, f, "expected transitive chain finding")
	require.Equal(t, SeverityWarning, f.Severity)
	require.Contains(t, f.Evidence, "sg-alb")
	require.Contains(t, f.Evidence, "sg-ec2")
	require.Contains(t, f.Evidence, "sg-rds")
}

// TestReachability_ALBPort443Normal — 0.0.0.0/0:443 on an ALB must NOT
// fire a critical finding (it's routine public web exposure).
func TestReachability_ALBPort443Normal(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-alb", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 443, PortTo: 443, CIDR: "0.0.0.0/0"}},
		nil,
	)
	fx.AddResource("alb-1", "vpc-a", "subnet-a", "elbv2-load-balancer", []string{"sg-alb"})

	findings := fx.runSGAnalyzer()
	// 443 is not in the privileged-port table, so no world-open finding
	// should fire. And no critical findings at all for this fixture.
	require.Zero(t, countFindingsWithSeverity(findings, SeverityCritical),
		"ALB:443 exposure should not produce a critical finding")
}

// TestReachability_RDSWorldExposed — 0.0.0.0/0:3306 on an RDS is
// critical.
func TestReachability_RDSWorldExposed(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-rds", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 3306, PortTo: 3306, CIDR: "0.0.0.0/0"}},
		nil,
	)
	fx.AddResource("rds-db", "vpc-a", "subnet-a", "rds-instance", []string{"sg-rds"})

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "reachable from 0.0.0.0/0 on MySQL")
	require.NotNil(t, f, "expected RDS MySQL world-open finding")
	require.Equal(t, SeverityCritical, f.Severity)
}

// TestReachability_EFSMountExposed — 0.0.0.0/0:2049 on EFS is critical.
func TestReachability_EFSMountExposed(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-efs", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 2049, PortTo: 2049, CIDR: "0.0.0.0/0"}},
		nil,
	)
	fx.AddResource("efs-data", "vpc-a", "subnet-a", "efs-file-system", []string{"sg-efs"})

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "reachable from 0.0.0.0/0 on NFS/EFS")
	require.NotNil(t, f, "expected EFS NFS world-open finding")
	require.Equal(t, SeverityCritical, f.Severity)
}

// TestReachability_Isolated — an instance with no edges surfaces the
// isolated info finding.
func TestReachability_Isolated(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-empty", "vpc-a", nil, nil)
	fx.AddInstance("i-isolated", "vpc-a", "subnet-a", []string{"sg-empty"})

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "is network-isolated")
	require.NotNil(t, f, "expected isolated info finding")
	require.Equal(t, SeverityInfo, f.Severity)
}

// TestReachability_OverCap_EmitsNotice — exceeding the hard cap emits a
// single skipped notice finding.
func TestReachability_OverCap_EmitsNotice(t *testing.T) {
	orig := sgReachabilityResourceCap
	sgReachabilityResourceCap = 2
	t.Cleanup(func() { sgReachabilityResourceCap = orig })

	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-x", "vpc-a", nil, nil)
	for _, id := range []string{"i-1", "i-2", "i-3"} {
		fx.AddInstance(id, "vpc-a", "subnet-a", []string{"sg-x"})
	}

	findings := fx.runSGAnalyzer()
	f := findFindingWithTitle(findings, "SG reachability skipped")
	require.NotNil(t, f, "expected skipped notice finding")
	require.Equal(t, SeverityNotice, f.Severity)
}

// TestNACL_AllowFromSG_DenyFromNACL — SG allows but NACL blocks →
// unreachable. The fixture adds a single egress NACL rule on a port that
// does not cover the query, so the NACL layer rejects. Validates via
// the matrix emitter — i-src → i-dst on tcp/80 must NOT appear.
func TestNACL_AllowFromSG_DenyFromNACL(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-src", "vpc-a")
	fx.AddSubnet("subnet-dst", "vpc-a")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-src", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-a", "subnet-dst", []string{"sg-dst"})
	// NACL on src subnet only permits port 443 egress — blocks port 80.
	fx.AddNACL("subnet-src", []naclRuleSpec{
		{Protocol: "tcp", PortFrom: 443, PortTo: 443, CIDR: "0.0.0.0/0", Egress: true, RuleNumber: 100},
	})

	findings := fx.runSGAnalyzer()
	require.False(t, matrixContainsTCP80(t, findings),
		"NACL egress denial should block reachability")
}

// TestNACL_BothAllow — NACL permits the query port on both ends and the
// SG layer already allows. Validates via the matrix emitter.
func TestNACL_BothAllow(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-src", "vpc-a")
	fx.AddSubnet("subnet-dst", "vpc-a")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-src", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-a", "subnet-dst", []string{"sg-dst"})
	fx.AddNACL("subnet-src", []naclRuleSpec{
		{Protocol: "tcp", PortFrom: 80, PortTo: 80, CIDR: "0.0.0.0/0", Egress: true, RuleNumber: 100},
	})
	fx.AddNACL("subnet-dst", []naclRuleSpec{
		{Protocol: "tcp", PortFrom: 80, PortTo: 80, CIDR: "0.0.0.0/0", RuleNumber: 100},
	})

	findings := fx.runSGAnalyzer()
	require.True(t, matrixContainsTCP80(t, findings),
		"both NACL layers allow + SG allows → reachable")
}

// TestCrossVPC_PeeringWithRoute — peered VPCs with SG reference →
// reachable via the peering edge. Validates via the matrix emitter.
func TestCrossVPC_PeeringWithRoute(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddVPC("vpc-b")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSubnet("subnet-b", "vpc-b")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-b",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-b", "subnet-b", []string{"sg-dst"})
	fx.AddPeering("vpc-a", "vpc-b")

	findings := fx.runSGAnalyzer()
	require.True(t, matrixContainsTCP80(t, findings),
		"peered VPCs with SG reference → reachable")
}

// TestCrossVPC_Unpeered — no peering edge means the analyzer rejects
// cross-VPC reachability regardless of SG rules. Validates via the
// matrix emitter — i-src → i-dst on tcp/80 must NOT appear.
func TestCrossVPC_Unpeered(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddVPC("vpc-b")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSubnet("subnet-b", "vpc-b")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-b",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-b", "subnet-b", []string{"sg-dst"})

	findings := fx.runSGAnalyzer()
	require.False(t, matrixContainsTCP80(t, findings),
		"unpeered VPCs → unreachable")
}

// TestReachabilityMatrix_EmittedWithEntries — the matrix finding exists
// with a non-empty entry count.
// Matrix-finding integration tests moved to
// aws_sg_reachability_matrix_integration_test.go to keep this file under
// the 500-line hard cap.
