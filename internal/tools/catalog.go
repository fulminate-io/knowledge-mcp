// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// falseValue backs the *bool needed to render additionalProperties:false on a
// closed object schema. kgtools.Property.AdditionalProperties is a *bool (nil =
// open/omitted, &false = strict-closed); a *bool can't be expressed as an inline
// composite-literal address, so closed shapes across the schema files take
// AdditionalProperties: &falseValue. Genuinely-arbitrary maps leave it nil.
var falseValue = false

// AllToolSchemas returns the complete client-owned MCP tool catalog.
//
// The MCP tool catalog is client-owned: the client is the single source of truth
// for the tools/list surface. Both loadSchemas (the tools/list responder) and the
// dream worker's BuildAllowedTools compose from this one builder so the advertised
// surface and the worker allowlist can never drift apart.
//
// The catalog is a static set of 21 pure schema literals; building it is
// constant-time and never fails, so callers can treat it as a plain value.
//
// Grouping mirrors the historical composition:
//   - generic primitives (query/traverse/mutate/delete/manage)
//   - sync (the bidirectional Fulminate Cloud sync tool)
//   - first-class tools (thoughts/search/file_symbols/collect)
//   - the remaining client-owned tools (worker/ast/help/record_decision +
//     the create_* batch creators + assemble)
//
// pipeline_scan + pipeline_list_graphs are NOT advertised. They are
// index-gap-discovery infra, never LLM-facing tool calls — pipeline_scan rides its
// own typed EngineService.PipelineScan RPC (the client LLM pipeline's WireClient
// calls it directly, off Dispatch), and pipeline_list_graphs is composed from the
// generic Execute RETURN_MODE_GRAPH_NAMES read. Advertising them would
// deny-on-call (no engine compile arm, no intercept).
func AllToolSchemas() []kgtools.MCPTool {
	return []kgtools.MCPTool{
		// Generic consolidated primitives.
		QueryToolDef(),
		TraverseToolDef(),
		MutateToolDef(),
		DeleteToolDef(),
		ManageToolDef(),

		// Bidirectional Fulminate Cloud sync.
		SyncToolDef(),

		// First-class tools (formerly the server's FirstClassTools set).
		ThoughtsToolDef(),
		SearchToolDef(),
		FileSymbolsToolDef(),
		CollectToolDef(),

		// Remaining client-owned tools.
		WorkerToolDef(),
		GraphTypeToolDef(),
		AstToolDef(),
		HelpToolDef(),
		RecordDecisionToolDef(),
		CreatePlanToolDef(),
		CreateTicketToolDef(),
		CreateProjectToolDef(),
		CreateResearchToolDef(),
		CreateTestPlanToolDef(),
		AssembleToolDef(),
	}
}
