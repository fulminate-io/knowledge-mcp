// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resolveEdges resolves raw edge IDs (pkg.Symbol or pkg.Receiver.Method format)
// to actual node IDs (filepath:Symbol format used by the graph). Uses an in-memory
// symbol map for same-build resolution.
//
// Edges where both FromID and ToID can be resolved are included in the result.
// Unresolvable edges (e.g., calls to stdlib) are dropped.
// IMPORTS edges are passed through unchanged.
func resolveEdges(edges []*knowledgev1.Edge, symbolMap map[string]string, nodeIDs map[string]bool) []*knowledgev1.Edge {
	// Build reverse lookup: bare symbol name → []nodeID for cross-package resolution.
	byName := make(map[string][]string)
	// Build pkg:method → []nodeID for same-package method resolution when
	// the symbolMap key includes a receiver type (e.g., "store.db.Retrieve").
	byPkgName := make(map[string][]string)
	for qualName, nodeID := range symbolMap {
		parts := strings.Split(qualName, ".")
		if len(parts) >= 2 {
			method := parts[len(parts)-1]
			byName[method] = append(byName[method], nodeID)
			pkg := parts[0]
			byPkgName[pkg+":"+method] = append(byPkgName[pkg+":"+method], nodeID)
		}
	}

	var resolved []*knowledgev1.Edge
	for _, edge := range edges {
		// IMPORTS edges use file path → import path; no resolution needed.
		// The edges are freshly built by chunkResultsToPopulate / ConvertEdges
		// (not shared), so the pointer is appended directly.
		if kgtypes.EdgeType(edge.Type) == kgtypes.EdgeImports {
			resolved = append(resolved, edge)
			continue
		}

		origFrom := edge.FromId

		// Resolve FromID.
		newFrom := resolveEdgeID(edge.FromId, "", "", symbolMap, nodeIDs, byName, byPkgName)
		if newFrom == "" {
			continue
		}

		// Extract caller's package and receiver from original qualified name.
		// Format: "pkg.Func" or "pkg.Receiver.Method"
		callerPkg, callerReceiver := extractCallerContext(origFrom)

		// Resolve ToID.
		newTo := resolveEdgeID(edge.ToId, callerPkg, callerReceiver, symbolMap, nodeIDs, byName, byPkgName)
		if newTo == "" {
			continue
		}

		edge.FromId = newFrom
		edge.ToId = newTo
		resolved = append(resolved, edge)
	}
	return resolved
}

// extractCallerContext extracts the Go package name and receiver type from a
// qualified edge ID. For "store.db.Retrieve" returns ("store", "db").
// For "store.Open" returns ("store", "").
func extractCallerContext(qualName string) (pkg, receiver string) {
	parts := strings.Split(qualName, ".")
	if len(parts) >= 2 {
		pkg = parts[0]
	}
	if len(parts) >= 3 {
		receiver = parts[1]
	}
	return
}

// resolveEdgeID attempts to resolve a raw ID to a graph node ID.
//
// Resolution order:
//  1. Direct qualified name lookup in symbolMap (e.g., "store.db.Retrieve")
//  2. Already a known node ID (e.g., file path for CONTAINS edges)
//  3. Same-package resolution:
//     a. Receiver-qualified: callerPkg.callerReceiver.id (e.g., "store.db.Retrieve")
//     b. Direct: callerPkg.id (e.g., "store.Open")
//     c. Fuzzy: any same-package symbol with this name (unambiguous only)
//  4. Unambiguous cross-package lookup (bare name found in exactly one package)
//  5. For dotted IDs (e.g., "g.Close" from variable method calls): extract the last
//     segment and retry steps 3-4 with just the method name
//
// Returns "" if unresolvable.
func resolveEdgeID(id, callerPkg, callerReceiver string, symbolMap map[string]string, nodeIDs map[string]bool, byName, byPkgName map[string][]string) string {
	// 1. Direct qualified name lookup.
	if nodeID, ok := symbolMap[id]; ok {
		return nodeID
	}

	// 2. Already a valid node ID (file paths for CONTAINS FromID, etc.).
	if nodeIDs[id] {
		return id
	}

	// 3a. Same-package receiver-qualified (caller and callee share receiver type).
	if callerPkg != "" && callerReceiver != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+callerReceiver+"."+id]; ok {
			return nodeID
		}
	}
	// 3b. Same-package direct (standalone function in same package).
	if callerPkg != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+id]; ok {
			return nodeID
		}
		// 3c. Fuzzy same-package: any receiver type with this method (unambiguous only).
		if candidates := byPkgName[callerPkg+":"+id]; len(candidates) == 1 {
			return candidates[0]
		}
	}

	// 4. Unambiguous cross-package resolution.
	if candidates := byName[id]; len(candidates) == 1 {
		return candidates[0]
	}

	// 5. For dotted IDs like "g.Close" (method calls on variables), extract the
	// last segment and retry resolution with just the method name.
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		if nodeID := resolveDottedEdgeCallee(id[idx+1:], callerPkg, callerReceiver, symbolMap, byName, byPkgName); nodeID != "" {
			return nodeID
		}
	}

	return ""
}

// resolveDottedEdgeCallee resolves a method name extracted from a dotted ID
// (e.g., "Close" from "g.Close") by retrying same-package and cross-package lookups.
func resolveDottedEdgeCallee(methodName, callerPkg, callerReceiver string, symbolMap map[string]string, byName, byPkgName map[string][]string) string {
	// Try receiver-qualified (same receiver calling own method).
	if callerPkg != "" && callerReceiver != "" {
		if nodeID, ok := symbolMap[callerPkg+"."+callerReceiver+"."+methodName]; ok {
			return nodeID
		}
	}
	if callerPkg != "" {
		// Try direct same-package.
		if nodeID, ok := symbolMap[callerPkg+"."+methodName]; ok {
			return nodeID
		}
		// Try fuzzy same-package (unambiguous only).
		if candidates := byPkgName[callerPkg+":"+methodName]; len(candidates) == 1 {
			return candidates[0]
		}
	}
	if candidates := byName[methodName]; len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}
