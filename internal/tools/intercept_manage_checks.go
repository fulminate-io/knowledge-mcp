// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptManageChecks claims the manage_checks MCP call. The
// server has no manage_checks handler; this is the only path that answers it.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// manageChecksArgs mirrors ManageChecksToolDef()'s declared params. EVERY
// declared param has a field here and every field is declared: a param the
// schema publishes but no field reads would be accepted and then silently
// dropped, which is the exact defect the create_* family's schema/args parity
// guard exists to close.
type manageChecksArgs struct {
	Operation string `json:"operation"`

	Language string `json:"language,omitempty"`
	Repo     string `json:"repo,omitempty"`

	IDs        []string `json:"ids,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
	TopK       int      `json:"top_k,omitempty"`
	// IncludeTests is a POINTER because the run knob has three states, not two:
	// omitted, explicitly true, explicitly false. An omitted flag is legal for
	// every language while an explicit one is refused for a language with no
	// test-file convention, and a plain bool cannot tell those apart.
	IncludeTests *bool `json:"include_tests,omitempty"`

	Name        string `json:"name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
	Severity    string `json:"severity,omitempty"`
	CheckType   string `json:"check_type,omitempty"`
	DSLPattern  string `json:"dsl_pattern,omitempty"`
	CheckWhere  string `json:"check_where,omitempty"`
	// AppliesToTests needs no third state: on the create path an omitted flag
	// and an explicit false both mean "write no declaration", which is what the
	// check contract reads as false.
	AppliesToTests bool `json:"applies_to_tests,omitempty"`

	FixtureBad  *manageChecksFixtureArgs `json:"fixture_bad,omitempty"`
	FixtureGood *manageChecksFixtureArgs `json:"fixture_good,omitempty"`

	Format string `json:"format,omitempty"`
}

// manageChecksFixtureArgs is one fixture example node as the caller supplies it.
type manageChecksFixtureArgs struct {
	Name        string `json:"name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
}

// InterceptManageChecks handles the manage_checks MCP call.
func InterceptManageChecks(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "manage_checks" {
		return false, kgtools.ToolResult{}
	}
	var a manageChecksArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("manage_checks: invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	// Ahead of every validation and every write: the decode above discards any
	// top-level key manageChecksArgs has no field for, so an undeclared param
	// would otherwise vanish into a successful call.
	if err := rejectUndeclaredParams("manage_checks", "", ManageChecksToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if !manageChecksOperationKnown(a.Operation) {
		return true, errorResult(unknownManageChecksOperation(a.Operation))
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("manage_checks: graph caller unavailable")
	}
	switch a.Operation {
	case OpChecksCreate:
		return true, manageChecksCreate(ctx, gc, a)
	case OpChecksList:
		return true, manageChecksList(ctx, gc, a)
	case OpChecksRun:
		return true, manageChecksRun(ctx, deps, gc, a)
	}
	// Unreachable while the vocabulary and this switch agree — the guard above
	// admits only the three operations dispatched here. It errors LOUDLY rather
	// than falling through silently, so a term added to manageChecksOperations
	// without an arm surfaces as a named contradiction instead of a tool that
	// advertises an operation and answers nothing. The same posture the check
	// runner takes for its own unlanded executor seam.
	return true, errorResult(fmt.Sprintf(
		"manage_checks: %q is an admitted operation with no handler — the operation vocabulary and the dispatch have drifted",
		a.Operation))
}

// unknownManageChecksOperation is the bad-input refusal, naming the offending
// value AND enumerating the admitted vocabulary rendered from
// manageChecksOperations at call time — never a second hand-written list.
func unknownManageChecksOperation(op string) string {
	return fmt.Sprintf("manage_checks: %q is not an admitted operation (admitted: %s)",
		op, strings.Join(manageChecksOperations, ", "))
}
