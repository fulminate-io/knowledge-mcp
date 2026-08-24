// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	kgtypes "github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	kgwire "github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sink_fileless_decline_test.go — the FILELESS payload's decline basis.
//
// WHAT IT EXISTS TO PROVE. The fileless nodes — the hierarchy package nodes, the
// repo root, the language hub — are outside the manifest by construction, so the
// server never declines them and a diff-mode collect re-uploaded all of them on
// every run. The client now compares a whole-set signature against the last
// DONE-confirmed upload and drops the set when it matches.
//
// EVERY TEST HERE MUST ISOLATE THE DISCOVERY STORE. The package var points at the
// developer's real ~/.knowledge/collect-discovery.json, so a test that writes
// there corrupts the machine's collect state AND makes its own second collect's
// decline an artifact of a previous run rather than of this one.

// filelessDeclineResult builds a code CollectResult carrying BOTH populations:
// one fileless node with outbound edges to file-owned nodes, and two file-owned
// nodes. It is the deliberate opposite of twoFileResult, which carries no
// fileless node at all.
//
// IT TAKES THE HUB'S CONTENT so one call site can drive the signature to MOVE
// without changing anything else — the third arm below needs exactly that, and a
// fixed fixture could not express it. It is a SEPARATE constructor from
// sink_diff_upload_test.go's filelessResult because these tests own a distinct
// graph name: the decline is keyed per graph and branch, so sharing a name would
// let one test's recorded baseline decide another's outcome.
func filelessDeclineResult(hubContent string) *collectorwire.CollectResult {
	nodes := []*knowledgev1.Node{
		{Id: "pkg", Type: "package", SymbolName: "pkg", Content: hubContent, Language: "go"},
		{Id: "pkg/a.go:Alpha", Type: "function", SymbolName: "Alpha", FilePath: "pkg/a.go", Language: "go"},
		{Id: "pkg/b.go:Beta", Type: "function", SymbolName: "Beta", FilePath: "pkg/b.go", Language: "go"},
	}
	edges := []kgwire.BatchEdge{
		// THE HUB'S OWN OUTBOUND EDGE: this is the one that must ride or be dropped
		// with its node and never separately.
		{FromIdx: -1, ToIdx: -1, FromID: "pkg", ToID: "pkg/a.go:Alpha", Type: kgtypes.EdgeType("CONTAINS")},
		{FromIdx: -1, ToIdx: -1, FromID: "pkg", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CONTAINS")},
		{FromIdx: -1, ToIdx: -1, FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CALLS")},
	}
	return &collectorwire.CollectResult{
		GraphType:              kgtypes.GraphCode,
		GraphName:              "fileless-decline-repo",
		CurrentBranch:          "main",
		Nodes:                  nodes,
		Edges:                  edges,
		WalkComplete:           true,
		DiscoveryFingerprint:   "fingerprint-v1",
		CollectorOutputVersion: testCollectorOutputVersion,
	}
}

// uploadedRows flattens every chunk the fake recorded into one node and edge set.
func uploadedRows(rec *recordingIngest) (nodes []*knowledgev1.Node, edges []*knowledgev1.BatchEdge) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, c := range rec.chunks {
		nodes = append(nodes, c.GetNodes()...)
		edges = append(edges, c.GetEdges()...)
	}
	return nodes, edges
}

// filelessNodeIDs returns the ids of the uploaded nodes carrying no file path.
func filelessNodeIDs(nodes []*knowledgev1.Node) []string {
	var out []string
	for _, n := range nodes {
		if n.GetFilePath() == "" {
			out = append(out, n.GetId())
		}
	}
	return out
}

// runFilelessCollect drives one whole collect against a fresh fake whose manifest
// agrees with the result, with the tail reporting DONE so the baselines advance.
func runFilelessCollect(t *testing.T, result *collectorwire.CollectResult, finalizeID string) *recordingIngest {
	t.Helper()
	client, rec := startRecordingIngest(t)
	rec.manifest = manifestMatching(result)
	rec.finalizeID = finalizeID
	rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_DONE
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
	return rec
}

// seedFilelessSiblingBaselines records the discovery and collector-version
// baselines this result would leave behind — but NOT the fileless one.
//
// IT SEEDS EXACTLY TWO OF THREE, deliberately. Both siblings are evaluated as
// fallback triggers before the fileless comparison matters, so a fixture that
// seeded neither would fall back to a full collect on every run and the decline
// could never be observed. Leaving the FILELESS baseline unseeded is what makes
// the first collect below a genuine known-positive rather than a foregone one.
func seedFilelessSiblingBaselines(t *testing.T, result *collectorwire.CollectResult) {
	t.Helper()
	require.NoError(t, defaultDiscoveryStore.record(
		baselineCommit{key: discoveryKey(result), sig: discoverySignature(result)},
		baselineCommit{
			key: collectorVersionKey(result),
			sig: strconv.FormatUint(uint64(result.CollectorOutputVersion), 10),
		}))
}

// TestDiffUpload_UnchangedFilelessSetIsNotUploaded is the decline itself, driven
// in BOTH directions over three collects of one fixture.
//
// THE THREE ARMS ARE NOT INTERCHANGEABLE. The first is the known-positive control
// — without it, a fixture that happened to carry no fileless nodes would satisfy
// the second assertion vacuously. The third mutates the fileless payload and
// asserts it rides again, which is what proves the digest MOVES rather than
// merely that something is absent: a counter that can never be driven non-zero
// cannot be told from one that was never wired.
func TestDiffUpload_UnchangedFilelessSetIsNotUploaded(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	first := filelessDeclineResult("package pkg")
	seedFilelessSiblingBaselines(t, first)

	// ARM 1 — KNOWN-POSITIVE CONTROL. No fileless baseline exists yet, so the set
	// is unconditionally changed and must ride.
	rec1 := runFilelessCollect(t, first, "fin-fileless-1")
	nodes1, _ := uploadedRows(rec1)
	require.Equal(t, []string{"pkg"}, filelessNodeIDs(nodes1),
		"control: with no recorded baseline the fileless set MUST upload — otherwise the absence "+
			"asserted below is a property of the fixture rather than of the decline")

	// ARM 2 — THE DECLINE. The identical result against a matching manifest: the
	// signature now equals the one arm 1's DONE tail recorded.
	rec2 := runFilelessCollect(t, filelessDeclineResult("package pkg"), "fin-fileless-2")
	nodes2, _ := uploadedRows(rec2)
	require.Empty(t, filelessNodeIDs(nodes2),
		"an unchanged fileless payload must not be re-uploaded on the second diff collect")

	// ARM 3 — THE DIGEST MOVES. One fileless node's content changes, so the
	// signature differs and the whole set rides again.
	rec3 := runFilelessCollect(t, filelessDeclineResult("package pkg // edited"), "fin-fileless-3")
	nodes3, _ := uploadedRows(rec3)
	require.Equal(t, []string{"pkg"}, filelessNodeIDs(nodes3),
		"a CHANGED fileless payload must ride again — a digest that can never move is indistinguishable "+
			"from one that was never wired")
}

// TestDiffUpload_FilelessDeclineDropsNodesAndEdgesTogether asserts the decline's
// ATOMICITY directly.
//
// THE PRODUCTION CONSEQUENCE THIS STANDS IN FOR: the server's zero-edge residual
// reclaim deletes the whole outbound edge set of any source that is NODE-present
// but EDGE-absent at one epoch. Uploading fileless nodes while filtering their
// edges — or the reverse — would therefore delete every package hub's entire
// CONTAINS set. If a future edit ever splits the two, this test goes red.
func TestDiffUpload_FilelessDeclineDropsNodesAndEdgesTogether(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	first := filelessDeclineResult("package pkg")
	seedFilelessSiblingBaselines(t, first)

	// The first collect records the baseline the second declines against, and is
	// also the known-positive for the edge half: its hub edges DO ride.
	rec1 := runFilelessCollect(t, first, "fin-atomic-1")
	_, edges1 := uploadedRows(rec1)
	var hubEdges1 int
	for _, e := range edges1 {
		if e.GetFromId() == "pkg" {
			hubEdges1++
		}
	}
	require.Equal(t, 2, hubEdges1,
		"control: the fileless hub's two outbound edges ride on the first collect — a fixture whose "+
			"hub carried no edges would satisfy the absence below without proving anything")

	rec2 := runFilelessCollect(t, filelessDeclineResult("package pkg"), "fin-atomic-2")
	nodes2, edges2 := uploadedRows(rec2)

	onWire := make(map[string]bool, len(nodes2))
	for _, n := range nodes2 {
		onWire[n.GetId()] = true
	}
	require.Empty(t, filelessNodeIDs(nodes2), "precondition: this collect declined the fileless set")
	for _, e := range edges2 {
		require.NotEqual(t, "pkg", e.GetFromId(),
			"no edge whose source is a DECLINED fileless node may ride: that hub would be node-absent "+
				"and edge-present, and the next collect's reclaim reads the mismatch the other way")
		require.True(t, onWire[e.GetFromId()],
			"every uploaded edge's FromID must resolve to a node also on the wire — an edge whose source "+
				"was filtered out lands against a source the server sees as carrying no edges")
	}
}

// TestFilelessBaseline_NotAdvancedOnUnconfirmedTail pins the fileless baseline to
// the same DONE-only commit rule its two siblings obey: it rides
// outcome.baselines to commitCollectBaselines and advances on nothing else.
//
// THE SUCCESS ARM IS MANDATORY. This package's fake reports UNKNOWN by default,
// so a fileless baseline that was never appended to the pending set at all would
// satisfy the failure arm perfectly — the vacuity manifest_record_test.go's own
// header warns about.
func TestFilelessBaseline_NotAdvancedOnUnconfirmedTail(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	result := filelessDeclineResult("package pkg")
	seedFilelessSiblingBaselines(t, result)
	before := readBaselineStore(t)
	require.NotContains(t, string(before), filelessKey(result),
		"precondition: the fileless key is NOT seeded, so any appearance below is this collect's own write")

	// UNKNOWN TAIL: the collect stands, the durable half committed, but the tail
	// was never observed to complete.
	client, rec := startRecordingIngest(t)
	rec.manifest = manifestMatching(result)
	rec.finalizeID = "fin-fileless-unknown"
	rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	require.Equal(t, string(before), string(readBaselineStore(t)),
		"an UNKNOWN tail is an unobserved outcome, and the fileless baseline must not advance on one")

	// KNOWN POSITIVE, same fixture and same store: a DONE tail DOES write it.
	rec2 := runFilelessCollect(t, filelessDeclineResult("package pkg"), "fin-fileless-done")
	require.NotNil(t, rec2)
	after := readBaselineStore(t)
	require.NotEqual(t, string(before), string(after),
		"control: DONE MUST advance the baselines, or the assertion above proves nothing")
	require.Contains(t, string(after), filelessKey(result),
		"and the key that moved is the FILELESS one specifically, not merely one of its siblings")
}
