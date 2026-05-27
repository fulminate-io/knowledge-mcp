// SPDX-License-Identifier: Apache-2.0

// Package render assembles client-side, human-readable views of
// project/ticket/plan/phase/step/research/test_plan/decision/pattern
// nodes from the knowledge graph. Per the client/server split locked
// for FUL-241/FUL-251, the server stores nodes and serves them; this
// package walks the wire-returned shapes and renders the markdown.
// The server-side assemble dispatch (cmd/knowledge-server/tools/
// tools_assemble.go) collapses to a client-intercept-required sentinel once
// Phase 4 lands; intercept_assemble.go in cmd/knowledge/internal/
// tools claims the call and delegates to render.Handle.
//
// proxyAnnotation here is functionally equivalent to (and
// intentionally duplicated from) cmd/knowledge/internal/tools/
// tools_logs_proxy_helpers.go:16. We cannot import that file because
// tools/intercept_assemble.go imports this package, which would form
// a cycle. The duplication is the cycle break.
//
// Client-side proxies render via inline metadata only; server-side
// resolveProxyIfNeeded (cmd/knowledge-server/tools/
// tools_traverse_proxy.go:44-77) enrichment relies on store.ResolveProxy
// against the in-process store and is not reachable through MCP tools.
// Tree walks call proxyAnnotation directly instead of
// resolveProxyIfNeeded. Acceptable degradation: golden files capture
// the actual client-side output post-relocation, not the server-side
// enrichment.
package render

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// truncate shortens s to n characters, appending "..." if truncated.
// Verbatim port of cmd/knowledge-server/tools/helpers.go:132-138.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// proxyAnnotation returns the inline-metadata view of a proxy node,
// or empty string when the node is not a proxy. Mirrors the body of
// cmd/knowledge/internal/tools/tools_logs_proxy_helpers.go:16-38 —
// duplicated here to break the render↔tools import cycle (see
// package doc). Operates over the wire *knowledgev1.Node via the shared
// kgwire proxy reader (kgwire.IsProxy / kgwire.ProxyInfo), which reads
// the proto carrier the client holds post-FUL-295 retype and returns the
// proto *knowledgev1.ProxyTarget (graph_type / name / node_id getters).
func proxyAnnotation(n *knowledgev1.Node) string {
	if !kgwire.IsProxy(n) {
		return ""
	}
	info := kgwire.ProxyInfo(n)
	if info == nil {
		return "[proxy]"
	}
	var parts []string
	if info.GetGraphType() != "" {
		parts = append(parts, info.GetGraphType())
	}
	if info.GetName() != "" {
		parts = append(parts, info.GetName())
	}
	if info.GetNodeId() != "" {
		parts = append(parts, info.GetNodeId())
	}
	if len(parts) == 0 {
		return "[proxy]"
	}
	return "[proxy → " + strings.Join(parts, ":") + "]"
}
