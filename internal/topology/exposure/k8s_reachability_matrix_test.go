// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// k8s_reachability_matrix_test.go covers the Phase 4 Step 2 matrix emitter:
// the single "reachability_matrix" raw-data finding downstream analyzers
// consume via type+name lookup.

// TestMatrixFinding_Exists asserts that the analyzer emits exactly one
// matrix finding with the well-known algorithm and title on a normal run.
func TestMatrixFinding_Exists(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, nil)

	var matrices []Finding
	for _, f := range findings {
		if f.Algorithm == "k8s_reachability_matrix" {
			matrices = append(matrices, f)
		}
	}
	require.Len(t, matrices, 1, "exactly one matrix finding must be emitted on the normal path")
	assert.Equal(t, "reachability_matrix", matrices[0].Title)
}

// TestMatrixFinding_Shape asserts the JSON-serialized matrix in the
// Summary field deserializes into the expected shape and contains at least
// the pod pairs implied by the fixture.
func TestMatrixFinding_Shape(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, nil)

	var matrix Finding
	for _, f := range findings {
		if f.Algorithm == "k8s_reachability_matrix" {
			matrix = f
			break
		}
	}
	require.NotEmpty(t, matrix.Summary)

	var entries []matrixEntry
	require.NoError(t, json.Unmarshal([]byte(matrix.Summary), &entries))
	require.NotEmpty(t, entries, "matrix must contain at least one entry")

	// Every entry must name known pods (src, dst) and have Allowed set.
	seenWeb, seenAPI := false, false
	for _, e := range entries {
		if e.Note != "" {
			continue
		}
		if e.Src == "default/Pod/web" || e.Dst == "default/Pod/web" {
			seenWeb = true
		}
		if e.Src == "default/Pod/api" || e.Dst == "default/Pod/api" {
			seenAPI = true
		}
	}
	assert.True(t, seenWeb && seenAPI, "matrix must reference both fixture pods")
}

// TestMatrixFinding_Metadata asserts cluster, pod_count, and entry_count
// are present in the matrix finding's Metadata map.
func TestMatrixFinding_Metadata(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, nil)

	var matrix Finding
	for _, f := range findings {
		if f.Algorithm == "k8s_reachability_matrix" {
			matrix = f
			break
		}
	}
	require.NotNil(t, matrix.Metadata)
	assert.Equal(t, k8sReachabilityAcct, matrix.Metadata["cluster"])
	assert.NotEmpty(t, matrix.Metadata["pod_count"])
	assert.NotEmpty(t, matrix.Metadata["entry_count"])
}

// TestMatrixFinding_CapAndTruncation feeds a synthetic index whose total
// entry count exceeds matrixMaxEntries and asserts the returned matrix
// contains exactly matrixMaxEntries real entries followed by one
// truncation sentinel.
func TestMatrixFinding_CapAndTruncation(t *testing.T) {
	// Construct a tiny ad-hoc index with enough pods and probes that
	// len(pods)*(len(pods)-1)*len(probes) >> matrixMaxEntries. 200 pods
	// with 1 probe → 200*199 = 39800 entries, well over the 10000 cap.
	idx := &reachabilityIndex{
		pods: make(map[string]*podInfo, 200),
	}
	for i := range 200 {
		id := "default/Pod/p" + padInt(i)
		idx.pods[id] = &podInfo{
			ID:                 id,
			Namespace:          "default",
			Labels:             map[string]string{},
			AllowedIngressFrom: map[string][]portRange{},
			AllowedEgressTo:    map[string][]portRange{},
		}
	}
	req := Request{Name: "cluster-big"}
	matrix, ok := emitReachabilityMatrix(req, idx, []portProbe{{}})
	require.True(t, ok)

	var entries []matrixEntry
	require.NoError(t, json.Unmarshal([]byte(matrix.Summary), &entries))

	// The capped slice must contain exactly matrixMaxEntries real entries
	// plus one trailing truncation sentinel.
	require.Len(t, entries, matrixMaxEntries+1,
		"matrix must contain matrixMaxEntries real entries plus one sentinel")
	last := entries[len(entries)-1]
	assert.Equal(t, "truncated", last.Src)
	assert.Contains(t, last.Note, "truncated")
}

// padInt pads an int into a width-4 decimal string so pod IDs sort
// lexicographically in step with numeric order. Keeps the matrix tests
// deterministic regardless of iteration order elsewhere.
func padInt(i int) string {
	var sb strings.Builder
	sb.Grow(4)
	for _, d := range []int{1000, 100, 10, 1} {
		sb.WriteByte('0' + byte((i/d)%10))
	}
	return sb.String()
}

// TestMatrixFinding_SkippedPathSkipsMatrix confirms the matrix emitter is
// bypassed when the index is the skipped sentinel — only the
// reachability_skipped notice is emitted. Uses withPodCap to exercise the
// sentinel path with a tiny fixture.
func TestMatrixFinding_SkippedPathSkipsMatrix(t *testing.T) {
	withPodCap(t, 10)
	fx := newCloudFixture(t)
	for i := range reachabilityPodCap + 1 {
		addPod(fx, "default/Pod/p"+padInt(i), "default")
	}
	findings := runClassify(t, fx, nil)

	for _, f := range findings {
		assert.NotEqual(t, "k8s_reachability_matrix", f.Algorithm,
			"matrix emitter must be skipped on the sentinel path")
	}
}

// TestMatrixFinding_EmitMatrixFalseOptsOut verifies that setting
// Extra["emit_matrix"] = "false" suppresses the matrix finding on a normal
// run without affecting the other classifier outputs. Users running the
// analyzer on very large clusters where even the truncated matrix is
// wasteful can opt out via this flag.
func TestMatrixFinding_EmitMatrixFalseOptsOut(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, map[string]string{"emit_matrix": "false"})

	for _, f := range findings {
		assert.NotEqual(t, "k8s_reachability_matrix", f.Algorithm,
			"emit_matrix=false must suppress the matrix finding")
	}
	// The asymmetric fixture still emits at least one non-matrix finding
	// (the asymmetric pair + per-probe partials), so the classifier
	// definitely ran.
	require.NotEmpty(t, findings, "non-matrix findings must still be emitted")
}

// TestMatrixFinding_DownstreamQuery pins the downstream-consumer contract of
// the matrix finding the analyzer produces: a downstream consumer locates it
// by the well-known Title ("reachability_matrix") and deserializes the matrix
// payload out of the finding's Summary. The persistence round-trip
// (EmitFindingsForGraph → store query) is dispatcher-side scaffolding and no
// longer lives in the client analyzer package; the analyzer's own contract is
// the finding SHAPE asserted here, which is what the dispatcher then persists.
func TestMatrixFinding_DownstreamQuery(t *testing.T) {
	fx := buildAsymmetricFixture(t)
	findings := runClassify(t, fx, nil)

	var matrix Finding
	for _, f := range findings {
		if f.Algorithm == "k8s_reachability_matrix" {
			matrix = f
			break
		}
	}
	// Well-known Title is the key the dispatcher writes into the persisted
	// node's SymbolName column for downstream lookup.
	require.Equal(t, "reachability_matrix", matrix.Title,
		"downstream must locate the matrix finding by its well-known Title")

	// The matrix payload rides in Summary (the dispatcher copies it into the
	// node Description on persist). It must deserialize into []matrixEntry.
	var entries []matrixEntry
	require.NoError(t, json.Unmarshal([]byte(matrix.Summary), &entries))
	require.NotEmpty(t, entries)
}
