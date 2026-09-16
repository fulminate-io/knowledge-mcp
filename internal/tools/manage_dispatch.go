// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
)

// manage_dispatch.go holds the arms and prelude InterceptManage delegates to,
// lifted out of manage.go so that file's dispatch table can grow an operation
// without the file's own length becoming what blocks it.

// decodeManageCall is InterceptManage's PRELUDE: it claims the call, rejects
// params the published schema does not declare, and decodes the arguments.
//
// THE THREE RETURNS ARE THREE OUTCOMES, not a bool plus a payload. claimed=false
// means this intercept is not the one — either the tool is not manage, or the
// arguments do not decode as manage's — and the call passes to the next claimant
// unchanged. A non-nil refusal means the call IS manage's and was rejected
// before any handler ran; rejectUndeclaredParams runs HERE, ahead of dispatch,
// which is why an operation whose params are missing from the schema is
// unreachable in production however well its handler works.
//
// It is split out of InterceptManage so the dispatch table below it can grow an
// arm without the enclosing function's statement count becoming what blocks it.
func decodeManageCall(params kgtools.CallToolParams) (a manageArgs, claimed bool, refusal *kgtools.ToolResult) {
	if params.Name != "manage" {
		return manageArgs{}, false, nil
	}
	if err := rejectUndeclaredParams("manage", "", ManageToolDef().InputSchema.Properties, params.Arguments); err != nil {
		res := errorResult(err.Error())
		return manageArgs{}, true, &res
	}
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return manageArgs{}, false, nil
	}
	return a, true, nil
}

// handlePprofStart begins a CPU profile of the client process and lazily
// brings up the loopback pprof endpoint so the result is fetchable.
func handlePprofStart() kgtools.ToolResult {
	addr, err := profiling.StartCPU()
	if err != nil {
		return errorResult("pprof_start: " + err.Error())
	}
	return textResult(fmt.Sprintf(
		"CPU profile started. Reproduce the slow operation now, then call manage(operation:\"pprof_stop\"). pprof endpoint: http://%s/debug/pprof/",
		addr))
}

// handlePprofStop stops the CPU profile and reports where to pull it.
func handlePprofStop() kgtools.ToolResult {
	url, size, err := profiling.StopCPU()
	if err != nil {
		return errorResult("pprof_stop: " + err.Error())
	}
	return textResult(fmt.Sprintf(
		"CPU profile stopped (%d bytes). Fetch + open it:\n  go tool pprof %s\nor save a copy:\n  curl -s %s -o cpu.pprof",
		size, url, url))
}

// handlePipelineLifecycleManage serves the three pipeline circuit-breaker
// operations, which share one seam and differ only in which method they call.
//
// THEY ARE ONE ARM RATHER THAN THREE CASES because InterceptManage's dispatch is
// bounded by a statement count, and three near-identical cases spend that budget
// on a distinction the reader does not need: pause, resume and status are the
// three faces of one latch. Grouping them keeps the routing visible in the
// switch while the latch's own vocabulary stays here.
func handlePipelineLifecycleManage(deps ClientDeps, a manageArgs) kgtools.ToolResult {
	switch a.Operation {
	case "pause_pipeline":
		return handlePausePipeline(deps, a)
	case "resume_pipeline":
		return handleResumePipeline(deps)
	default:
		return handlePipelineStatus(deps, a.Format)
	}
}

// handlePprofManage serves the two profiling operations, which share one latch
// and take no parameters at all.
//
// THEY ARE ONE ARM RATHER THAN TWO CASES for the reason stated on
// handlePipelineLifecycleManage below: InterceptManage's dispatch is bounded by a
// statement count, and start and stop are the two ends of one profile. Grouping
// them keeps the routing visible in the switch and buys the budget a new
// operation needs.
func handlePprofManage(operation string) kgtools.ToolResult {
	if operation == "pprof_start" {
		return handlePprofStart()
	}
	return handlePprofStop()
}

// handleBranchOverlayManage serves the two branch-overlay operations, grouped on
// the same argument as handlePprofManage: they are the list and the delete of one
// thing, and the switch spends its budget better on the distinction between
// families than on the distinction between these two.
func handleBranchOverlayManage(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	if a.Operation == "delete_branch" {
		return handleClientDeleteBranch(ctx, deps, a)
	}
	return handleClientListBranches(ctx, deps, a)
}
