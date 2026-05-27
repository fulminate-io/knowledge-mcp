// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// aws_sg_reachability_matrix_integration_test.go holds the end-to-end
// integration tests for the reachability matrix finding. Unit tests for
// the cap mechanics and wire format live in aws_sg_reachability_matrix_test.go;
// these tests run the full AWSSGReachabilityAnalyzer.Run pipeline against
// sgFixture graphs and assert the emitted finding's shape + correctness.
// Extracted from aws_sg_reachability_test.go to keep that file under
// the 500-line hard cap.

// TestReachabilityMatrix_EmittedWithEntries — matrix finding exists with
// at least one entry for a minimal ingress-rule fixture.
func TestReachabilityMatrix_EmittedWithEntries(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-a", "subnet-a", []string{"sg-dst"})

	findings := fx.runSGAnalyzer()
	var matrix *Finding
	for i := range findings {
		if findings[i].Algorithm == "aws_sg_reachability_matrix" {
			matrix = &findings[i]
			break
		}
	}
	require.NotNil(t, matrix, "expected aws_sg_reachability_matrix finding")
	require.Equal(t, "aws_sg_reachability_matrix", matrix.Title)
	require.Greater(t, matrix.Metrics["entry_count"], 0.0,
		"matrix should carry at least one entry")
	// Round-trip the JSON payload to ensure the matrix is decodable.
	var entries []sgMatrixEntry
	require.NoError(t, json.Unmarshal([]byte(matrix.Summary), &entries))
	require.NotEmpty(t, entries)
}

// TestReachabilityMatrix_CappedAt10000 — exceeding the matrix cap adds
// the truncation sentinel entry.
func TestReachabilityMatrix_CappedAt10000(t *testing.T) {
	// Build a cap exceeding small enough to be quick but still to exceed
	// matrixMaxEntries. Temporarily lower matrixMaxEntries via a local
	// test hook is cleaner than allocating 10000 instances.
	origCap := matrixMaxEntriesForTesting
	matrixMaxEntriesForTesting = 4
	t.Cleanup(func() { matrixMaxEntriesForTesting = origCap })

	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-web", "vpc-a",
		[]sgRuleSpec{{Protocol: "", PortFrom: 0, PortTo: 0, CIDR: "0.0.0.0/0"}},
		nil,
	)
	for _, id := range []string{"i-1", "i-2", "i-3", "i-4", "i-5"} {
		fx.AddInstance(id, "vpc-a", "subnet-a", []string{"sg-web"})
	}

	findings := fx.runSGAnalyzer()
	var matrix *Finding
	for i := range findings {
		if findings[i].Algorithm == "aws_sg_reachability_matrix" {
			matrix = &findings[i]
			break
		}
	}
	require.NotNil(t, matrix)
	// entry_count reflects the TOTAL before truncation, not the emitted
	// length, so it should be > matrixMaxEntriesForTesting.
	require.Greater(t, matrix.Metrics["entry_count"], float64(matrixMaxEntriesForTesting))
	require.Equal(t, "true", matrix.Metadata["capped_by_limit"])
}

// TestReachabilityMatrix_MatchesLayerFilters — regression guard for the
// ingress-driven matrix enumeration. Builds a multi-VPC fixture with
// peering, SG-to-SG chains, world-open CIDR rules, and plain SG-peer
// rules, then cross-checks the emitted matrix against the per-layer
// reachability filter chain over every (src, dst, probe) triple. Every
// tuple the matrix emits must pass all layer filters, and every
// filter-passing tuple must appear in the matrix. This protects the
// enumeration rewrite from silently dropping or inventing tuples on
// graphs with mixed rule shapes.
func TestReachabilityMatrix_MatchesLayerFilters(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddVPC("vpc-b")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSubnet("subnet-b", "vpc-b")
	fx.AddPeering("vpc-a", "vpc-b")
	// sg-src: no ingress (acts purely as client).
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	// sg-web: tcp/80 from sg-src, tcp/22 from 0.0.0.0/0.
	fx.AddSG("sg-web", "vpc-a",
		[]sgRuleSpec{
			{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"},
			{Protocol: "tcp", PortFrom: 22, PortTo: 22, CIDR: "0.0.0.0/0"},
		},
		nil,
	)
	// sg-db (vpc-b): tcp/3306 from sg-src (cross-VPC via peering).
	fx.AddSG("sg-db", "vpc-b",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 3306, PortTo: 3306, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-web", "vpc-a", "subnet-a", []string{"sg-web"})
	fx.AddInstance("i-db", "vpc-b", "subnet-b", []string{"sg-db"})

	findings := fx.runSGAnalyzer()
	var matrix *Finding
	for i := range findings {
		if findings[i].Algorithm == "aws_sg_reachability_matrix" {
			matrix = &findings[i]
			break
		}
	}
	require.NotNil(t, matrix)
	var entries []sgMatrixEntry
	require.NoError(t, json.Unmarshal([]byte(matrix.Summary), &entries))

	// Build the authoritative reachable-set by applying the per-layer
	// filter chain (the same functions the matrix emitter uses) over the
	// entire (src, dst, probe) cross-product.
	idx, err := buildSGReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	ids := sortedResourceIDs(idx)
	probes := collectSGProbes(idx)
	type tuple struct {
		Src, Dst, Protocol string
		Port               int
	}
	want := map[tuple]struct{}{}
	for _, src := range ids {
		srcInfo := idx.resources[src]
		if srcInfo == nil {
			continue
		}
		for _, dst := range ids {
			if src == dst {
				continue
			}
			dstInfo := idx.resources[dst]
			if dstInfo == nil {
				continue
			}
			if !idx.crossVPCAllows(srcInfo, dstInfo) {
				continue
			}
			for _, p := range probes {
				if !egressSGAllows(srcInfo, dstInfo, p.Protocol, p.Port) {
					continue
				}
				if !ingressSGAllows(srcInfo, dstInfo, p.Protocol, p.Port) {
					continue
				}
				if !idx.naclLayerAllows(srcInfo, dstInfo, p.Protocol, p.Port) {
					continue
				}
				want[tuple{src, dst, p.Protocol, p.Port}] = struct{}{}
			}
		}
	}
	got := map[tuple]struct{}{}
	for _, e := range entries {
		if e.Src == "truncated" {
			continue
		}
		got[tuple{e.Src, e.Dst, e.Protocol, e.PortFrom}] = struct{}{}
	}
	require.Equal(t, want, got,
		"matrix tuples must match per-layer filter chain exactly")
}

// TestReachabilityMatrix_EmitFalse — emit_matrix=false skips matrix
// emission entirely.
func TestReachabilityMatrix_EmitFalse(t *testing.T) {
	fx := newSGFixture(t)
	fx.AddVPC("vpc-a")
	fx.AddSubnet("subnet-a", "vpc-a")
	fx.AddSG("sg-src", "vpc-a", nil, nil)
	fx.AddSG("sg-dst", "vpc-a",
		[]sgRuleSpec{{Protocol: "tcp", PortFrom: 80, PortTo: 80, PeerSG: "sg-src"}},
		nil,
	)
	fx.AddInstance("i-src", "vpc-a", "subnet-a", []string{"sg-src"})
	fx.AddInstance("i-dst", "vpc-a", "subnet-a", []string{"sg-dst"})

	req := Request{
		Caller: fx.fx,
		Graph:  kgtypes.GraphCloud,
		Name:   sgTestAccount,
		Extra:  map[string]string{"emit_matrix": "false"},
	}
	findings, err := AWSSGReachabilityAnalyzer{}.Run(newTestCtx(t), req)
	require.NoError(t, err)
	for i := range findings {
		require.NotEqual(t, "aws_sg_reachability_matrix", findings[i].Algorithm,
			"emit_matrix=false should suppress the matrix finding")
	}
}
