// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestDefaultDeny_SpecializedShapes asserts the T-GTB4 deny contract for the
// SPECIALIZED set (finding 457e861e): Compile returns ok=false AND the dispatcher
// DENIES them with an explicit error naming the tool, NEVER exec (Execute) and
// with no legacy fallback wire (decision 7fc2ff59 — the deny flip removed the
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
		// Code-aware (HandleSearchCode / HandleAnalyzeNode).
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
		// NOTE: query(mode:modules) is NO LONGER here — T-GTB1e added the
		// RETURN_MODE_GRAPH_NAMES list-graphs read mode, so it compiles to Execute
		// (proven by TestListGraphs_EnumeratesCatalog). It enumerates the graph
		// CATALOG of the target GraphType via the server-side list-graphs read.
		{"query file_symbols", "query", `{"mode":"file_symbols"}`},
		// Thought-graph filters (recall shape).
		{"query thought filter", "query", `{"valence_min":0.5}`},
		{"query session filter", "query", `{"session":"design"}`},
		// graph=logs (client-rendered).
		{"query logs", "query", `{"graph":"logs","name":"q1","text":"err"}`},
		{"traverse logs", "traverse", `{"start":"n1","graph":"logs","name":"q1"}`},
		// NOTE: multi-type search, cloud resource_type search, and
		// include_edge_metadata traverse are NO LONGER here — T2.4c made them
		// reducible (they ride the node_types / resource_type /
		// include_edge_metadata carriers and compile to Execute; proven by the
		// equivalence tests TestEquivalence_SearchMultiType / SearchResourceType /
		// TraverseEdgeMetadata).
		// Cross-graph link_graph stays specialized (proxy creation, T-GTB5/legacy).
		// NOTE: practice/transformers mutate (link/create) are NO LONGER here —
		// T-GTB6 Phase 1 narrowed the compileMutate guard to link_graph-only, so an
		// intra-practice/transformers op (no link_graph) Target-routes to a
		// MutationPlan (proven by TestCompileMutate_PracticeTransformers). The
		// tools-layer InterceptMutate routes the cross-graph proxy decision tree
		// (handleClientCrossGraphLink) BEFORE reaching the engine.
		{"mutate link_graph", "mutate", `{"operation":"link","link_graph":"linkage","from":"x","to":"y","relationship":"r"}`},
		// NOTE: mutate(bulk_update_metadata) is NO LONGER here — T-GTB1e lowered it
		// onto MUTATION_KIND_UPDATE_ITEMS (a metadata-only subset of update_batch's
		// per-item shape, all riding one Execute → one txn; the backend-tag reject is
		// preserved by the engine validateUpdateItems decode). Proven by
		// TestCompileMutate_BulkUpdateMetadata + the equivalence test.
		// NOTE: by-id update / delete / link / unlink are NO LONGER here — T2.4c
		// added the Selection.ids by-id WRITE selector (closed TICKET-GAP
		// b535d1a9), so they compile to Execute (proven by TestEquivalence_MutateUpdate /
		// MutateDeleteByIDs / MutateLinkByID / MutateUnlinkByID).
		// NOTE: heterogeneous update_batch is NO LONGER here — T-GTB1c added the
		// MUTATION_KIND_UPDATE_ITEMS per-item arm, so it compiles to Execute
		// (proven by TestCompileMutate_UpdateBatch). A mutate(upsert) WITHOUT an id
		// still falls through (the upsert key is required), so it stays below.
		// answer stays specialized.
		{"mutate upsert (no id → legacy)", "mutate", `{"operation":"upsert","type":"worker","name":"w"}`},
		{"mutate answer", "mutate", `{"operation":"answer","id":"q","conclusion":"done"}`},
		// NOTE: thought/charge creates are NO LONGER here — T-GTB6 Phase 7 removed
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
			out, err := Dispatch(context.Background(), execFn, tc.tool, json.RawMessage(tc.args))
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
// NOTE: cmd/knowledge/internal/dream was REMOVED from this list by T-GTB6 D8
// (decision 620137ea): the dream worker's eino tool dispatch now intentionally
// rides the STANDARD client path (intercept chain → engine.Dispatch, composed by
// bootstrap/dream.go's dispatchForRunner and injected as a dream.DispatchFunc) —
// the worker shares the one client dispatch path, with no bespoke raw t.client.Call
// fall-through. So dream is no longer a raw-client-only package; this guard no
// longer applies to it.
var specializedRawClientPackages = []string{
	"cmd/knowledge/internal/thought",
	"cmd/knowledge/internal/workercrud",
	"cmd/knowledge/internal/linker",
	"cmd/knowledge/internal/prune",
	"cmd/knowledge/internal/pipeline",
	"cmd/knowledge/internal/topology",
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
	for _, pkg := range specializedRawClientPackages {
		t.Run(pkg, func(t *testing.T) {
			// grep for an engine.Dispatch reference under the package dir.
			out, _ := exec.Command("grep", "-rl", "engine.Dispatch", root+"/"+pkg).CombinedOutput() //nolint:gosec // fixed args, test-only.
			hits := strings.TrimSpace(string(out))
			assert.Empty(t, hits, "%s must NOT route through engine.Dispatch (raw-client gc.Call only); found in:\n%s", pkg, hits)
		})
	}
}

// repoRoot resolves the git repo root so the grep guard anchors to absolute
// paths regardless of the test's working directory.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
