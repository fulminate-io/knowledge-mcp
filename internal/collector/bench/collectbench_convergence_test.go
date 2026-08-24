// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// collectbench_convergence_test.go — RUN 4's equivalence proof, the AFTER-full
// floor, and the manifest capture both depend on.

// benchFullPathGraph is the landing surface for the NON-DIFF arm.
//
// IT IS A SECOND GRAPH, NOT A SECOND ACCOUNT, and that is a ruling rather than a
// shortcut. The bench server helper is a single-tenant clean room by design —
// it stamps one fixed account on every request with no per-request input — and
// its stated reason is measurement integrity: a fixed identity is what keeps the
// bench from producing statements labeled differently from the ones it is meant
// to measure. A second graph supplies every property this arm's reasoning needs:
// an EMPTY MANIFEST on its first collect, INDEPENDENT per-graph tables, and a
// manifest comparable over the wire.
const benchFullPathGraph = "collectbench-fullpath"

// runFullFacts/runFullSample name the AFTER-full record — a FIRST collect into a
// FRESH graph, which is what makes the floor band's lower bound meaningful.
const (
	runFullFacts  = "runfull.json"
	runFullSample = "runfull.psql.json"
)

// THE ANCHOR IS THIS TREE'S OWN MEASURED FULL COLLECT, not a historical one.
//
// WHAT CHANGED AND WHY. These used to be BEFORE-main literals from the committed
// RUN-1 artifact (tree 1b8ee5d0, head 754d4d00), and the band asked whether the
// armed path still cost about what the pre-wave path cost. That comparison is no
// longer available: the census was re-anchored from a top-15-by-queryid sum to a
// complete per-tag one, so the two sides stopped being the same quantity and
// differencing them measured the instrument as much as the code. The anchor is
// now a SAME-TREE measurement and the band is a NOISE BUDGET around it.
//
// THEY REMAIN LITERALS RATHER THAN A RE-DERIVATION, for the original reason: the
// artifact holds several run sections, so any range-based re-derivation would
// silently blend runs into one anchor and loosen the band with every gate green.
//
// THE MULTIPLIERS BELOW ARE A NOISE BUDGET, NOT A DESIGN-DELTA MODEL. There is no
// modelled cost being allowed for — the anchor and the measurement are the same
// tree — so the slack exists only to absorb container run-to-run variation.
// THE TIME BOUND HAS ALREADY BEEN TIGHTENED ONCE ON A MEASURED SPREAD, and this
// is the record of it rather than a pending instruction. A second same-tree
// sample measured 6305 ms against the 6029 ms anchor — a 4.6% spread — so the
// server multiplier moved 1.20 -> 1.15, a ceiling of 6933 ms that the observed
// 6305 clears by about 10%. The BUFFER multiplier deliberately did NOT move: its
// slack absorbs corpus growth rather than noise, which is a different quantity
// from the 0.018% run-to-run spread buffers actually showed.
const (
	anchorServerMS = 6029
	anchorBuffers  = 3612181
	anchorRows     = 218399 // node_rows 35017 + edge_rows 183382
)

// manifestCapture is a captured CollectManifest response, reduced to what an
// equality is honestly about: how many entries it carried and a digest over the
// (path, hash) pairs in sorted order.
//
// SORTED BEFORE DIGESTING, because the response's ENTRY ORDER is not part of the
// contract — a digest over the wire order would report a re-ordering as a
// divergence and fail against correct work.
type manifestCapture struct {
	Graph   string `json:"graph"`
	Entries int    `json:"entries"`
	Digest  string `json:"digest"`
}

// captureManifest fetches the manifest for a graph and reduces it.
func captureManifest(
	t *testing.T, client knowledgev1connect.IngestServiceClient, graph string,
) manifestCapture {
	t.Helper()
	// The operation stamp is REQUIRED, not decorative: the client's operation
	// interceptor rejects a covered RPC issued with no operation in context, and
	// CollectManifest is covered. The shipped collect stamps it at the tool
	// boundary; a capture issued outside that boundary must stamp its own.
	ctx := graphclient.WithOperation(context.Background(), graphclient.OpCollect)
	resp, err := client.CollectManifest(ctx,
		connect.NewRequest(&knowledgev1.CollectManifestRequest{
			GraphType: "code", GraphName: graph,
		}))
	require.NoError(t, err, "fetch CollectManifest for %s", graph)

	lines := make([]string, 0, len(resp.Msg.GetEntries()))
	for _, e := range resp.Msg.GetEntries() {
		lines = append(lines, e.GetFilePath()+" "+hex.EncodeToString(e.GetContributionHash()))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintln(h, l)
	}
	return manifestCapture{Graph: graph, Entries: len(lines), Digest: hex.EncodeToString(h.Sum(nil))}
}

// readManifestCapture loads a capture the conductor asked for earlier.
func readManifestCapture(t *testing.T, name string) manifestCapture {
	t.Helper()
	var m manifestCapture
	readJSON(t, name, &m)
	return m
}

// TestCollectBench_CaptureManifest is a DRIVER, not an assertion: the conductor
// invokes it after each run's census has been captured, so the extra RPC does not
// land in the census it would otherwise perturb.
func TestCollectBench_CaptureManifest(t *testing.T) {
	m := captureManifest(t, benchIngestClient(benchServerURL(t)), benchGraphFor())
	blob, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	out := os.Getenv(envOut)
	require.NotEmpty(t, out, "%s must name where to write the capture", envOut)
	out = filepath.Clean(out)
	require.True(t, filepath.IsAbs(out), "%s must be an absolute path, got %q", envOut, out)
	require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600))
	t.Logf("manifest capture: %s", blob)
}

// TestCollectBench_FullCollectStaysInsideNoiseBand asserts a FULL collect on the
// armed tree stays inside a noise band around this tree's own measured cost.
//
// THE NAME AND THE COMPARISON BOTH CHANGED, and the old ones were a false
// statement rather than a loose one. This gate used to be
// FullCollectFloorHoldsAgainstBefore and compared against BEFORE-main; the census
// re-anchor replaced a top-15-by-queryid sum with a complete per-tag one, so
// "against before" names a comparison the instrument no longer supports. What
// survives is the property that actually matters going forward: a full collect
// does not drift.
//
// THIS IS STILL THE SHIP-ARMED PREMISE AS A GATE. The diff is armed on the
// reasoning that the incremental path is better in every way, with a floor for
// completely new collects — and every FIRST collect of a graph uploads the whole
// corpus, as does every degraded-lane collect. So the floor is load-bearing, not
// descriptive; only what it is measured against has moved.
//
// THE 0.98x LOWER BOUND IS VALID ONLY BECAUSE AFTER-full IS A FIRST COLLECT INTO
// A FRESH GRAPH. Every row is an INSERT, so the skip clause has nothing to
// compare against and cannot engage. Re-run this same band against a POPULATED
// graph and the lower bound goes red against correct work, because that is
// exactly when enforcement collapses the writes being measured. If AFTER-full is
// ever respecified onto the bench graph, this floor must be dropped or inverted
// rather than inherited.
func TestCollectBench_FullCollectStaysInsideNoiseBand(t *testing.T) {
	facts, sample := readFacts(t, runFullFacts), readSample(t, runFullSample)

	rows := sample.NodeRows + sample.EdgeRows
	buffers := sample.ReadSideBuffers + sample.WriteSideBuffers
	// The four bounds, evaluated once so the log line and the assertions below
	// cannot drift apart into two different numbers wearing one name.
	timeCeil := float64(anchorServerMS) * 1.15
	bufferCeil := float64(anchorBuffers) * 1.10
	rowFloor, rowCeil := float64(anchorRows)*0.98, float64(anchorRows)*1.10
	t.Logf("AFTER-full vs same-tree anchor: server_exec_ms %d vs %d (bound 1.15x => %.0f); "+
		"buffers %d vs %d (bound 1.10x => %.0f); write rows %d vs %d (bound 0.98x => %.0f, bound 1.10x => %.0f); "+
		"uploaded nodes %d edges %d",
		sample.ServerExecMS, anchorServerMS, timeCeil,
		buffers, anchorBuffers, bufferCeil,
		rows, anchorRows, rowFloor, rowCeil,
		facts.NodesUploaded, facts.EdgesUploaded)

	// The census is a COMPLETE per-tag sum over toplevel statements on both sides
	// — anchor and measurement come from the same instrument on the same tree,
	// which is what makes the comparison a noise budget rather than an instrument
	// difference wearing one column name.
	require.Positive(t, sample.ServerExecMS,
		"the AFTER-full census reported zero server time, so every bound below compares against nothing")

	assert.LessOrEqual(t, float64(sample.ServerExecMS), timeCeil,
		"AFTER-full collect-path server time %d ms exceeds bound 1.15x of the same-tree anchor %d ms",
		sample.ServerExecMS, anchorServerMS)
	assert.LessOrEqual(t, float64(buffers), bufferCeil,
		"AFTER-full collect-path buffers %d exceed bound 1.10x of the same-tree anchor %d", buffers, anchorBuffers)

	// TWO-SIDED, AND THE LOWER BOUND IS THE REAL ASSERTION: a DROP means the full
	// path stopped landing rows it used to land. The band is UNCHANGED at
	// [0.98, 1.10] across the re-anchor, because unlike time and buffers the row
	// count was never an instrument-dependent quantity — a landed row is a landed
	// row under either census — so only its reference point moved.
	assert.GreaterOrEqual(t, float64(rows), rowFloor,
		"AFTER-full landed %d write rows, below bound 0.98x of the same-tree anchor %d — "+
			"the full path stopped landing rows it used to land", rows, anchorRows)
	assert.LessOrEqual(t, float64(rows), rowCeil,
		"AFTER-full landed %d write rows, above bound 1.10x of the same-tree anchor %d", rows, anchorRows)
}

// TestCollectBench_ConvergesWithFreshFullCollect proves the diff-landed graph and
// a genuinely full-landed one agree.
//
// WHY ARM B MUST NOT COMPUTE A DIFF AT ALL. If both arms traveled the shared
// diff code path they could land identically WRONG and the comparison would still
// come out equal, proving only determinism. Shadow is not eligible either — it
// calls computeCollectDiff and is on the shared path by construction, differing
// only in what it uploads. So arm B runs through the tag-gated constructor, which
// skips the manifest fetch and the diff entirely and uploads every file exactly
// as the pre-incremental client did.
func TestCollectBench_ConvergesWithFreshFullCollect(t *testing.T) {
	url := benchServerURL(t)
	tree := benchTreeRoot(t)

	// The full-path arm's own recorder, so its RPC count is attributable to it
	// alone rather than to whatever else the phase did.
	rec := &recordingIngestClient{inner: benchIngestClient(url)}
	runOneCollect(t, tree, benchFullPathGraph, 0, func(knowledgev1connect.IngestServiceClient) collector.Sink {
		return remote.NewUploadSinkForBenchFullPath(
			func(context.Context) (knowledgev1connect.IngestServiceClient, error) { return rec, nil })
	})

	// ITS OWN GATE, BECAUSE THE PARENT COMPARISON CANNOT SEE IT. Arm B lands in a
	// FRESH graph, so an arm B that mistakenly ran the ordinary armed path would
	// fetch an EMPTY manifest and upload everything anyway — against zero entries
	// every present file reads CHANGED — leaving the landed state IDENTICAL and
	// map-equality still passing. The skip is therefore asserted directly.
	t.Run("full_path_arm_issues_no_manifest_rpc", func(t *testing.T) {
		manifestRPCs, finalizes, _ := rec.observed()
		require.Equal(t, 1, finalizes,
			"the full-path arm issued %d Finalize calls, expected 1 — a zero RPC count read off a run "+
				"that never collected is not evidence of a skip", finalizes)
		assert.Zero(t, manifestRPCs,
			"the full-path arm issued %d CollectManifest RPCs — it took the ordinary armed path, so "+
				"the comparison below would be a determinism check on ONE code path rather than a "+
				"comparison between two", manifestRPCs)
	})

	// The equivalence itself: equal file sets and equal per-file hashes, compared
	// client-side. Both manifests are fetched over the wire from the same server.
	client := benchIngestClient(url)
	diffLanded := captureManifest(t, client, benchGraphFor())
	fullLanded := captureManifest(t, client, benchFullPathGraph)

	require.Positive(t, diffLanded.Entries,
		"the diff-landed manifest is EMPTY, so an equality against it proves nothing")
	require.Positive(t, fullLanded.Entries,
		"the full-landed manifest is EMPTY, so an equality against it proves nothing")
	// NEVER assert one set's size against the other's: two manifests that lost the
	// SAME entries are still equal. Each side is checked non-empty above against
	// its own landing, and the digest below is what compares them.
	assert.Equal(t, diffLanded.Entries, fullLanded.Entries,
		"file-set sizes differ: diff-landed %d entries, full-landed %d",
		diffLanded.Entries, fullLanded.Entries)
	assert.Equal(t, diffLanded.Digest, fullLanded.Digest,
		"the diff-landed graph and a fresh FULL collect of the same tree do not agree: "+
			"their (file, contribution-hash) sets differ. The incremental path landed something "+
			"the full path would not have")
}
