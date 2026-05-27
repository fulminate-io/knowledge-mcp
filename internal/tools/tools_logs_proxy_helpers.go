// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// proxyAnnotation mirrors the server-side helper (cmd/knowledge-server/
// tools/tools_traverse_proxy.go). Client-side log handlers receive
// *knowledgev1.Node values returned from server queries, so this metadata-only
// view stays useful — no DB lookup required. The proxy detection + target read
// defer to the shared kgwire reader (kgwire.IsProxy / kgwire.ProxyInfo), which
// returns the proto *knowledgev1.ProxyTarget.
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

// resolveProxyIfNeeded is the client-side stub for the server helper. The
// server helper consults its store to resolve proxy targets, which the client
// cannot do without a wire-trip. Until the BCN11 follow-up wires a ResolveProxy
// RPC, the client returns the metadata-only annotation — which is a strict
// subset of what the server returns. Branch proxies (kgwire.IsBranchProxy) are
// skipped: they reference the same ID in the base graph, no annotation needed.
func resolveProxyIfNeeded(_ context.Context, n *knowledgev1.Node) string {
	if !kgwire.IsProxy(n) {
		return ""
	}
	if kgwire.IsBranchProxy(n) {
		return ""
	}
	return proxyAnnotation(n)
}
