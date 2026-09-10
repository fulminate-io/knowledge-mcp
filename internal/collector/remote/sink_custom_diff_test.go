// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sink_custom_diff_test.go — the NODE-KEYED upload path driven end to end
// through the real UploadSink against the recording fake, so every assertion
// reads the bytes the sink actually put on the wire.
//
// THE MEASUREMENT IS THE UPLOAD PAYLOAD, never a duration and never rows
// written. The server's per-row skip guard already writes zero row versions for
// a byte-identical re-collect whatever the client uploads, so rows written does
// not discriminate this change at all; the shipped node and edge counts do.
//
// EVERY R1/R2 CASE RUNS BOTH FILE SHAPES. A registered collector's file path is
// optional and usually absent, so a fixture whose nodes all carry paths would
// pass while the realistic fileless graph re-uploaded itself on every collect —
// the exact defect this ticket exists to remove, hiding behind a green suite.

// customResult builds a registered-custom CollectResult of two records and one
// edge between them. filePaths chooses the shape: with paths, the nodes look
// like a code graph's; without, every node is fileless and a FILE-keyed manifest
// would render empty for the whole graph.
func customResult(filePaths bool) *collectorwire.CollectResult {
	nodes := []*knowledgev1.Node{
		{Id: "issue-1", Type: "issue", SymbolName: "ACME-1", Content: "the first issue"},
		{Id: "issue-2", Type: "issue", SymbolName: "ACME-2", Content: "the second issue"},
	}
	if filePaths {
		nodes[0].FilePath = "issues/1.json"
		nodes[1].FilePath = "issues/2.json"
	}
	edges := []kgwire.BatchEdge{{
		FromIdx: -1, ToIdx: -1,
		FromID: "issue-1", ToID: "issue-2", Type: kgtypes.EdgeType("BLOCKS"),
	}}
	return &collectorwire.CollectResult{
		GraphType:              customType,
		GraphName:              "acme-tracker",
		Nodes:                  nodes,
		Edges:                  edges,
		WalkComplete:           true,
		DiscoveryFingerprint:   "custom-fingerprint-v1",
		CollectorOutputVersion: testCollectorOutputVersion,
	}
}

// customManifestMatching renders the NODE-KEYED manifest a server holding this
// exact result would serve: one entry per node id at the hash this client
// computes. Against it the changed set is EMPTY.
func customManifestMatching(result *collectorwire.CollectResult) *knowledgev1.CollectManifestResponse {
	hashes := contribhash.NodeKeyContributionHashes(result.Nodes, result.Edges)
	resp := &knowledgev1.CollectManifestResponse{
		ManifestId:        "custom-manifest-matching",
		HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	}
	for id, h := range hashes {
		resp.Entries = append(resp.Entries, newManifestEntry(diffKeyNode, id, append([]byte(nil), h[:]...)))
	}
	return resp
}

// runCustomCollect drives one collect of result against a server serving
// manifest, and returns the captured chunks and Finalize.
func runCustomCollect(
	t *testing.T, result *collectorwire.CollectResult, manifest *knowledgev1.CollectManifestResponse,
) ([]*knowledgev1.CollectChunkRequest, *knowledgev1.FinalizeRequest) {
	t.Helper()
	client, rec := startRecordingIngest(t)
	rec.manifest = manifest
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
	rec.mu.Lock()
	chunks := rec.chunks
	rec.mu.Unlock()
	return chunks, rec.finalizeRequest(t)
}

// shippedCounts sums the node and edge rows across every captured chunk — the
// upload payload, which is R1's discriminating reading.
func shippedCounts(chunks []*knowledgev1.CollectChunkRequest) (nodes, edges int) {
	for _, c := range chunks {
		nodes += len(c.GetNodes())
		edges += len(c.GetEdges())
	}
	return nodes, edges
}

// TestCustomDiff_UnchangedRecollectShipsNothing is R1. A second collect of a
// registered custom graph whose nodes and edges are byte-identical to the first
// ships ZERO nodes and ZERO edges.
//
// THE SAME-RUN KNOWN-POSITIVE IS THE FIRST COLLECT, asserted in each arm before
// the second: an instrument that reported zero for a collect that never ran
// would report zero here too, so the first pass's full upload is what proves the
// zero is a skip rather than a broken probe.
func TestCustomDiff_UnchangedRecollectShipsNothing(t *testing.T) {
	for _, arm := range []struct {
		name      string
		filePaths bool
	}{
		{"fileless custom nodes (the realistic shape)", false},
		{"file-bearing custom nodes", true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			isolateDiscoveryStore(t)
			t.Setenv(collectDiffEnv, "on")

			// PASS ONE: the server holds nothing, so the whole graph uploads. This is
			// the known-positive the zero below is read against, and it is R4.
			first := customResult(arm.filePaths)
			chunks, _ := runCustomCollect(t, first, nil)
			nodes, edges := shippedCounts(chunks)
			require.Equal(t, 2, nodes, "the first collect of a new custom graph uploads in full")
			require.Equal(t, 1, edges)

			// PASS TWO: the server now holds exactly what pass one sent.
			second := customResult(arm.filePaths)
			seedCollectBaselines(second)
			chunks, fin := runCustomCollect(t, second, customManifestMatching(second))
			nodes, edges = shippedCounts(chunks)
			require.Zero(t, nodes, "a byte-identical re-collect ships NO node bodies")
			require.Zero(t, edges, "and NO edge bodies")
			require.Len(t, chunks, 1, "an empty diff still sends exactly one chunk so Finalize has an epoch")
			require.True(t, chunks[0].GetDiffMode(),
				"the chunk is stamped as a diff — without this an empty chunk is indistinguishable from a degraded collect of an empty graph")
			require.True(t, fin.GetDiffMode(), "Finalize carries the same resolved mode")
			require.Empty(t, fin.GetDeletedNodeIds(), "nothing was dropped, so nothing is named")
			require.Empty(t, fin.GetDeletedFiles(), "and a node-keyed collect never fills the file carrier")
		})
	}
}

// TestCustomDiff_ChangedAndAddedShipExactly is R2. The shipped id SET equals the
// changed-plus-added set exactly — asserted per id rather than by count, because
// a count of two is satisfied by shipping the wrong two.
func TestCustomDiff_ChangedAndAddedShipExactly(t *testing.T) {
	for _, arm := range []struct {
		name      string
		filePaths bool
	}{
		{"fileless custom nodes", false},
		{"file-bearing custom nodes", true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			isolateDiscoveryStore(t)
			t.Setenv(collectDiffEnv, "on")

			// The manifest describes the ORIGINAL two records.
			base := customResult(arm.filePaths)
			manifest := customManifestMatching(base)

			// This collect edits one record's content and adds a third.
			next := customResult(arm.filePaths)
			next.Nodes[1].Content = "the second issue, edited"
			third := &knowledgev1.Node{Id: "issue-3", Type: "issue", SymbolName: "ACME-3", Content: "brand new"}
			if arm.filePaths {
				third.FilePath = "issues/3.json"
			}
			next.Nodes = append(next.Nodes, third)
			seedCollectBaselines(next)

			chunks, _ := runCustomCollect(t, next, manifest)
			require.Equal(t, map[string]bool{"issue-2": true, "issue-3": true},
				uploadedNodeIDs(chunks),
				"exactly the changed record and the added one — issue-1 is unchanged and must not ship")
		})
	}
}

// TestCustomDiff_EdgeChangeShipsItsSourceNode pins that the node key covers
// EDGES as well as node bodies. A node's key hash folds the node and the edges
// it owns, so an edge added, removed or edited moves its FROM node's key and
// that node re-lands carrying its whole edge set.
//
// WITHOUT THIS PROPERTY the node key would be a silent data-loss shape: an edge
// change leaves every node body identical, so a key covering the node alone
// would read UNCHANGED and the edge would never land.
func TestCustomDiff_EdgeChangeShipsItsSourceNode(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	base := customResult(false)
	manifest := customManifestMatching(base)

	next := customResult(false)
	next.Edges[0].Type = kgtypes.EdgeType("RELATES_TO")
	seedCollectBaselines(next)

	chunks, _ := runCustomCollect(t, next, manifest)
	require.Equal(t, map[string]bool{"issue-1": true}, uploadedNodeIDs(chunks),
		"the edge's FROM node re-lands; the unrelated node does not")
	_, edges := shippedCounts(chunks)
	require.Equal(t, 1, edges, "and the edge itself rides with its source")
}

// TestCustomDiff_SettledTupleRows are the two matrix rows that PIN THE SETTLED
// CONTRIBUTION TUPLE rather than reporting a defect. Custom graphs use the same
// fourteen fields the code family does, so a change confined to Summary or to
// Metadata is correctly invisible to the diff: summaries are pipeline-owned and
// metadata is outside the tuple on both sides.
//
// EACH ROW CARRIES A SAME-RUN POSITIVE — a Content change in the same fixture
// that DOES ship. Without it the row passes vacuously against a diff that never
// armed, which is the failure mode a "nothing shipped" assertion is most prone
// to.
func TestCustomDiff_SettledTupleRows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(n *knowledgev1.Node)
		wantShip bool
	}{
		{
			name:     "a collector-supplied SUMMARY change ships nothing",
			mutate:   func(n *knowledgev1.Node) { n.Summary = "a summary the collector made up" },
			wantShip: false,
		},
		{
			name:     "a METADATA-only change ships nothing",
			mutate:   func(n *knowledgev1.Node) { n.Metadata = map[string]string{"priority": "high"} },
			wantShip: false,
		},
		{
			name:     "the same-run positive: a CONTENT change ships",
			mutate:   func(n *knowledgev1.Node) { n.Content = "edited body" },
			wantShip: true,
		},
		{
			name: "metadata is not stranded: it rides whenever a hashed field also moved",
			mutate: func(n *knowledgev1.Node) {
				n.Content = "edited body"
				n.Metadata = map[string]string{"priority": "high"}
			},
			wantShip: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDiscoveryStore(t)
			t.Setenv(collectDiffEnv, "on")

			manifest := customManifestMatching(customResult(false))
			next := customResult(false)
			tc.mutate(next.Nodes[1])
			// READ BEFORE THE COLLECT. WriteResult REASSIGNS result.Nodes to the
			// narrowed subset, so the fixture's own slice is not the same slice
			// afterwards — reading through it after the call indexes the filtered set.
			wantMetadata := next.Nodes[1].GetMetadata()
			seedCollectBaselines(next)

			chunks, _ := runCustomCollect(t, next, manifest)
			ids := uploadedNodeIDs(chunks)
			if !tc.wantShip {
				require.Empty(t, ids, "the change is outside the settled fourteen-field tuple, so nothing ships")
				return
			}
			require.Equal(t, map[string]bool{"issue-2": true}, ids)
			if wantMetadata == nil {
				return
			}
			for _, c := range chunks {
				for _, n := range c.GetNodes() {
					if n.GetId() != "issue-2" {
						continue
					}
					require.Equal(t, "high", n.GetMetadata()["priority"],
						"the metadata rides with the row whose hashed field moved")
				}
			}
		})
	}
}

// TestCustomDiff_DroppedNodeIsNamedOnTheNodeCarrier is R7's client half: a
// record the previous collect carried and this one does not is NAMED on
// deleted_node_ids, and nothing is inferred from its absence.
//
// THE FILE CARRIER MUST STAY EMPTY, and that is the assertion with teeth: the
// server resolves the two fields under different rules and refuses a request
// carrying both, so a client that filled both would refuse every deletion phase
// on that graph.
func TestCustomDiff_DroppedNodeIsNamedOnTheNodeCarrier(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	manifest := customManifestMatching(customResult(false))

	dropped := customResult(false)
	dropped.Nodes = dropped.Nodes[:1] // issue-2 is gone
	dropped.Edges = nil               // and so is the edge it was the target of
	seedCollectBaselines(dropped)

	chunks, fin := runCustomCollect(t, dropped, manifest)
	require.Equal(t, []string{"issue-2"}, fin.GetDeletedNodeIds(),
		"the dropped record is named by id — never inferred from absence")
	require.Empty(t, fin.GetDeletedFiles(),
		"a node-keyed collect leaves the FILE carrier empty: the server refuses a request naming both")
	require.True(t, fin.GetWalkComplete(), "the completeness assertion is what arms the deletion phase at all")
	// THE SURVIVOR RE-LANDS, AND THAT IS THE NODE KEY WORKING RATHER THAN NOISE.
	// Dropping issue-2 also drops the edge issue-1 OWNED, and a node's key hash
	// folds the node together with its outbound edges — so issue-1's key genuinely
	// moved and its row must re-land carrying its new (empty) edge set. A diff that
	// left it alone would leave the stale edge on the server with nothing to clear
	// it.
	require.Equal(t, map[string]bool{"issue-1": true}, uploadedNodeIDs(chunks),
		"the edge's source re-lands because its owned edge set moved; nothing else ships")
}

// TestCustomDiff_IncompleteWalkNamesNoDeletion pins that the completeness
// assertion rides through unmodified. A collector that does not assert a
// complete walk sends walk_complete=false, and the server's deletion phase is
// disabled by it however the named set reads.
func TestCustomDiff_IncompleteWalkNamesNoDeletion(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	manifest := customManifestMatching(customResult(false))
	dropped := customResult(false)
	dropped.Nodes = dropped.Nodes[:1]
	dropped.Edges = nil
	dropped.WalkComplete = false
	seedCollectBaselines(dropped)

	_, fin := runCustomCollect(t, dropped, manifest)
	require.False(t, fin.GetWalkComplete(),
		"the client reports the collector's own assertion verbatim; the server refuses the phase on it")
}

// TestCustomDiff_ReachesTheDiffRatherThanErroring is R6. The two
// producer-regression refusals do not fire for a registered custom collect that
// carries its stamps, and the manifest is actually fetched.
//
// THE NEGATIVE ARMS ARE THE OTHER HALF. Each refusal is asserted to STILL FIRE
// when the stamp is genuinely absent, so admitting the family did not disarm
// either guard — a test that only proved the happy path would pass equally
// against a sink that had deleted them.
func TestCustomDiff_ReachesTheDiffRatherThanErroring(t *testing.T) {
	t.Run("a stamped custom collect reaches the manifest fetch and diffs", func(t *testing.T) {
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "on")

		client, rec := startRecordingIngest(t)
		result := customResult(false)
		seedCollectBaselines(result)
		rec.manifest = customManifestMatching(result)

		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
		rec.mu.Lock()
		calls := rec.manifestCalls
		rec.mu.Unlock()
		require.Equal(t, 1, calls, "a registered custom collect FETCHES a manifest — it no longer skips the diff")
	})

	t.Run("a node-keyed collect owes NO fileless baseline", func(t *testing.T) {
		// THE FILELESS BASELINE IS A FILE-KEYED CONCEPT AND TAKING IT HERE WOULD BE
		// ACTIVELY HARMFUL. Every node of a custom graph is fileless in the file
		// sense, so the digest would cover the WHOLE graph: one changed record moves
		// it, keepFileless flips, and the lane that exists to spare an undiffable set
		// would re-upload every node the diff had just narrowed away.
		//
		// The observable is the discovery store's own contents after a successful
		// collect — the baselines this collect committed.
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "on")

		client, rec := startRecordingIngest(t)
		rec.finalizeID = "fin-1"
		rec.tailState = knowledgev1.FinalizeState_FINALIZE_STATE_DONE
		result := customResult(false)
		rec.manifest = customManifestMatching(result)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

		entries, err := defaultDiscoveryStore.readLocked()
		require.NoError(t, err)
		require.Contains(t, entries, discoveryKey(result), "the discovery baseline IS owed")
		require.Contains(t, entries, collectorVersionKey(result), "and the collector-version one")
		require.NotContains(t, entries, filelessKey(result),
			"but the fileless baseline is a FILE-keyed concept and a node-keyed collect must not record one")
	})

	t.Run("an unstamped discovery fingerprint STILL aborts, before the fetch", func(t *testing.T) {
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "on")

		client, rec := startRecordingIngest(t)
		result := customResult(false)
		result.DiscoveryFingerprint = ""

		err := NewUploadSink(client).WriteResult(context.Background(), "", result)
		require.Error(t, err, "our own producer regressing is not repaired by a full collect")
		require.Contains(t, err.Error(), "empty discovery fingerprint")
		require.Contains(t, err.Error(), string(customType),
			"the diagnostic names the family it actually met, not a hardcoded one")
		rec.mu.Lock()
		calls := rec.manifestCalls
		rec.mu.Unlock()
		require.Zero(t, calls, "the fingerprint check costs no round trip")
	})

	t.Run("a zero collector output version STILL aborts", func(t *testing.T) {
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "on")

		client, rec := startRecordingIngest(t)
		result := customResult(false)
		result.CollectorOutputVersion = 0
		rec.manifest = customManifestMatching(result)

		err := NewUploadSink(client).WriteResult(context.Background(), "", result)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unstamped collector output version")
		require.Contains(t, err.Error(), string(customType), "and names the family")
		require.Zero(t, chunkCount(rec), "the abort precedes the first chunk")
	})
}

// TestCustomDiff_ProviderChangeRelandsInFull pins the collector-version trigger
// over the custom family: a collect whose provider identity MOVED re-lands the
// graph in full AND withholds the manifest echo, exactly as it does for code.
//
// THE ECHO SUPPRESSION IS THE HALF THAT MAKES IT WORK. The server's decline is
// not keyed on diff mode: it declines any key whose echoed identity and hash
// match, so a trigger that only forced a full upload would produce an upload the
// server declines key by key, and NOTHING would re-land.
func TestCustomDiff_ProviderChangeRelandsInFull(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	base := customResult(false)
	seedCollectBaselines(base)

	moved := customResult(false)
	moved.CollectorOutputVersion = testCollectorOutputVersion + 1

	chunks, fin := runCustomCollect(t, moved, customManifestMatching(moved))
	require.Equal(t, map[string]bool{"issue-1": true, "issue-2": true}, uploadedNodeIDs(chunks),
		"a moved provider identity re-lands the whole graph")
	require.Empty(t, fin.GetManifestId(),
		"and the manifest echo is WITHHELD, or the server declines every row and nothing re-lands")
	for _, c := range chunks {
		require.Empty(t, c.GetManifestId(), "the chunks withhold it too — one source, both consumers")
	}
	require.Empty(t, fin.GetDeletedNodeIds(), "a degraded lane names no deletions")
}

// TestCustomDiff_ChunksEchoNodeKeyedEntries pins the wire shape of the chunk's
// echoed contributions: a node-keyed collect fills the NODE arm, so the server's
// decoder reads ids rather than an empty file path.
func TestCustomDiff_ChunksEchoNodeKeyedEntries(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	result := customResult(false)
	seedCollectBaselines(result)
	// A manifest the collect DISAGREES with, so rows actually ship and the chunk
	// carries entries to inspect.
	chunks, _ := runCustomCollect(t, result, &knowledgev1.CollectManifestResponse{
		ManifestId:        "custom-manifest-empty",
		HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	})
	var seen int
	for _, c := range chunks {
		for _, e := range c.GetFileContributions() {
			key, kind, ok := manifestEntryKey(e)
			require.True(t, ok, "every echoed entry carries a key")
			require.Equal(t, diffKeyNode, kind, "on the NODE arm")
			require.NotEmpty(t, key)
			seen++
		}
	}
	require.Positive(t, seen, "a node-keyed collect echoes its keys, or the server can decline nothing")
}
