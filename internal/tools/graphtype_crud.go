// SPDX-License-Identifier: Apache-2.0

// graphtype_crud.go — per-op handlers for the custom_collector tool's
// register/update/delete/list operations + the args->proto builder and the
// list renderer. The dispatch entry point lives in graphtype.go::
// InterceptGraphType; this file holds the per-op bodies.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleGraphTypeRegister builds the gen record from args and creates it. The
// CRUD client runs validateRegistration (record-shape + built-in collision)
// before the wire call, so structural problems surface at the caller.
func handleGraphTypeRegister(ctx context.Context, deps ClientDeps, a graphTypeArgs) kgtools.ToolResult {
	cc := deps.GraphTypeCRUD()
	if cc == nil {
		return errorResult("custom_collector:register: graphTypeCRUD not wired — constructClient degraded at boot")
	}
	if strings.TrimSpace(a.Name) == "" {
		return errorResult("custom_collector:register: name is required")
	}
	d, err := graphTypeDefFromArgs(a)
	if err != nil {
		return errorResult("custom_collector:register: " + err.Error())
	}
	if err := cc.Create(ctx, d); err != nil {
		return errorResult("custom_collector:register: " + err.Error())
	}
	return textResult(fmt.Sprintf("graph type %q registered (binary=%s transport=%s)",
		d.GetName(), d.GetCollector().GetBinaryPath(), d.GetCollector().GetParamTransport()))
}

// handleGraphTypeUpdate re-writes an existing record. Update is a full re-write
// (supply every field you want persisted), mirroring the worker update
// semantics, and enforces the same validateRegistration gate as register.
func handleGraphTypeUpdate(ctx context.Context, deps ClientDeps, a graphTypeArgs) kgtools.ToolResult {
	cc := deps.GraphTypeCRUD()
	if cc == nil {
		return errorResult("custom_collector:update: graphTypeCRUD not wired — constructClient degraded at boot")
	}
	if strings.TrimSpace(a.Name) == "" {
		return errorResult("custom_collector:update: name is required")
	}
	d, err := graphTypeDefFromArgs(a)
	if err != nil {
		return errorResult("custom_collector:update: " + err.Error())
	}
	if err := cc.Update(ctx, d); err != nil {
		return errorResult("custom_collector:update: " + err.Error())
	}
	return textResult(fmt.Sprintf("graph type %q updated", d.GetName()))
}

// handleGraphTypeDelete removes a registered graph type, classifying not-found
// via the graphclient.ErrNotFound the CRUD client maps from the wire.
func handleGraphTypeDelete(ctx context.Context, deps ClientDeps, a graphTypeArgs) kgtools.ToolResult {
	cc := deps.GraphTypeCRUD()
	if cc == nil {
		return errorResult("custom_collector:delete: graphTypeCRUD not wired — constructClient degraded at boot")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("custom_collector:delete: name is required")
	}
	if err := cc.Delete(ctx, name); err != nil {
		if errors.Is(err, graphclient.ErrNotFound) {
			return errorResult(fmt.Sprintf("custom_collector:delete: graph type %q not found (use custom_collector(operation:\"list\") to enumerate registered types)", name))
		}
		return errorResult("custom_collector:delete: " + err.Error())
	}
	return textResult(fmt.Sprintf("graph type %q deleted", name))
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

// --- args -> proto ---

// collectorArgs / behaviorArgs / overrideArgs mirror the nested schema objects.
// The booleans are *bool so an omitted key stays unset (proto field presence),
// distinguishing "inherit" from explicit false.
type collectorArgs struct {
	BinaryPath     string               `json:"binary_path"`
	ParamTransport string               `json:"param_transport"`
	ParamSchema    map[string]paramArgs `json:"param_schema"`
}

type paramArgs struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type behaviorArgs struct {
	Syncable        *bool             `json:"syncable"`
	Summarizable    *bool             `json:"summarizable"`
	Embeddable      *bool             `json:"embeddable"`
	EmbedFields     []string          `json:"embed_fields"`
	SummarizeFields []string          `json:"summarize_fields"`
	Bm25Fields      []string          `json:"bm25_fields"`
	Extra           map[string]string `json:"extra"`
}

type overrideArgs struct {
	Summarizable    *bool    `json:"summarizable"`
	Embeddable      *bool    `json:"embeddable"`
	EmbedFields     []string `json:"embed_fields"`
	SummarizeFields []string `json:"summarize_fields"`
	Bm25Fields      []string `json:"bm25_fields"`
}

// graphTypeDefFromArgs builds the gen *knowledgev1.GraphTypeDef from the parsed
// args, unmarshalling the collector/behavior/node_types RawMessage objects into
// the proto sub-messages. Returns an error only when a nested object is
// malformed JSON.
func graphTypeDefFromArgs(a graphTypeArgs) (*knowledgev1.GraphTypeDef, error) {
	d := &knowledgev1.GraphTypeDef{Name: strings.TrimSpace(a.Name)}

	if len(a.Collector) > 0 {
		var ca collectorArgs
		if err := json.Unmarshal(a.Collector, &ca); err != nil {
			return nil, fmt.Errorf("collector: %w", err)
		}
		col := &knowledgev1.CollectorSpec{
			BinaryPath:     ca.BinaryPath,
			ParamTransport: ca.ParamTransport,
		}
		if len(ca.ParamSchema) > 0 {
			col.ParamSchema = make(map[string]*knowledgev1.ParamSpec, len(ca.ParamSchema))
			for k, p := range ca.ParamSchema {
				col.ParamSchema[k] = &knowledgev1.ParamSpec{Type: p.Type, Required: p.Required}
			}
		}
		d.Collector = col
	}

	if len(a.Behavior) > 0 {
		var ba behaviorArgs
		if err := json.Unmarshal(a.Behavior, &ba); err != nil {
			return nil, fmt.Errorf("behavior: %w", err)
		}
		d.Behavior = &knowledgev1.BehaviorDefaults{
			Syncable:        ba.Syncable,
			Summarizable:    ba.Summarizable,
			Embeddable:      ba.Embeddable,
			EmbedFields:     ba.EmbedFields,
			SummarizeFields: ba.SummarizeFields,
			Bm25Fields:      ba.Bm25Fields,
			Extra:           ba.Extra,
		}
	}

	if len(a.NodeTypes) > 0 {
		var nts map[string]overrideArgs
		if err := json.Unmarshal(a.NodeTypes, &nts); err != nil {
			return nil, fmt.Errorf("node_types: %w", err)
		}
		if len(nts) > 0 {
			d.NodeTypes = make(map[string]*knowledgev1.NodeTypeOverride, len(nts))
			for nt, ov := range nts {
				d.NodeTypes[nt] = &knowledgev1.NodeTypeOverride{
					Summarizable:    ov.Summarizable,
					Embeddable:      ov.Embeddable,
					EmbedFields:     ov.EmbedFields,
					SummarizeFields: ov.SummarizeFields,
					Bm25Fields:      ov.Bm25Fields,
				}
			}
		}
	}

	return d, nil
}

// --- render ---

// graphTypesAsJSON returns a marshaling-friendly view of the record list for
// list with format=json. Surfaces collector + behavior over the proto getters.
func graphTypesAsJSON(defs []*knowledgev1.GraphTypeDef) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		b := d.GetBehavior() // nil-safe getter; b may be nil
		out = append(out, map[string]any{
			"name":        d.GetName(),
			"binary_path": d.GetCollector().GetBinaryPath(),
			"transport":   d.GetCollector().GetParamTransport(),
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

// formatGraphTypesTable renders the list as a markdown table surfacing name +
// collector + behavior cascade flags. Same empty-case messaging shape as
// worker:list / log_backend:list.
func formatGraphTypesTable(defs []*knowledgev1.GraphTypeDef) string {
	if len(defs) == 0 {
		return "No graph types registered. Use custom_collector(operation: \"register\", ...) to add one."
	}
	var sb strings.Builder
	sb.WriteString("| name | binary | transport | syncable | summarizable | embeddable | overrides |\n")
	sb.WriteString("|------|--------|-----------|----------|--------------|------------|-----------|\n")
	for _, d := range defs {
		b := d.GetBehavior()
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s | %d |\n",
			d.GetName(),
			emptyDash(d.GetCollector().GetBinaryPath()),
			emptyDash(d.GetCollector().GetParamTransport()),
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
