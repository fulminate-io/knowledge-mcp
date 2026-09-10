// SPDX-License-Identifier: Apache-2.0

// manage_operations.go — the manage tool's operation vocabulary, kept beside
// InterceptManage's dispatch rather than inside manage.go, which is already at
// the package's file-size budget.

package tools

import "slices"

// manageOperations is every operation a `manage` call may legitimately name.
//
// IT USED TO CARRY FOUR OPERATIONS NO SWITCH CASE HERE ANSWERED — list_logs,
// discard_logs, configure_log_backend and list_log_backends, claimed by a logs
// intercept further down the chain, which InterceptManage had to recognize as
// known and DECLINE rather than reject. That intercept is gone with the built-in
// log collectors, so the list is again exactly InterceptManage's own cases, and
// a name it does not answer is a name nothing answers.
//
// Sorted, and sized by construction (len(manageOperations)) — never by a
// hand-written numeral, which is the kind of claim that rots silently.
// TestInterceptManage_DeclaredOperationsAllKnown and
// TestUnknownOperationLists_MatchDeclaredSchemas keep it set-equal to the
// operation enum ManageToolDef() publishes.
var manageOperations = []string{
	"clear_llm_failures",
	"delete_branch",
	"drop_graph",
	"import_style_rules",
	"link",
	"list_branches",
	"migrate_embed_identity",
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
