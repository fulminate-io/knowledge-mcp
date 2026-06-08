// SPDX-License-Identifier: Apache-2.0

// Package workercrud holds the client-side NodeWorker CRUD surface used
// by the worker MCP tool. The translation between workers.Worker (the
// in-memory configuration shape) and *knowledgev1.Node{Type: NodeWorker}
// (the graph-resident persisted shape) lives here because the wire
// loopback path still marshals a Node value into the upsert payload
// metadata.
//
// The contract mirrors the NodeLogBackend pattern in
// cmd/knowledge-server/tools/tools_logs_manage_backend.go: stable
// identity in Name + Type, body fields under Description, every
// remaining field carried in the Metadata map. List/array fields
// (ToolAllowlist, Triggers) are JSON-encoded into their map values so
// the inline metadata representation stays one-string-per-key.
package workercrud

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// Metadata keys persisted on a NodeWorker. Centralized so producers
// (WorkerToNode) and consumers (NodeToWorker, list/status renderers)
// cannot drift on key spelling.
const (
	metaProvider            = "provider"
	metaModel               = "model"
	metaBaseURL             = "base_url"
	metaSystemPrompt        = "system_prompt"
	metaToolAllowlist       = "tool_allowlist"        // JSON-encoded []string
	metaTriggers            = "triggers"              // JSON-encoded []workers.Trigger
	metaMaxIterations       = "max_iterations"        // decimal int as string
	metaMaxWallclockSeconds = "max_wallclock_seconds" // decimal int as string
	metaEnabled             = "enabled"               // "true" | "false"
)

// WorkerToNode marshals w into a *knowledgev1.Node ready for db.Upsert. The
// returned node's ID equals w.Name, so the caller does not need to round-
// trip through ByID before deciding create vs update — Upsert handles
// that on the storage side.
//
// Returns an error only when one of the JSON-encoded fields fails to
// marshal, which in practice never happens for the constituent types
// ([]string and []workers.Trigger). The error path is preserved for
// forward-compatibility (e.g. if Trigger gains a json.Marshaler that can
// fail).
func WorkerToNode(w workers.Worker) (*knowledgev1.Node, error) {
	allowlistJSON, err := json.Marshal(w.ToolAllowlist)
	if err != nil {
		return nil, fmt.Errorf("marshal tool_allowlist: %w", err)
	}
	triggers := w.Triggers
	if triggers == nil {
		triggers = []workers.Trigger{}
	}
	triggersJSON, err := json.Marshal(triggers)
	if err != nil {
		return nil, fmt.Errorf("marshal triggers: %w", err)
	}

	meta := map[string]string{
		metaProvider:            string(w.Provider),
		metaModel:               w.Model,
		metaBaseURL:             w.BaseURL,
		metaSystemPrompt:        w.SystemPrompt,
		metaToolAllowlist:       string(allowlistJSON),
		metaTriggers:            string(triggersJSON),
		metaMaxIterations:       strconv.Itoa(w.MaxIterations),
		metaMaxWallclockSeconds: strconv.Itoa(w.MaxWallclockSeconds),
		metaEnabled:             strconv.FormatBool(w.Enabled),
	}

	return &knowledgev1.Node{
		Id:          w.Name,
		Type:        string(kgtypes.NodeWorker),
		SymbolName:  w.Name,
		Source:      "worker:configure",
		Description: w.Description,
		Metadata:    meta,
	}, nil
}

// NodeToWorker is the inverse of WorkerToNode.
//
// Validation is intentionally lenient — missing or empty optional fields
// (Model, BaseURL, MaxIterations, MaxWallclockSeconds, Triggers) decode to
// their zero values. Required fields (Name, Provider) round-trip verbatim;
// workers.Worker.Validate is the canonical post-decode check.
func NodeToWorker(n *knowledgev1.Node) (workers.Worker, error) {
	if kgtypes.NodeType(n.GetType()) != kgtypes.NodeWorker {
		return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: expected type %q, got %q", kgtypes.NodeWorker, n.GetType())
	}
	w := workers.Worker{
		Name:         n.GetSymbolName(),
		Description:  n.GetDescription(),
		Provider:     config.Provider(kgtypes.Value(n, metaProvider)),
		Model:        kgtypes.Value(n, metaModel),
		BaseURL:      kgtypes.Value(n, metaBaseURL),
		SystemPrompt: kgtypes.Value(n, metaSystemPrompt),
	}
	if w.Name == "" {
		// Some legacy nodes may carry the name only on ID — fall back so
		// a hand-edited graph doesn't silently produce a nameless worker.
		w.Name = n.GetId()
	}

	if v := strings.TrimSpace(kgtypes.Value(n, metaToolAllowlist)); v != "" {
		var list []string
		if err := json.Unmarshal([]byte(v), &list); err != nil {
			return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: tool_allowlist: %w", err)
		}
		w.ToolAllowlist = list
	}
	if v := strings.TrimSpace(kgtypes.Value(n, metaTriggers)); v != "" {
		var triggers []workers.Trigger
		if err := json.Unmarshal([]byte(v), &triggers); err != nil {
			return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: triggers: %w", err)
		}
		w.Triggers = triggers
	}
	if v := strings.TrimSpace(kgtypes.Value(n, metaMaxIterations)); v != "" {
		nv, err := strconv.Atoi(v)
		if err != nil {
			return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: max_iterations: %w", err)
		}
		w.MaxIterations = nv
	}
	if v := strings.TrimSpace(kgtypes.Value(n, metaMaxWallclockSeconds)); v != "" {
		nv, err := strconv.Atoi(v)
		if err != nil {
			return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: max_wallclock_seconds: %w", err)
		}
		w.MaxWallclockSeconds = nv
	}
	if v := strings.TrimSpace(kgtypes.Value(n, metaEnabled)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return workers.Worker{}, fmt.Errorf("workercrud: NodeToWorker: enabled: %w", err)
		}
		w.Enabled = b
	}
	return w, nil
}
