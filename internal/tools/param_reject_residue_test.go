// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// param_reject_residue_test.go drives every residue tool's intercept with a
// payload carrying exactly one key the tool's schema does not declare, and
// asserts the call is REFUSED with an error naming that key.
//
// THE DEFECT IT REPRODUCES. Each intercept decodes the caller's payload into its
// own args struct with a plain json.Unmarshal, which discards any key the struct
// has no field for. A caller who mistyped a param, or supplied one belonging to a
// sibling tool, gets a SUCCESS with that param silently gone — a call that did
// less than it was asked to, reported as if it had done all of it.
//
// THE EMPTY-PAYLOAD FENCE IS NOT PART OF THAT RED. An absent or empty payload is
// a legal, documented call shape across this surface — help() with no topic is
// the documented way to list topics, and both the propagate handler and the hive
// intercept explicitly guard on a non-empty payload. empty_arguments_accepted
// passes BEFORE and AFTER the sweep; its job is to catch a rejection wired so
// broadly it starts refusing legal no-arg calls.

// undeclaredProbeKey is the single undeclared key every tool subtest supplies.
// One distinctive spelling, so a copy-paste slip between rows is visible in the
// failure message rather than silently asserting the wrong thing.
const undeclaredProbeKey = "zzz_undeclared_probe"

// interceptFn is the shape every client intercept shares.
type interceptFn func(context.Context, ClientDeps, kgtools.CallToolParams) (bool, kgtools.ToolResult)

// residueRejectCase is one tool's row: the intercept to drive, the tool name to
// drive it under, and a minimal payload that clears the tool-name gate.
//
// The valid half of each payload only has to get PAST the name gate — it does
// not have to succeed, because the rejection fires before any validation or
// backend call. Payloads are deliberately chosen to do no real work if the
// rejection is absent (the red case): no collector runs, no filesystem walk.
type residueRejectCase struct {
	tool    string
	name    string // params.Name to drive under; defaults to tool when empty
	fn      interceptFn
	payload map[string]any
}

func residueRejectTable() []residueRejectCase {
	return []residueRejectCase{
		{tool: "analyze_usage", fn: InterceptAnalyzeUsage, payload: map[string]any{}},
		{tool: "assemble", fn: InterceptAssemble, payload: map[string]any{"id": "n1"}},
		// list_node_kinds is the one ast operation that reads no source tree.
		{tool: "ast", fn: InterceptAst, payload: map[string]any{"operation": "list_node_kinds", "language": "go"}},
		// No type => the intercept errors before any collector is constructed.
		{tool: "collect", fn: InterceptCollect, payload: map[string]any{}},
		{tool: "custom_collector", fn: InterceptGraphType, payload: map[string]any{"operation": "list"}},
		{tool: "delete", fn: InterceptDeleteGuard, payload: map[string]any{"ids": []string{"a"}}},
		{tool: "file_symbols", fn: InterceptFileSymbols, payload: map[string]any{"file_path": "a.go"}},
		{tool: "help", fn: InterceptHelp, payload: map[string]any{"topic": "query"}},
		// hive spells its operation selector `op`, not `operation`.
		{tool: "hive", fn: InterceptHive, payload: map[string]any{"op": "status"}},
		// register_repo is purely client-side and errors on the missing name/root,
		// so it clears the name gate without touching the daemon.
		{tool: "manage", fn: InterceptManage, payload: map[string]any{"operation": "register_repo"}},
		{tool: "query", fn: InterceptQueryStats, payload: map[string]any{"mode": "stats"}},
		{tool: "record_decision", fn: InterceptRecordDecision, payload: map[string]any{"name": "d"}},
		{tool: "search", fn: InterceptSearch, payload: map[string]any{"query": "x", "graph": "knowledge"}},
		{tool: "sync", fn: InterceptSync, payload: map[string]any{"operation": "status"}},
		{tool: "thoughts", fn: InterceptThoughts, payload: map[string]any{"operation": "propagate"}},
		{tool: "traverse", fn: InterceptLogsTraversal, payload: map[string]any{"start": "a"}},
		{tool: "worker", fn: InterceptWorker, payload: map[string]any{"operation": "list"}},
		// NOT an eighteenth tool: a SECOND entry point on the mutate tool. It
		// gates on params.Name == "mutate" and fires ahead of the main mutate
		// intercept, so the main intercept's accounting does not cover it.
		{
			tool: "mutate_add_criterion", name: "mutate", fn: InterceptAddCriterion,
			payload: map[string]any{"operation": "create", "type": "criterion", "step_id": "s1", "description": "d", "command": "c"},
		},
	}
}

// TestResidueTools_RejectUndeclaredTopLevelParam is the per-tool reproduction
// plus the empty-payload fence.
func TestResidueTools_RejectUndeclaredTopLevelParam(t *testing.T) {
	for _, tc := range residueRejectTable() {
		t.Run(tc.tool, func(t *testing.T) {
			name := tc.name
			if name == "" {
				name = tc.tool
			}

			payload := map[string]any{}
			maps.Copy(payload, tc.payload)
			payload[undeclaredProbeKey] = "x"

			handled, res := tc.fn(opCtx(), rejectProbeDeps(), kgtools.CallToolParams{
				Name:      name,
				Arguments: mustMarshal(t, payload),
			})

			require.Truef(t, handled,
				"%s must CLAIM a call carrying an undeclared param — falling through routes the typo to a surface that cannot report it", tc.tool)
			assert.Truef(t, res.IsError, "%s must refuse a call carrying an undeclared param", tc.tool)
			assert.Containsf(t, resultText(res), undeclaredProbeKey,
				"%s must NAME the offending key so the caller can fix the call; got: %s", tc.tool, resultText(res))

			if tc.tool == "file_symbols" {
				assertFileSymbolsOwnsItsRejection(t, resultText(res))
			}

			if tc.tool == "delete" {
				// The guard's whole contract is to fall through when there is
				// nothing to reject. A guard that claimed every delete call
				// would swallow the real delete before it reached the
				// dispatcher — a failure the assertions above cannot see.
				// `format` must be a DECLARED delete param for this to hold.
				clean, _ := InterceptDeleteGuard(opCtx(), rejectProbeDeps(), kgtools.CallToolParams{
					Name:      "delete",
					Arguments: mustMarshal(t, map[string]any{"ids": []string{"a"}, "format": "json"}),
				})
				assert.False(t, clean,
					"a clean delete payload must fall THROUGH to the dispatcher — a guard that claims it swallows the delete")
			}
		})
	}

	// CHARACTERIZATION GUARD, green before and after the sweep.
	t.Run("empty_arguments_accepted", func(t *testing.T) {
		cases := []struct {
			label string
			name  string
			fn    interceptFn
			args  json.RawMessage
		}{
			{label: "help_nil", name: "help", fn: InterceptHelp},
			{label: "thoughts_nil", name: "thoughts", fn: InterceptThoughts},
			{label: "thoughts_propagate", name: "thoughts", fn: InterceptThoughts,
				args: json.RawMessage(`{"operation":"propagate"}`)},
			{label: "hive_nil", name: "hive", fn: InterceptHive},
		}
		for _, c := range cases {
			_, res := c.fn(opCtx(), rejectProbeDeps(), kgtools.CallToolParams{Name: c.name, Arguments: c.args})
			assert.NotContainsf(t, resultText(res), "unknown parameter",
				"%s: a legal no-arg call must not be refused as carrying an unknown parameter; got: %s",
				c.label, resultText(res))
		}
	})
}

// assertFileSymbolsOwnsItsRejection is the file_symbols subtest's extra half:
// the standalone tool must account against ITS OWN schema, not against query's.
//
// WHY IT NEEDS ITS OWN ASSERTIONS. InterceptFileSymbols claims the standalone
// file_symbols tool AND query(mode:"file_symbols"), and accounts for both against
// the QUERY schema. That is wrong twice over, and the generic undeclared-key
// assertion above can see neither failure:
//
//   - ATTRIBUTION. The refusal a file_symbols caller reads names "query" and
//     enumerates query's params. Everything actionable in the message points at
//     a tool the caller did not call.
//   - COVERAGE. Sweeping against query's schema ACCEPTS every key query declares
//     and file_symbols does not — a standalone call carrying one is served with
//     the param silently dropped. An invented probe key cannot reach that hole,
//     because it is undeclared on both schemas.
//
// The query-mode control is the other half: the same intercept must keep serving
// query(mode:"file_symbols", path_prefix:...), where path_prefix is legitimate.
// It fences a fix applied to the whole intercept instead of the standalone arm.
func assertFileSymbolsOwnsItsRejection(t *testing.T, got string) {
	t.Helper()

	assert.Containsf(t, got, "file_symbols: unknown parameter",
		"the refusal must name file_symbols, the tool the caller actually called; got: %s", got)
	assert.Containsf(t, got, validTopLevelParams(FileSymbolsToolDef().InputSchema.Properties),
		"the refusal must enumerate file_symbols' OWN valid params, not another tool's; got: %s", got)

	// A key query declares and file_symbols does not. Undeclared HERE, so the
	// standalone tool must refuse it rather than silently drop it.
	handled, res := InterceptFileSymbols(opCtx(), rejectProbeDeps(), kgtools.CallToolParams{
		Name:      "file_symbols",
		Arguments: mustMarshal(t, map[string]any{"file_path": "a.go", "path_prefix": "zzz"}),
	})
	assert.True(t, handled, "the standalone file_symbols tool must claim its own call")
	assert.Truef(t, res.IsError,
		"file_symbols must refuse `path_prefix` — it is declared by query, NOT by file_symbols, and this path never reads it; got: %s",
		resultText(res))

	// KNOWN-POSITIVE CONTROL: the same key on the QUERY path is legitimate and
	// must still be served. Without this, a fix that rejected path_prefix for
	// BOTH tools would look correct here while breaking query(mode:file_symbols).
	_, qres := InterceptFileSymbols(opCtx(), rejectProbeDeps(), kgtools.CallToolParams{
		Name:      "query",
		Arguments: mustMarshal(t, map[string]any{"mode": "file_symbols", "path_prefix": "a.go"}),
	})
	assert.NotContains(t, resultText(qres), "unknown parameter",
		"query(mode:file_symbols) must still accept path_prefix — query declares it and the query arm consults it")
}

// rejectProbeDeps is the fixture every subtest drives. It carries a live
// GraphCaller because three of these intercepts consult deps.GraphCaller()
// before they decode; with a nil one they would return a graph-client error and
// the subtest would be asserting the fixture's shape rather than the rejection.
func rejectProbeDeps() ClientDeps { return interceptTestDeps{gc: &fakeGraphCaller{}} }
