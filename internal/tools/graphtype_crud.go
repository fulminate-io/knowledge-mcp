// SPDX-License-Identifier: Apache-2.0

// graphtype_crud.go — the custom_collector tool's ONE operation, `list`, and the
// two renderers over it. The dispatch entry point lives in graphtype.go::
// InterceptGraphType; this file holds the body.
//
// THE WRITE HANDLERS WENT WITH THE REGISTRATION CONTRACT. register, update and
// delete wrote a record the config file now owns, and their args-to-proto
// builder went with them; the live half of their provider check (dial the
// provider, assert the tool's schemas) moved to `knowledge collector add`, which
// dials before writing the entry, and the collect path runs the same check
// through the same verifiedTool it always did.

package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// renderProvider names the provider transport a record carries, for the list
// table. Under the config-file contract a freshly written record carries NO
// provider and renders "-", which is the ordinary case rather than the odd one:
// the connection half lives in the file and never reaches the server. A record
// that does render one is a legacy record from the retired register tool.
func renderProvider(col *knowledgev1.CollectorSpec) string {
	switch {
	case col.GetStdio() != nil:
		return "stdio:" + col.GetStdio().GetCommand()
	case col.GetHttp() != nil:
		return "http:" + col.GetHttp().GetUrl()
	default:
		return "-"
	}
}

// handleGraphTypeList enumerates registered graph types, sorted by name for
// deterministic output. Surfaces name + collector + behavior so users see what
// is registered.
func handleGraphTypeList(ctx context.Context, deps ClientDeps, a graphTypeArgs) kgtools.ToolResult {
	cc := deps.GraphTypeCRUD()
	if cc == nil {
		return errorResult("custom_collector:list: graphTypeCRUD not wired — constructClient degraded at boot")
	}
	defs, err := cc.List(ctx)
	if err != nil {
		return errorResult("custom_collector:list: " + err.Error())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].GetName() < defs[j].GetName() })
	if a.Format == "json" {
		return jsonResult(graphTypesAsJSON(defs))
	}
	return textResult(formatGraphTypesTable(defs))
}

// --- render ---

// graphTypesAsJSON returns a marshaling-friendly view of the record list for
// list with format=json. Surfaces collector + behavior over the proto getters.
func graphTypesAsJSON(defs []*knowledgev1.GraphTypeDef) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		b := d.GetBehavior() // nil-safe getter; b may be nil
		out = append(out, map[string]any{
			"name":     d.GetName(),
			"tool":     d.GetCollector().GetTool(),
			"provider": renderProvider(d.GetCollector()),
			"behavior": map[string]any{
				"syncable":         behaviorBool(b, func(x *knowledgev1.BehaviorDefaults) *bool { return x.Syncable }),
				"summarizable":     behaviorBool(b, func(x *knowledgev1.BehaviorDefaults) *bool { return x.Summarizable }),
				"embeddable":       behaviorBool(b, func(x *knowledgev1.BehaviorDefaults) *bool { return x.Embeddable }),
				"embed_fields":     b.GetEmbedFields(),
				"summarize_fields": b.GetSummarizeFields(),
				"bm25_fields":      b.GetBm25Fields(),
				"extra":            b.GetExtra(),
			},
			"node_type_overrides": len(d.GetNodeTypes()),
		})
	}
	return out
}

// behaviorBool extracts a tri-state behavior pointer nil-safely: nil behavior or
// an unset field yields nil (omitted in JSON); a set field yields its *bool.
func behaviorBool(b *knowledgev1.BehaviorDefaults, pick func(*knowledgev1.BehaviorDefaults) *bool) *bool {
	if b == nil {
		return nil
	}
	return pick(b)
}

// emptyDash returns "-" for empty strings so the markdown table cells stay
// visually balanced. Mirrors orDash() in tools_logs_manage_backend.go — kept
// package-private, and it lives here because formatGraphTypesTable below is now
// its only caller. It was previously defined in worker_render.go, alongside a
// second caller that went with the worker tool.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatGraphTypesTable renders the list as a markdown table surfacing name +
// collector + behavior cascade flags. The empty case names the registration
// path rather than reporting a bare zero, so a caller who has registered
// nothing yet is told what to do next.
func formatGraphTypesTable(defs []*knowledgev1.GraphTypeDef) string {
	if len(defs) == 0 {
		return "No custom collector families are known to the server. Registration is a config file: `knowledge collector add` writes an entry, and the family's behavior record appears here after its first collect."
	}
	var sb strings.Builder
	sb.WriteString("| name | provider | tool | syncable | summarizable | embeddable | overrides |\n")
	sb.WriteString("|------|----------|------|----------|--------------|------------|-----------|\n")
	for _, d := range defs {
		b := d.GetBehavior()
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s | %d |\n",
			d.GetName(),
			emptyDash(renderProvider(d.GetCollector())),
			emptyDash(d.GetCollector().GetTool()),
			triBool(b.GetSyncable, b != nil && b.Syncable != nil),
			triBool(b.GetSummarizable, b != nil && b.Summarizable != nil),
			triBool(b.GetEmbeddable, b != nil && b.Embeddable != nil),
			len(d.GetNodeTypes()),
		)
	}
	return sb.String()
}

// triBool renders a tri-state behavior flag: "-" when unset (inherit), else the
// bool value. get returns the dereferenced value; set reports presence.
func triBool(get func() bool, set bool) string {
	if !set {
		return "-"
	}
	return fmt.Sprintf("%v", get())
}
