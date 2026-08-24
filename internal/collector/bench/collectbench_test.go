// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/codesync" // registers the "code" collector
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// The environment the conductor script exports for this harness.
//
// THE URL IS READ HERE AND IS NOT A KNOB ON THE SHIPPED BINARY, and the
// distinction is the whole reason this is stated. The standing rule is that the
// shipped client's server address is never user-overridable — no flag, no
// environment variable, no config file — and that override has been introduced
// and removed once already. Nothing here adds one back: this is build-tagged
// test code constructing its own client against an address the harness that
// started the server handed it, which is what every existing client integration
// test does.
const (
	// envServerURL is the base URL of the booted collectbench-serve helper.
	envServerURL = "KBENCH_SERVER_URL"

	// envOut, when set, names a file this harness writes its measurements to as
	// JSON. The script reads it back when assembling the artifact table.
	envOut = "KBENCH_OUT"

	// envRunLabel names the RUN this invocation is (RUN 1 is the clean-room
	// BEFORE baseline). Carried straight through into the JSON so the script
	// never has to infer which measurement it is holding.
	envRunLabel = "KBENCH_RUN_LABEL"
)

// benchGraphName is the FIXED graph name every bench run lands in, overriding
// the one the collector derived.
//
// THIS IS NOT COSMETIC. CodeCollector sets GraphName to filepath.Base(rootDir)
// (codesync/collector.go), and the harness derives rootDir from its own source
// file — so the graph key follows whatever directory this checkout happens to
// live in. Run from a git worktree, that is the worktree's directory name.
//
// Two things break if it is left alone. The graph key is the per-graph TABLE
// PREFIX, so it appears verbatim in the statement census the artifact commits —
// checking in an ephemeral directory name. Worse, step B extends that same
// artifact with AFTER columns and asserts row churn BETWEEN runs: a later run
// from a different directory would land in a different, EMPTY graph, making its
// first collect a first-landing rather than a re-collect, so every row it wrote
// would read as churn. That is a false verdict produced silently, with nothing
// in the output to reveal that the two runs were not comparing the same graph.
//
// Pinning the name costs nothing in fidelity: the graph key is an identifier,
// not an input to the work, and neither the collector nor the server does
// anything different because of its value.
const benchGraphName = "collectbench"

// benchMeasurement is what the client side is entitled to report. It is
// deliberately narrow: node_rows / edge_rows LANDED and the statement census are
// SERVER-side facts the script reads over psql, and a client-side guess at them
// would be a different number wearing the same name.
type benchMeasurement struct {
	RunLabel string `json:"run_label"`
	RepoRoot string `json:"repo_root"`
	// Graph is the pinned graph name the run landed in. Emitted so the artifact
	// can state which graph the server-side row counts were read from rather
	// than leaving the reader to infer it from a table prefix.
	Graph string `json:"graph"`
	// WallTimeMS is the end-to-end figure the artifact reports: collect plus
	// upload plus Finalize plus the finalize-tail wait, measured around the same
	// two calls collector.Collect makes.
	WallTimeMS int64 `json:"wall_time_ms"`
	// ChunkMS and UploadMS split WallTimeMS into the local chunking half and the
	// over-the-wire half, so a change in one is not read as a change in the other.
	ChunkMS       int64 `json:"chunk_ms"`
	UploadMS      int64 `json:"upload_ms"`
	NodesUploaded int   `json:"nodes_uploaded"`
	EdgesUploaded int   `json:"edges_uploaded"`
}

// repoRoot resolves THIS repository's root from the location of this source
// file, which is the one anchor that cannot drift with the caller's cwd or with
// whatever directory `go test` was invoked from. This file lives at
// cmd/knowledge/internal/collector/bench/, so the root is five levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must resolve this file's path")
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", ".."))
	require.NoError(t, err)
	// Known-positive control on the derivation: a wrong number of ".." would
	// still produce a real directory, and the collector would happily chunk it.
	// Anchoring on a file that exists ONLY at this repo's root is what makes a
	// mis-derived path fail loudly here instead of silently benchmarking the
	// wrong tree.
	require.FileExists(t, filepath.Join(root, "go.work"),
		"repo root derivation is wrong: %s has no go.work", root)
	return root
}

// TestCollectBench_FullBaseline runs ONE FULL collect of this repository through
// the real client path and records what a client can observe. Against
// pre-incremental main this IS the BEFORE baseline — the container twin of the
// production QI BEFORE capture — and the artifact labels it exactly that.
//
// THE PATH IS THE REAL ONE, NOT A RECONSTRUCTION. The two calls below are
// literally the body of collector.Collect (pipeline.go:26-41): Lookup the
// registered "code" collector, run it, hand the CollectResult to the Sink. They
// are inlined here rather than routed through collector.Collect only so the run
// can be timed in two phases and the uploaded counts read off the result; the
// collector, the chunker and the UploadSink are the shipped ones.
//
// WHAT IT DOES NOT OBSERVE, and why: the per-file contribution manifest. That
// RPC does not exist on landed main — it is Phase 2's — so RUN 1 uploads every
// file by construction. Step B, which arms the diff, is where the manifest
// responses become observable from this side.
func TestCollectBench_FullBaseline(t *testing.T) {
	serverURL := os.Getenv(envServerURL)
	if serverURL == "" {
		t.Fatalf("%s is unset — this harness is driven by scripts/collect-bench.sh, "+
			"which boots the collectbench-serve helper and exports its URL", envServerURL)
	}

	// CLIENT STATE follows the repoManifest idiom (the shape
	// cmd/knowledge/internal/tools/collect_manifest_test.go:17 uses; its
	// withTestManifest helper is unexported in package tools and is not callable
	// from here, so the reuse is the idiom, not the symbol). repoManifest resolves
	// its path from os.UserHomeDir, so pointing HOME at a temp dir is what keeps
	// the operator's real ~/.knowledge untouched by a bench run.
	t.Setenv("HOME", t.TempDir())

	root := repoRoot(t)
	ctx := context.Background()

	c, err := collector.Lookup("code")
	require.NoError(t, err, "the codesync collector must be registered")

	start := time.Now()
	result, err := c.Collect(ctx, root, collector.CollectOptions{})
	require.NoError(t, err, "real codesync collect over %s", root)
	chunkDur := time.Since(start)
	require.NotEmpty(t, result.Nodes, "a collect of this repository must produce nodes")

	// Pin the graph the run lands in, so the measurement does not follow the
	// directory this checkout sits in. See benchGraphName for why this is a
	// correctness fix for step B's between-run comparison and not a rename.
	require.NotEmpty(t, result.GraphName, "the collector must have named a graph")
	result.GraphName = benchGraphName

	// The real UploadSink over a real client. NewGraphClientForURL is the client's
	// own URL-shaped constructor — same h2c transport, same operation stamper,
	// same reconnect interceptor as the shipped port-shaped path.
	sink := remote.NewUploadSink(graphclient.NewGraphClientForURL(serverURL).IngestClient())
	uploadStart := time.Now()
	require.NoError(t, sink.WriteResult(ctx, c.Name(), result), "upload + finalize")
	uploadDur := time.Since(uploadStart)
	totalDur := time.Since(start)

	m := benchMeasurement{
		RunLabel:      os.Getenv(envRunLabel),
		RepoRoot:      root,
		Graph:         result.GraphName,
		WallTimeMS:    totalDur.Milliseconds(),
		ChunkMS:       chunkDur.Milliseconds(),
		UploadMS:      uploadDur.Milliseconds(),
		NodesUploaded: len(result.Nodes),
		EdgesUploaded: len(result.Edges),
	}
	blob, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	t.Logf("collect bench measurement:\n%s", blob)
	if out := os.Getenv(envOut); out != "" {
		// The path is required ABSOLUTE and is cleaned before use. A relative
		// value would resolve against `go test`'s own working directory — the
		// package directory, not the caller's — so the script would write the
		// artifact input somewhere it never looks and then assemble the table
		// from a stale file without noticing.
		out = filepath.Clean(out)
		require.True(t, filepath.IsAbs(out), "%s must be an absolute path, got %q", envOut, out)
		require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600),
			"write measurement JSON the script assembles the artifact from")
	}
}
