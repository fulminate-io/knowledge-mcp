// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	collectorwire "github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// collectbench_harness_test.go — the shared driver every RUN after RUN 1 goes
// through, plus the JSON hand-off the conductor assembles the artifact from.
//
// WHY THE MEASUREMENTS ARE SPLIT ACROSS A SCRIPT AND A TEST, since the seam is
// not obvious from either side. The server-side facts — landed row counts, xmin,
// the pg_stat_statements census — can only be read over psql, and the client
// module must not grow a Postgres driver (the same promise that keeps
// testcontainers out of it: cmd/knowledge is the OSS-shipped binary). So the
// CONDUCTOR samples psql around each collect and writes one JSON per run, and
// the ASSERTION tests in this package read those files back and do the
// comparing. That keeps every bound inside a named Go test — which is what makes
// the anchored `--- PASS:` line a criterion greps for mean anything — without
// putting a driver where it may not go.

const (
	// envTreeRoot names the tree a run collects. The conductor points it at a
	// COPY of this repository under its workdir, so the K=25 mutation edits files
	// that are not the developer's working tree. Unset falls back to repoRoot for
	// RUN 1's in-place baseline.
	envTreeRoot = "KBENCH_TREE"

	// envMutateK is how many files this run mutates before collecting. "0" and
	// unset both mean a quiescent run.
	envMutateK = "KBENCH_MUTATE_K"

	// envGraph overrides the graph a run lands in. The convergence arm needs a
	// second, independent landing surface; unset means benchGraphName.
	envGraph = "KBENCH_GRAPH"

	// envSamplesDir is the conductor's workdir, where it writes the per-run psql
	// samples and client records the assertion tests read back.
	envSamplesDir = "KBENCH_SAMPLES_DIR"
)

// benchRunFacts is the client half of one run's record. It carries only what a
// client can honestly observe; every server-side number lives in the psql sample
// beside it.
type benchRunFacts struct {
	RunLabel      string `json:"run_label"`
	Graph         string `json:"graph"`
	TreeRoot      string `json:"repo_root"`
	WallTimeMS    int64  `json:"wall_time_ms"`
	ChunkMS       int64  `json:"chunk_ms"`
	UploadMS      int64  `json:"upload_ms"`
	NodesUploaded int    `json:"nodes_uploaded"`
	EdgesUploaded int    `json:"edges_uploaded"`
	// MutatedFiles is how many files this run edited before collecting, and
	// MutatedNodes/MutatedEdges are those files' ACTUAL node and edge counts in
	// the collected result. The K-scaling bound is expressed against the actual
	// share rather than the file-count share, and this is where that share is
	// known: the collector's result still carries every file's nodes at this
	// point, so partitioning by owning file is a client-side fact.
	MutatedFiles int `json:"mutated_files"`
	MutatedNodes int `json:"mutated_nodes"`
	MutatedEdges int `json:"mutated_edges"`
	// TotalNodes/TotalEdges are the WHOLE collected tree's counts, before the
	// diff narrows the upload. NodesUploaded below them is what actually shipped,
	// and the gap between the two is the diff's own effect.
	TotalNodes int `json:"total_nodes"`
	TotalEdges int `json:"total_edges"`
	// UploadedFileOwnedFiles is how many DISTINCT owning files the upload set
	// carried. It is the first half of quiescence clause (c): on a K=0 armed run
	// it must be ZERO, because only the fileless set ships.
	UploadedFileOwnedFiles int `json:"uploaded_file_owned_files"`
	// DeletedFilesOnFinalize is the size of the deletion set the FinalizeRequest
	// carried, and Finalizes is how many Finalize calls were issued. The second
	// is what stops a zero-length deletion set read off a run that never
	// finalized from passing as an empty one.
	DeletedFilesOnFinalize int `json:"deleted_files_on_finalize"`
	Finalizes              int `json:"finalizes"`
	// ManifestRPCs is how many CollectManifest calls this run's sink issued.
	ManifestRPCs int `json:"manifest_rpcs"`
}

// benchTreeRoot resolves the tree a run collects: the conductor's copy when it
// pointed us at one, otherwise this repository in place.
//
// THE COPY IS NOT A CONVENIENCE. RUN 3 mutates K files before collecting, and a
// bench that edited the developer's own working tree to produce a measurement
// would be destroying uncommitted work to make a number.
func benchTreeRoot(t *testing.T) string {
	t.Helper()
	if tree := os.Getenv(envTreeRoot); tree != "" {
		require.True(t, filepath.IsAbs(tree), "%s must be absolute, got %q", envTreeRoot, tree)
		require.DirExists(t, tree, "%s does not exist", envTreeRoot)
		return tree
	}
	return repoRoot(t)
}

// benchGraphFor resolves which graph a run lands in.
func benchGraphFor() string {
	if g := os.Getenv(envGraph); g != "" {
		return g
	}
	return benchGraphName
}

// mutateKFiles edits the first k Go files under tree in sorted order and returns
// their tree-relative paths.
//
// THE SELECTION IS SORTED AND THEREFORE DETERMINISTIC across runs, which is what
// lets a later run mutate THE SAME files and lets the artifact name them.
//
// THE EDIT GOES INSIDE A FUNCTION BODY, AND THAT IS THE WHOLE POINT. Appending a
// comment to the END of the file was the obvious form and it measured almost
// nothing: the per-file contribution hash moved, so the diff correctly selected
// the file and uploaded it — but the SYMBOL nodes in that file were still
// byte-identical, so the server's per-row skip clause declined every one of them
// and the run landed a single row. The K-scaling bound was then satisfied by a
// factor of thousands while never exercising the payoff it exists to measure.
// A statement inserted into a function body changes that function's node
// CONTENT, which is what the node contribution hash covers, so the mutated
// files' rows genuinely re-land.
//
// `_ = 0` is chosen because it is legal in ANY function body and references
// nothing, so the copied tree still parses and still compiles.
func mutateKFiles(t *testing.T, tree string, k int) []string {
	t.Helper()
	if k == 0 {
		return nil
	}
	var candidates []string
	require.NoError(t, filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip trees the collector does not chunk anyway, so the mutation
			// budget is spent on files that actually produce nodes.
			if name := info.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only files carrying a multi-line function are candidates: a file with
		// no body to edit would be "mutated" into a no-op, which is the failure
		// mode this selection exists to avoid.
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && hasEditableFuncBody(path) {
			candidates = append(candidates, path)
		}
		return nil
	}), "walk the copied tree")
	sort.Strings(candidates)
	require.GreaterOrEqual(t, len(candidates), k,
		"the copied tree holds only %d mutable Go files with an editable function body, fewer than the requested K=%d",
		len(candidates), k)

	rel := make([]string, 0, k)
	for _, path := range candidates[:k] {
		require.True(t, insertIntoFirstFuncBody(t, path), "mutate %s: no function body found after selecting it", path)
		r, rerr := filepath.Rel(tree, path)
		require.NoError(t, rerr)
		rel = append(rel, r)
	}
	return rel
}

// firstFuncBodyLine returns the index of the line OPENING the first top-level
// function body — a line beginning `func ` and ending in `{` — or -1.
func firstFuncBodyLine(lines []string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, "func ") && strings.HasSuffix(strings.TrimRight(l, "\r"), "{") {
			return i
		}
	}
	return -1
}

// hasEditableFuncBody reports whether path carries a function this mutation can
// edit, so selection and mutation cannot disagree about a file.
func hasEditableFuncBody(path string) bool {
	body, err := os.ReadFile(path) //nolint:gosec // a walk-produced path under the conductor's tree copy
	if err != nil {
		return false
	}
	return firstFuncBodyLine(strings.Split(string(body), "\n")) >= 0
}

// insertIntoFirstFuncBody inserts one statement as the first line of the first
// top-level function body in path.
func insertIntoFirstFuncBody(t *testing.T, path string) bool {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // a walk-produced path under the conductor's tree copy
	require.NoError(t, err)
	lines := strings.Split(string(body), "\n")
	at := firstFuncBodyLine(lines)
	if at < 0 {
		return false
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at+1]...)
	out = append(out, "\t_ = 0 // collectbench: mutated for the K-scaling run.")
	out = append(out, lines[at+1:]...)
	//nolint:gosec // same walk-produced path, written back inside the conductor's own tree copy
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600), "mutate %s", path)
	return true
}

// countOwnedBy returns how many of result's nodes and edges belong to the given
// tree-relative files. Edges are attributed through their endpoint node ids,
// which is how the collector addresses them.
func countOwnedBy(result *collectorwire.CollectResult, files []string) (nodes, edges int) {
	owned := make(map[string]struct{}, len(files))
	for _, f := range files {
		owned[f] = struct{}{}
	}
	ids := make(map[string]struct{})
	for _, n := range result.Nodes {
		if _, ok := owned[n.FilePath]; ok {
			nodes++
			ids[n.Id] = struct{}{}
		}
	}
	for _, e := range result.Edges {
		_, from := ids[e.FromID]
		_, to := ids[e.ToID]
		if from || to {
			edges++
		}
	}
	return nodes, edges
}

// runOneCollect performs exactly one collect through the REAL client path and
// returns what the client observed. sinkFor builds the sink, so a caller can
// hand it the ordinary armed sink or the bench's non-diff one.
//
// The two calls are the body of collector.Collect (pipeline.go): Lookup the
// registered "code" collector, run it, hand the CollectResult to the Sink. They
// are inlined only so the run can be timed in two phases and the uploaded counts
// read off the result.
func runOneCollect(
	t *testing.T, tree, graph string, k int,
	sinkFor func(knowledgev1connect.IngestServiceClient) collector.Sink,
) benchRunFacts {
	t.Helper()
	// repoManifest resolves its path from os.UserHomeDir, so pointing HOME at a
	// temp dir keeps the operator's real ~/.knowledge untouched by a bench run.
	t.Setenv("HOME", t.TempDir())

	mutated := mutateKFiles(t, tree, k)
	// DELETIONS ARE OPT-IN AND DEFAULT TO NONE, so every pre-existing run behaves
	// exactly as before. A run that removes files is the only way the finalize tail
	// does its expensive work — see collectbench_leasetail_test.go.
	deletedFromTree := deleteKFiles(t, tree, deleteKFromEnv(t))
	installSinkTimingTap(t, os.Getenv(envRunLabel), len(deletedFromTree))

	// STAMP THE OPERATION THE WAY THE SHIPPED ENTRY POINT DOES. The client's
	// operation interceptor REJECTS any covered RPC issued with no operation in
	// context, and the real collect stamps it once at the tool boundary —
	// tools/collect.go: WithOperation(rt.BaseContext(), OperationForTool(name)).
	//
	// A BARE CONTEXT DOES NOT FAIL LOUDLY HERE, WHICH IS EXACTLY WHY IT MUST BE
	// STAMPED: the manifest fetch errors, applyCollectDiff degrades to a full
	// collect (`collect diff: falling back to a full collect reason=handshake_error`),
	// and the run still succeeds — so the bench measures the DEGRADED lane while
	// reporting one manifest RPC, and every quiescence figure it produces is about
	// a code path the bench does not exist to measure.
	ctx := graphclient.WithOperation(context.Background(), graphclient.OpCollect)
	c, err := collector.Lookup("code")
	require.NoError(t, err, "the codesync collector must be registered")

	start := time.Now()
	result, err := c.Collect(ctx, tree, collector.CollectOptions{})
	require.NoError(t, err, "real codesync collect over %s", tree)
	chunkDur := time.Since(start)
	require.NotEmpty(t, result.Nodes, "a collect of this tree must produce nodes")

	// Pin the graph so the measurement does not follow the directory the tree
	// copy happens to sit in. See benchGraphName.
	require.NotEmpty(t, result.GraphName, "the collector must have named a graph")
	result.GraphName = graph

	totalNodes, totalEdges := len(result.Nodes), len(result.Edges)
	mutNodes, mutEdges := countOwnedBy(result, mutated)

	// EVERY RUN GOES THROUGH THE RECORDING CLIENT, both arms alike. It is a
	// pass-through that only counts and captures, so the arm under measurement is
	// still the shipped one; what it buys is the two wire observables no
	// server-side reading can supply — the manifest RPC count and the deletion
	// set the FinalizeRequest carried.
	// The hash inputs are captured BEFORE the upload, because WriteResult narrows
	// result in place: after it, the rows the diff DECLINED are gone, and those
	// are half of what a two-run comparison needs. See collectbench_diag_test.go.
	preDiffInputs := captureFileHashInputs(result.Nodes, result.Edges)

	rec := &recordingIngestClient{inner: benchIngestClient(benchServerURL(t))}
	uploadStart := time.Now()
	require.NoError(t, sinkFor(rec).WriteResult(ctx, c.Name(), result), "upload + finalize")
	uploadDur := time.Since(uploadStart)
	manifestRPCs, finalizes, deleted := rec.observed()
	recordDeletionSets(t, os.Getenv(envRunLabel), deletedFromTree, deleted)
	writeUploadDiag(t, os.Getenv(envRunLabel), preDiffInputs, result)

	// result.Nodes/Edges are NARROWED IN PLACE by the diff, so the counts read
	// AFTER WriteResult are what actually went on the wire — which is the number
	// this bench is about. The pre-upload totals were captured above.
	return benchRunFacts{
		RunLabel:      os.Getenv(envRunLabel),
		Graph:         result.GraphName,
		TreeRoot:      tree,
		WallTimeMS:    time.Since(start).Milliseconds(),
		ChunkMS:       chunkDur.Milliseconds(),
		UploadMS:      uploadDur.Milliseconds(),
		NodesUploaded: len(result.Nodes),
		EdgesUploaded: len(result.Edges),
		MutatedFiles:  len(mutated),
		MutatedNodes:  mutNodes,
		MutatedEdges:  mutEdges,
		TotalNodes:    totalNodes,
		TotalEdges:    totalEdges,
		// The upload set's file-owned half, counted by DISTINCT owning file: a
		// node with no file path is the fileless set, which ships on every
		// collect by design and is not what this figure is about.
		UploadedFileOwnedFiles: distinctOwningFiles(result),
		DeletedFilesOnFinalize: len(deleted),
		Finalizes:              finalizes,
		ManifestRPCs:           manifestRPCs,
	}
}

// distinctOwningFiles counts the DISTINCT files owning the nodes still in
// result after the diff narrowed it — the upload set's file-owned half.
func distinctOwningFiles(result *collectorwire.CollectResult) int {
	files := make(map[string]struct{})
	for _, n := range result.Nodes {
		if n.FilePath != "" {
			files[n.FilePath] = struct{}{}
		}
	}
	return len(files)
}

// benchIngestClient builds the real ingest client for a URL. NewGraphClientForURL
// is the client's own URL-shaped constructor — same h2c transport, same operation
// stamper, same reconnect interceptor as the shipped port-shaped path.
func benchIngestClient(url string) knowledgev1connect.IngestServiceClient {
	return graphclient.NewGraphClientForURL(url).IngestClient()
}

// benchServerURL reads the conductor's exported server address, failing loudly
// rather than skipping: a skip here would be a bench that reported success
// having measured nothing.
func benchServerURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(envServerURL)
	if u == "" {
		t.Fatalf("%s is unset — this harness is driven by scripts/collect-bench.sh, "+
			"which boots the collectbench-serve helper and exports its URL", envServerURL)
	}
	return u
}

// writeFacts persists a run's client-side record where the conductor reads it.
func writeFacts(t *testing.T, f benchRunFacts) {
	t.Helper()
	blob, err := json.MarshalIndent(f, "", "  ")
	require.NoError(t, err)
	t.Logf("collect bench measurement:\n%s", blob)
	out := os.Getenv(envOut)
	if out == "" {
		return
	}
	out = filepath.Clean(out)
	require.True(t, filepath.IsAbs(out), "%s must be an absolute path, got %q", envOut, out)
	require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600),
		"write measurement JSON the script assembles the artifact from")
}

// mutateKFromEnv reads the run's K, defaulting to a quiescent run.
func mutateKFromEnv(t *testing.T) int {
	t.Helper()
	raw := os.Getenv(envMutateK)
	if raw == "" {
		return 0
	}
	k, err := strconv.Atoi(raw)
	require.NoError(t, err, "%s must be an integer, got %q", envMutateK, raw)
	require.GreaterOrEqual(t, k, 0, "%s must not be negative", envMutateK)
	return k
}

// TestCollectBench_Run is the DRIVER the conductor invokes once per run. It is
// deliberately NOT one of the locked assertion names: it measures, and the
// assertion tests below compare. Splitting them is what lets the conductor
// sample psql BETWEEN collects, which a single long test could not allow.
func TestCollectBench_Run(t *testing.T) {
	facts := runOneCollect(t, benchTreeRoot(t), benchGraphFor(), mutateKFromEnv(t),
		func(c knowledgev1connect.IngestServiceClient) collector.Sink {
			// ARM A: the ordinary armed path — lever unset, so the resolver
			// returns diffModeOn.
			return remote.NewUploadSink(c)
		})
	writeFacts(t, facts)
}
