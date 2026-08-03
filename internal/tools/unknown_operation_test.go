// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// unknownOperationCase drives one operation-dispatched tool's entry point with a
// bogus operation.
type unknownOperationCase struct {
	tool   string
	invoke func(t *testing.T, op string) (bool, kgtools.ToolResult)
}

// unknownOperationCases enumerates every operation-dispatched tool. Two shapes
// differ from the rest and are called out so a reader does not mistake them for
// mistakes: hive reads its operation from the `op` key rather than `operation`,
// and mutate is rejected by a head-of-function guard rather than a switch
// default (the guard runs before any graph routing, so an unknown operation
// cannot slip past on a foreign graph).
func unknownOperationCases() []unknownOperationCase {
	call := func(name, op string) kgtools.CallToolParams {
		return kgtools.CallToolParams{
			Name:      name,
			Arguments: json.RawMessage(`{"operation":"` + op + `"}`),
		}
	}
	return []unknownOperationCase{
		{"analyze_usage", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptAnalyzeUsage(opCtx(),
				analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{}}, call("analyze_usage", op))
		}},
		{"ast", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptAst(opCtx(), interceptTestDeps{}, call("ast", op))
		}},
		{"custom_collector", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptGraphType(opCtx(), interceptTestDeps{}, call("custom_collector", op))
		}},
		{"hive", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptHive(opCtx(), interceptTestDeps{gc: &fakeHiveCaller{}}, kgtools.CallToolParams{
				Name:      "hive",
				Arguments: json.RawMessage(`{"op":"` + op + `","hive":"h1"}`),
			})
		}},
		{"manage", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptManage(opCtx(), interceptTestDeps{}, call("manage", op))
		}},
		{"mutate", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptMutate(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}}, call("mutate", op))
		}},
		{"sync", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptSync(opCtx(), interceptTestDeps{}, call("sync", op))
		}},
		{"thoughts", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptThoughts(opCtx(), interceptTestDeps{}, call("thoughts", op))
		}},
		{"worker", func(t *testing.T, op string) (bool, kgtools.ToolResult) {
			t.Helper()
			return InterceptWorker(opCtx(), workerTestDeps{}, call("worker", op))
		}},
	}
}

// TestUnknownOperation_CanonicalMessage pins that every operation-dispatched
// tool answers a bogus operation itself, with one shared diagnostic naming the
// tool and quoting the operation — never the engine's tool-level deny, which
// would claim the TOOL has no client intercept when in truth every one of these
// has one and it is the OPERATION that is unknown.
func TestUnknownOperation_CanonicalMessage(t *testing.T) {
	const bogus = "definitely-not-an-op"

	covered := make([]string, 0, len(unknownOperationCases()))
	for _, c := range unknownOperationCases() {
		covered = append(covered, c.tool)
		t.Run(c.tool, func(t *testing.T) {
			handled, res := c.invoke(t, bogus)
			require.Truef(t, handled, "%s must claim an unknown operation, not defer it", c.tool)
			require.Truef(t, res.IsError, "%s must answer with an error: %s", c.tool, toolResultText(res))
			assert.Containsf(t, toolResultText(res),
				c.tool+`: unknown operation "`+bogus+`" — valid operations: `,
				"%s must emit the canonical diagnostic", c.tool)
		})
	}

	// COMPLETENESS GUARD, enumerated not counted: a length check is green for
	// any nine cases at all, including nine copies of one tool. Adding a tenth
	// operation-dispatched tool means declaring it HERE.
	slices.Sort(covered)
	assert.Equal(t, []string{
		"analyze_usage", "ast", "custom_collector", "hive", "manage",
		"mutate", "sync", "thoughts", "worker",
	}, covered, "the canonical-message table must cover exactly the operation-dispatched tools")
}

// TestUnknownOperationLists_MatchDeclaredSchemas is the schema-parity contract
// for the three arms whose valid-operations list is HAND-COPIED. The drift that
// matters is a SILENT over-rejection: an operation the tool still advertises,
// omitted from the list, now dies at a terminal arm instead of routing.
//
// mutate has no row: its list IS the schema read (mutateDeclaredOperations), so
// there are not two things that can disagree.
//
// The other five tools (analyze_usage, custom_collector, hive, sync, worker)
// declare no operation Enum anywhere in this package, so their lists are
// hand-written by necessity and have nothing to be checked against. That is not
// an oversight — there is no second source to compare them to.
func TestUnknownOperationLists_MatchDeclaredSchemas(t *testing.T) {
	rows := []struct {
		tool    string
		literal []string
		enum    []string
	}{
		{"manage", manageOperations, ManageToolDef().InputSchema.Properties["operation"].Enum},
		{"thoughts", thoughtsOperations, ThoughtsToolDef().InputSchema.Properties["operation"].Enum},
		{"ast", []string{"match", "count", "replace", "explain", "list_node_kinds"},
			AstToolDef().InputSchema.Properties["operation"].Enum},
	}
	for _, r := range rows {
		t.Run(r.tool, func(t *testing.T) {
			require.NotEmptyf(t, r.enum, "%s must publish its operation enum", r.tool)
			gotLiteral := slices.Clone(r.literal)
			gotEnum := slices.Clone(r.enum)
			slices.Sort(gotLiteral)
			slices.Sort(gotEnum)
			assert.Equalf(t, gotEnum, gotLiteral,
				"%s's valid-operations list and its declared schema enum have drifted", r.tool)
		})
	}
}

// TestInterceptThoughts_DeclaredOperationsStillRoute is the over-rejection
// catcher for thoughts, the analog of the mutate step's. Every operation the
// schema advertises must still route rather than hit the new terminal arm.
//
// Driving the real handlers is fixture-safe here (unlike manage, whose handlers
// have process- and disk-level side effects): they route through the fake
// GraphCaller. A handler that rejects a minimal payload on its own merits still
// satisfies the assertion — all this pins is that the operation was not
// answered with the unknown-operation diagnostic.
func TestInterceptThoughts_DeclaredOperationsStillRoute(t *testing.T) {
	enum := ThoughtsToolDef().InputSchema.Properties["operation"].Enum
	require.NotEmpty(t, enum, "the thoughts schema must publish its operation enum")

	for _, op := range enum {
		t.Run(op, func(t *testing.T) {
			deps := ctxPackDeps{gc: seededAdjFixture()}
			_, res := callAdjacency(t, deps, map[string]any{
				"operation":   op,
				"scope":       "all",
				"thought":     "tA",
				"thought_ids": []string{"tA"},
				"summary":     "s",
				"content":     "c",
				"polarity":    "positive",
				"weight":      5,
				"reasoning":   "r",
			})
			assert.NotContainsf(t, toolResultText(res), "unknown operation",
				"%q is declared by the thoughts schema — the terminal arm must not reject it", op)
		})
	}
}
