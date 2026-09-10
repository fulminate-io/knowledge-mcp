// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestDefaultDeny_SpecializedShapes asserts the deny contract for the
// SPECIALIZED set: Compile returns ok=false AND the dispatcher
// DENIES them with an explicit error naming the tool, NEVER exec (Execute) and
// with no legacy fallback wire (the deny flip removed the
// gc.Call fall-through).
//
// In production every one of these shapes is claimed by a client intercept
// (InterceptTopology / InterceptManage / InterceptThoughts / InterceptCollect /
// InterceptFileSymbols / the per-graph + query-rendering intercepts) BEFORE
// Dispatch runs, so they never actually reach this deny. The test calls Dispatch
// DIRECTLY to prove the floor contract: a shape that does reach the dispatcher and
// does not compile is denied legibly rather than silently forwarded to a deleted
// server handler.
func TestDefaultDeny_SpecializedShapes(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args string
	}{
		// Code-aware (the code-search and analyze-node client intercepts).
		{"search code", "search", `{"query":"x","graph":"code","repo":"r"}`},
		{"query code id", "query", `{"id":"x","graph":"code","repo":"r"}`},
		// Server-computation query modes.
		{"query stats", "query", `{"mode":"stats"}`},
		{"query examine", "query", `{"mode":"examine","id":"x"}`},
		{"query topology", "query", `{"mode":"topology","algorithm":"pagerank"}`},
		{"query pivot", "query", `{"mode":"pivot","rows":"a","cols":"b"}`},
		{"query correlations", "query", `{"mode":"correlations"}`},
		{"query timeline", "query", `{"mode":"timeline"}`},
		{"query explain", "query", `{"mode":"explain","id":"x"}`},
		{"query resolver", "query", `{"mode":"resolver"}`},
		{"query metadata_stats", "query", `{"mode":"metadata_stats"}`},
		{"query personality", "query", `{"mode":"personality"}`},
		{"query tensions", "query", `{"mode":"tensions"}`},
		{"query clusters", "query", `{"mode":"clusters"}`},
		{"query lineage", "query", `{"mode":"lineage","id":"x"}`},
		{"query evidence", "query", `{"mode":"evidence","id":"x"}`},
		{"query plan_tree", "query", `{"mode":"plan_tree","id":"x"}`},
		// NOTE: query(mode:modules) is NO LONGER here — the engine added the
		// RETURN_MODE_GRAPH_NAMES list-graphs read mode, so it compiles to Execute
		// (proven by TestCompileQuery_ModulesMode). It enumerates the graph
		// CATALOG of the target GraphType via the server-side list-graphs read.
		{"query file_symbols", "query", `{"mode":"file_symbols"}`},
		// Thought-graph filters (recall shape).
		{"query thought filter", "query", `{"valence_min":0.5}`},
		{"query session filter", "query", `{"session":"design"}`},
		// graph=logs (client-rendered).
		// NOTE: multi-type search, cloud resource_type search, and
		// include_edge_metadata traverse are NO LONGER here — they were made
		// reducible (they ride the node_types / resource_type /
		// include_edge_metadata carriers and compile to Execute; proven by the
		// tests TestCompileSearch_MultiTypeFilter,
		// TestCompileSearch_ResourceTypeFilter and
		// TestCompileTraverse_IncludeEdgeMetadata).
		// Cross-graph link_graph stays specialized (proxy creation, legacy).
		// NOTE: practice/checks mutate (link/create) are NO LONGER here —
		// the compileMutate guard was narrowed to link_graph-only, so an
		// intra-practice/checks op (no link_graph) Target-routes to a
		// MutationPlan (proven by TestCompileMutate_PracticeTransformers). The
		// tools-layer InterceptMutate routes the cross-graph proxy decision tree
		// (handleClientCrossGraphLink) BEFORE reaching the engine.
		{"mutate link_graph", "mutate", `{"operation":"link","link_graph":"linkage","from":"x","to":"y","relationship":"r"}`},
		// NOTE: mutate(bulk_update_metadata) is NO LONGER here — it was lowered
		// onto MUTATION_KIND_UPDATE_ITEMS (a metadata-only subset of update_batch's
		// per-item shape, all riding one Execute → one txn; the backend-tag reject is
		// preserved by the engine validateUpdateItems decode). Proven by
		// TestCompileMutate_BulkUpdateMetadata + the equivalence test.
		// NOTE: by-id update / delete / link / unlink are NO LONGER here — the
		// Selection.ids by-id WRITE selector was added, so they compile to Execute
		// (proven by TestCompileMutate_ByIDArmsReduce and TestCompileDelete_ByIDs).
		// NOTE: heterogeneous update_batch is NO LONGER here — the engine added the
		// MUTATION_KIND_UPDATE_ITEMS per-item arm, so it compiles to Execute
		// (proven by TestCompileMutate_UpdateBatch). A mutate(upsert) WITHOUT an id
		// still falls through (the upsert key is required), so it stays below.
		// answer stays specialized.
		{"mutate upsert (no id → legacy)", "mutate", `{"operation":"upsert","type":"worker","name":"w"}`},
		{"mutate answer", "mutate", `{"operation":"answer","id":"q","conclusion":"done"}`},
		// NOTE: thought/charge creates are NO LONGER here — the compileMutateCreate deny was removed,
		// the compileMutateCreate deny; a type:thought|charge create compiles to
		// MUTATION_KIND_CREATE (proven by TestCompileMutate_ThoughtChargeCreateCompiles
		// + the Dispatch-level bare-create no-summary-gate guards). The thoughts TOOL
		// (operation:think/charge) stays specialized below — it is the LLM-facing
		// surface the client composers claim, never reaching Compile.
		// thoughts tool (entirely specialized — unknown to Compile).
		{"thoughts recall", "thoughts", `{"operation":"recall","query":"x"}`},
		{"thoughts think", "thoughts", `{"operation":"think","content":"c"}`},
		{"thoughts charge", "thoughts", `{"operation":"charge","thought":"t","polarity":"positive"}`},
		{"thoughts trace", "thoughts", `{"operation":"trace","thought":"t"}`},
		// admin/server tools.
		{"collect", "collect", `{"type":"code","id":"/repo"}`},
		{"manage", "manage", `{"operation":"status"}`},
		{"file_symbols tool", "file_symbols", `{"file_path":"x.go"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// (1) Compile returns ok=false.
			req, ok := Compile(tc.tool, json.RawMessage(tc.args))
			assert.False(t, ok, "%s must Compile to ok=false (specialized)", tc.name)
			assert.Nil(t, req)

			// (2) Dispatch DENIES (no fallback wire), never exec (Execute).
			var execCalls int
			execFn := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
				execCalls++
				return nil, nil
			}
			out, err := Dispatch(context.Background(), execFn, nil, tc.tool, json.RawMessage(tc.args))
			require.NoError(t, err, "a deny is rendered as an error result, not returned")
			assert.Equal(t, 0, execCalls, "%s must NOT hit Execute", tc.name)
			assert.True(t, out.IsError, "%s must be DENIED (IsError) — no legacy fallback exists", tc.name)
			assert.Contains(t, out.Content[0].Text, tc.tool, "%s deny message names the tool", tc.name)
			assert.Contains(t, out.Content[0].Text, "denied", "%s deny message is legible", tc.name)
		})
	}
}

// specializedRawClientPackages are the directories the ticket Out-of-scope names
// as SPECIALIZED raw-client paths that MUST keep calling gc.Call directly (never
// route through engine.Dispatch). A package importing engine.Dispatch would mean
// it was accidentally rerouted.
//
// EVERY MEMBER MUST BE A PACKAGE THAT EXISTS. The guard greps a directory, and
// grep over a missing directory finds nothing — so a member naming a deleted
// package is a subtest that passes without measuring anything. The worker
// removal deleted the two packages this list used to carry alongside the four
// below, and they were dropped from here with them.
var specializedRawClientPackages = []string{
	"thought",
	"linker",
	"pipeline",
	"topology",
}

// TestDefaultDeny_SpecializedRawClientPackagesNotRerouted asserts the SPECIALIZED
// raw-client packages do NOT reference engine.Dispatch — they hold their own
// *GraphClient and call gc.Call directly, never through the compiling
// chokepoint. A grep-style guard against accidental rerouting (the ticket
// Out-of-scope contract). Skips when the source tree / git is unavailable.
func TestDefaultDeny_SpecializedRawClientPackagesNotRerouted(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("repo root unavailable: %v", err)
	}
	internalDir := filepath.Join(root, "cmd", "knowledge", "internal")
	if _, err := os.Stat(internalDir); err != nil {
		internalDir = filepath.Join(root, "internal")
	}
	for _, pkg := range specializedRawClientPackages {
		t.Run(pkg, func(t *testing.T) {
			pkgDir := filepath.Join(internalDir, pkg)
			hits, scanned := filesNaming(t, pkgDir, "engine.Dispatch")
			// KNOWN POSITIVE: the scan read a real package. Without it a wrong or
			// empty directory reports the same clean green as a correctly routed
			// one — which is exactly what the grep subprocess this replaced did,
			// since `grep -rl` over a missing directory finds nothing and exits
			// non-zero into a discarded error.
			require.NotZero(t, scanned,
				"the scan opened no Go file under %s, so this subtest measured nothing", pkgDir)
			assert.Empty(t, hits, "%s must NOT route through engine.Dispatch (raw-client gc.Call only); found in:\n%s",
				pkg, strings.Join(hits, "\n"))
		})
	}
}

// filesNaming reads every Go file under dir IN THIS PROCESS and returns the ones
// containing the literal, plus how many files it opened.
//
// IT REPLACES A `grep -rl` SUBPROCESS, and the replacement is the point rather
// than a style preference. `go test` keys a package's stored result on the files
// THE TEST PROCESS opened, and a child process's opens are never the test's — so
// nothing the grep read entered this package's cache key, INCLUDING the four
// sibling packages of this test's own module. Adding `engine.Dispatch` to
// internal/thought and re-running returned `ok (cached)`: a stored PASS for the
// guard whose whole subject is that reference. Reading the files here puts every
// one of them in the key, because they are inside this module.
//
// IT OPENS RATHER THAN WALKS WITH filepath.WalkDir for the same reason the
// fences elsewhere in this repository do: WalkDir lstats its root and opens
// nothing through a symlinked one. Nothing here is a symlink today, and the
// shape is kept uniform so a later reader does not have to work out which walk
// is safe.
func filesNaming(t *testing.T, dir, literal string) (hits []string, scanned int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "read %s", dir)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			subHits, subScanned := filesNaming(t, path, literal)
			hits = append(hits, subHits...)
			scanned += subScanned
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // a path under this module's own internal tree
		require.NoError(t, readErr, "read %s", path)
		scanned++
		if strings.Contains(string(body), literal) {
			hits = append(hits, path)
		}
	}
	return hits, scanned
}

// repoRoot resolves the git repo root so the dep guard anchors to absolute
// paths regardless of the test's working directory.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
