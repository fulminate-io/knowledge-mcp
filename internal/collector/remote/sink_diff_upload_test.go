// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sink_diff_upload_test.go — the armed upload path driven end-to-end through
// WriteResult against the recording fake, so every assertion reads the bytes the
// sink actually put on the wire rather than an intermediate value.
//
// THIS FILE IS THE CLIENT HALF OF A TWO-SIDED CONTRACT, and neither half is the
// contract alone. The client and the server are separate Go modules with no
// hand-written package shared between them, so a single-process
// collector-to-database test is impossible. The SERVER half is the
// collect_incrementality_*_integration_test.go family in the server module, where
// a wire-level rig proves the served manifest covers every discovered file at the
// client's hash. THIS half proves that, GIVEN such a manifest, the real
// computeCollectDiff plus filterToChangedFiles narrow the upload correctly. The
// join between them is the CollectManifest proto, which both sides already share.

// isolateDiscoveryStore points the package's discovery-signature store at a
// temp file for the duration of one test.
//
// WITHOUT THIS A TEST READS AND WRITES THE REAL ~/.knowledge/collect-discovery.json.
// That is other people's state: the discovery trigger would fire or not
// depending on what the developer's last real collect recorded, and the test
// would leave a bogus record behind for the next one.
func isolateDiscoveryStore(t *testing.T) {
	t.Helper()
	prev := defaultDiscoveryStore
	defaultDiscoveryStore = &discoveryStore{path: filepath.Join(t.TempDir(), "collect-discovery.json")}
	t.Cleanup(func() { defaultDiscoveryStore = prev })
}

// testCollectorOutputVersion is the collector identity every fixture stamps. It
// is a FIXTURE-LOCAL LITERAL rather than parser.CollectorOutputVersion: these
// tests assert how the sink reacts to a version that MOVED or did not, which is
// a property of the comparison, and binding them to the production const would
// make every future bump of it rewrite unrelated fixtures.
const testCollectorOutputVersion = 7

// seedCollectBaselines records BOTH baselines this result would leave behind, so
// a following collect of the same shape reads as neither a discovery-mode change
// NOR a collector-version change. It goes through the production signature and
// key functions rather than literals, so a nondeterministic signature would still
// trip a trigger and be caught here.
//
// IT SEEDS BOTH because the collector-version trigger is evaluated BEFORE the
// discovery one: a fixture that seeded only the discovery half would fire the
// collector-version trigger on every collect and mask every subtler reason.
func seedCollectBaselines(result *collectorwire.CollectResult) {
	_ = defaultDiscoveryStore.record(
		baselineCommit{key: discoveryKey(result), sig: discoverySignature(result)},
		baselineCommit{
			key: collectorVersionKey(result),
			sig: strconv.FormatUint(uint64(result.CollectorOutputVersion), 10),
		})
}

// twoFileResult builds a code CollectResult whose every node belongs to a file —
// NO FILELESS NODES, deliberately. The fileless set always uploads, so a fixture
// carrying one could never produce a genuinely empty diff upload.
func twoFileResult() *collectorwire.CollectResult {
	nodes := []*knowledgev1.Node{
		{Id: "pkg/a.go:Alpha", Type: "function", SymbolName: "Alpha", FilePath: "pkg/a.go", Language: "go"},
		{Id: "pkg/b.go:Beta", Type: "function", SymbolName: "Beta", FilePath: "pkg/b.go", Language: "go"},
	}
	edges := []kgwire.BatchEdge{{
		FromIdx: -1, ToIdx: -1,
		FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CALLS"),
	}}
	return &collectorwire.CollectResult{
		GraphType:              kgtypes.GraphCode,
		GraphName:              "diff-upload-repo",
		CurrentBranch:          "main",
		Nodes:                  nodes,
		Edges:                  edges,
		WalkComplete:           true,
		DiscoveryFingerprint:   "fingerprint-v1",
		CollectorOutputVersion: testCollectorOutputVersion,
	}
}

// manifestMatching renders a manifest that agrees with the result exactly: one
// entry per present file carrying the hash this client computes. Against it the
// changed set is EMPTY, which is the whole point of the no-change case.
func manifestMatching(result *collectorwire.CollectResult) *knowledgev1.CollectManifestResponse {
	hashes := contribhash.FileContributionHashes(result.Nodes, result.Edges)
	resp := &knowledgev1.CollectManifestResponse{
		ManifestId:        "manifest-matching",
		HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	}
	for path, h := range hashes {
		resp.Entries = append(resp.Entries, &knowledgev1.ManifestEntry{
			FilePath: path, ContributionHash: append([]byte(nil), h[:]...),
		})
	}
	return resp
}

// TestDiffUpload_NoChangeStillSendsOneChunk pins the guarantee stated at the top
// of collectChunkRequests: a collect whose diff is empty STILL reaches the
// server, so Finalize has an epoch the server has seen. A deletion-only
// re-collect that sent nothing at all would strand its own Finalize.
//
// THE CHUNK COUNT ALONE PROVES NOTHING and the fixture is small enough to make
// that obvious: a degraded lane uploads both nodes and that also fits in exactly
// one chunk. The discriminating assertions are that the single chunk carries ZERO
// nodes and ZERO edges, and that the collect resolved ARMED — together they say
// the diff governed and still sent its chunk.
func TestDiffUpload_NoChangeStillSendsOneChunk(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	seedCollectBaselines(result)
	rec.manifest = manifestMatching(result)

	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	captured := rec.chunks
	rec.mu.Unlock()

	require.Len(t, captured, 1, "an empty diff must still send exactly one CollectChunk")
	require.Empty(t, captured[0].GetNodes(), "the diff was empty, so the chunk carries no nodes")
	require.Empty(t, captured[0].GetEdges(), "the diff was empty, so the chunk carries no edges")
	require.True(t, captured[0].GetDiffMode(),
		"the chunk must be stamped as a diff — without this the empty chunk is indistinguishable from a degraded collect of an empty repo")
	require.True(t, rec.finalizeRequest(t).GetDiffMode(), "Finalize carries the same resolved mode as the chunks")
}

// filelessResult builds a code CollectResult carrying BOTH file-bearing nodes and
// a FILELESS set (a package node and a language hub, neither with a FilePath),
// plus one edge out of the fileless set.
//
// IT IS A SEPARATE CONSTRUCTOR RATHER THAN A CHANGE TO twoFileResult, and the
// reason is twoFileResult's own comment: it deliberately carries no fileless nodes
// because the fileless set always uploads, so a fixture with one could never
// produce a genuinely empty diff. That is exactly why the tests below need the
// OPPOSITE fixture — and why twoFileResult's two existing callers, which depend on
// the empty-diff property, must keep it.
func filelessResult() *collectorwire.CollectResult {
	nodes := []*knowledgev1.Node{
		{Id: "pkg/a.go:Alpha", Type: "function", SymbolName: "Alpha", FilePath: "pkg/a.go", Language: "go", Content: "func Alpha() {}"},
		{Id: "pkg/b.go:Beta", Type: "function", SymbolName: "Beta", FilePath: "pkg/b.go", Language: "go", Content: "func Beta() {}"},
		{Id: "pkg", Type: "package", SymbolName: "pkg"},
		{Id: "language:go", Type: "language", SymbolName: "go"},
	}
	edges := []kgwire.BatchEdge{
		{FromIdx: -1, ToIdx: -1, FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CALLS")},
		{FromIdx: -1, ToIdx: -1, FromID: "pkg", ToID: "pkg/a.go:Alpha", Type: kgtypes.EdgeType("CONTAINS")},
	}
	return &collectorwire.CollectResult{
		GraphType:              kgtypes.GraphCode,
		GraphName:              "diff-upload-fileless-repo",
		CurrentBranch:          "main",
		Nodes:                  nodes,
		Edges:                  edges,
		WalkComplete:           true,
		DiscoveryFingerprint:   "fingerprint-fileless-v1",
		CollectorOutputVersion: testCollectorOutputVersion,
	}
}

// uploadedNodeIDs returns the union of every node id across the captured chunks —
// the bytes that actually went on the wire.
func uploadedNodeIDs(chunks []*knowledgev1.CollectChunkRequest) map[string]bool {
	out := map[string]bool{}
	for _, c := range chunks {
		for _, n := range c.GetNodes() {
			out[n.GetId()] = true
		}
	}
	return out
}

// manifestOmitting renders a matching manifest with one file's entry REMOVED
// entirely, modeling a server that never served that path.
func manifestOmitting(result *collectorwire.CollectResult, path string) *knowledgev1.CollectManifestResponse {
	resp := manifestMatching(result)
	kept := resp.Entries[:0]
	for _, e := range resp.Entries {
		if e.GetFilePath() != path {
			kept = append(kept, e)
		}
	}
	resp.Entries = kept
	return resp
}

// TestDiffUpload_OneFileChangeUploadsOnlyThatFileAndFilelessSet drives the REAL
// UploadSink and asserts on the wire bytes: given a manifest agreeing with every
// file except one, the sink ships exactly that file's rows plus the whole fileless
// set.
//
// THE ASSERTION IS A SET EQUALITY OVER IDS, NOT A COUNT, and that is the point: a
// count of three is satisfied by uploading the WRONG file's three nodes. The
// property is per-id membership, so the assertion is per-id.
//
// ITS PAIR IS TestDiffUpload_NoChangeStillSendsOneChunk above, which asserts an
// EMPTY upload through the same sink and the same recording fake. A sink that
// uploaded nothing under any circumstances cannot satisfy both, and a sink that
// uploaded everything cannot either.
func TestDiffUpload_OneFileChangeUploadsOnlyThatFileAndFilelessSet(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := filelessResult()

	// The manifest is rendered against the ORIGINAL content, so it agrees with both
	// files; the edit below then makes exactly one of them disagree.
	rec.manifest = manifestMatching(result)
	result.Nodes[0].Content = "func Alpha() { edited() }"
	// Seeded AFTER the edit so the recorded signature is the one this collect
	// presents — a signature seeded before it could read as a discovery-mode change
	// and force a full upload, which would silently defeat the whole test.
	seedCollectBaselines(result)

	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	captured := rec.chunks
	rec.mu.Unlock()

	got := uploadedNodeIDs(captured)
	require.Equal(t,
		map[string]bool{"pkg/a.go:Alpha": true, "pkg": true, "language:go": true},
		got,
		"the upload is not exactly the edited file's rows plus the fileless set")
	require.NotContains(t, got, "pkg/b.go:Beta",
		"an untouched file's node went on the wire")

	for _, c := range captured {
		for _, e := range c.GetEdges() {
			require.True(t, got[e.GetFromId()],
				"edge %s->%s rode along without its FROM node, so it landed on a file this collect did not upload",
				e.GetFromId(), e.GetToId())
		}
	}
	require.True(t, captured[0].GetDiffMode(), "the chunk must be stamped as a diff")
	require.True(t, rec.finalizeRequest(t).GetDiffMode(), "Finalize carries the same resolved mode as the chunks")
}

// TestDiffUpload_FileMissingFromManifestReadsChanged covers the OTHER half of the
// diff condition. computeCollectDiff asks
// `if prior, ok := d.manifestFiles[path]; ok && prior == h` — an ABSENT entry and a
// DIFFERING entry reach the changed branch through different halves of that
// conjunction, and a regression could break either alone. The test above exercises
// the differing-hash half; this one exercises the absent half, which is precisely
// the shape a manifest that omits proxy-only paths produces.
//
// IT STAYS GREEN AFTER THE SERVER-SIDE FIX, deliberately: that fix changes what the
// SERVER renders, not how the client treats an absent entry. A client-side test
// that went red on a server-side fix would be a scheduled false failure.
func TestDiffUpload_FileMissingFromManifestReadsChanged(t *testing.T) {
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	result := filelessResult()
	seedCollectBaselines(result)

	// Every file's content is UNCHANGED against the manifest; the only difference is
	// that pkg/b.go was never served. So anything uploaded for pkg/b.go is
	// attributable to the absent entry and to nothing else.
	rec.manifest = manifestOmitting(result, "pkg/b.go")

	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	captured := rec.chunks
	rec.mu.Unlock()

	got := uploadedNodeIDs(captured)
	require.Contains(t, got, "pkg/b.go:Beta",
		"a file the manifest never served must read CHANGED and upload")
	require.NotContains(t, got, "pkg/a.go:Alpha",
		"control: the file the manifest DID serve at a matching hash must still be declined, "+
			"or this test is passing because the diff degraded to a full upload")
}

// TestWalkComplete_UnreadableFileClearsFlag is the catcher for a hardcoded
// `WalkComplete: true`, which satisfies every field-presence gate while disarming
// guard 2 — the only thing standing between a file that failed to READ and being
// NAMED as a deletion.
//
// THE TWO SUBTESTS DIFFER IN EXACTLY ONE INPUT, the walk signal. Everything else
// — nodes, edges, manifest, branch, fingerprint — is the same fixture, so a
// difference in the FinalizeRequest can only have come from that signal. The
// clean-walk control is what makes the false assertion meaningful: without it a
// sink hardcoding `false` would pass just as well as a correct one.
func TestWalkComplete_UnreadableFileClearsFlag(t *testing.T) {
	run := func(t *testing.T, walkComplete bool) *knowledgev1.FinalizeRequest {
		t.Helper()
		isolateDiscoveryStore(t)
		client, rec := startRecordingIngest(t)
		result := twoFileResult()
		result.WalkComplete = walkComplete
		seedCollectBaselines(result)
		rec.manifest = manifestMatching(result)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
		return rec.finalizeRequest(t)
	}

	t.Run("clean_walk_sets_the_flag", func(t *testing.T) {
		require.True(t, run(t, true).GetWalkComplete(),
			"a walk that read every file it set out to read reports complete")
	})
	t.Run("unreadable_file_clears_the_flag", func(t *testing.T) {
		require.False(t, run(t, false).GetWalkComplete(),
			"an unreadable file must clear walk_complete ON THE WIRE — the server's guard 2 reads this field and nothing else")
	})
}
