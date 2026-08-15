// SPDX-License-Identifier: Apache-2.0

// manage_operations.go — the manage tool's operation vocabulary, kept beside
// InterceptManage's dispatch rather than inside manage.go, which is already at
// the package's file-size budget.

package tools

import "slices"

// manageOperations is every operation a `manage` call may legitimately name:
// InterceptManage's own switch cases PLUS the four operations
// InterceptLogsManage claims later in the chain (list_logs, discard_logs,
// configure_log_backend, list_log_backends).
//
// Those four live here, away from the intercept that answers them, for the
// reason that makes this list necessary at all: InterceptManage runs FIRST, so
// its terminal unknown-operation arm has to recognize them as known and DECLINE
// them. A list of only its own cases would reject all four before their
// claimant ever saw the call.
//
// Sorted, and sized by construction (len(manageOperations)) — never by a
// hand-written numeral, which is the kind of claim that rots silently.
// TestInterceptManage_DeclaredOperationsAllKnown and
// TestUnknownOperationLists_MatchDeclaredSchemas keep it set-equal to the
// operation enum ManageToolDef() publishes.
var manageOperations = []string{
	"clear_llm_failures",
	"configure_log_backend",
	"delete_branch",
	"discard_logs",
	"drop_graph",
	"link",
	"list_branches",
	"list_log_backends",
	"list_logs",
	"pause_pipeline",
	"pipeline_status",
	"pprof_start",
	"pprof_stop",
	"promote_metadata",
	"prune",
	"prune-cache",
	"rebuild_cache",
	"rebuild_segments",
	"register_repo",
	"repair_edges",
	"resume_pipeline",
	"set_metadata_overrides",
	"status",
}

// manageOperationKnown reports whether op is a manage operation some arm of the
// chain claims. It is the gate InterceptManage's terminal arm consults before
// rejecting: a known operation it does not itself dispatch belongs downstream.
func manageOperationKnown(op string) bool {
	return slices.Contains(manageOperations, op)
}
